#!/usr/bin/env bash

# Run the complete local test stack against disposable etcd, PostgreSQL, and
# Kafka services. The integration tests are destructive: never replace these
# endpoints with services that contain real data.

set -euo pipefail

readonly SCRIPT_DIR="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)"
readonly PROJECT_ROOT="$(cd -- "${SCRIPT_DIR}/.." && pwd)"
readonly ETCD_CONTAINER="fastrg-tests-etcd"
readonly POSTGRES_CONTAINER="fastrg-tests-postgres"
readonly KAFKA_CONTAINER="fastrg-tests-kafka"

if [ "$#" -ne 0 ]; then
    echo "Usage: $0" >&2
    exit 2
fi

readonly TEST_TMP_DIR="$(mktemp -d)"
readonly COVERAGE_FILE="${PROJECT_ROOT}/coverage.out"

declare -a started_containers=()
declare -a paused_containers=()

SETUP_STATUS="FAIL"
GO_TEST_STATUS="NOT RUN"
REST_SMOKE_STATUS="NOT RUN"
FULL_STACK_E2E_STATUS="FAIL"
COVERAGE_TOTAL="unavailable"
OVERALL_STATUS=0

print_summary() {
    echo
    echo "==================== TEST SUMMARY ===================="
    printf 'Environment setup:                 %s\n' "$SETUP_STATUS"
    printf 'Unit tests:                       %s\n' "$GO_TEST_STATUS"
    printf 'etcd/PostgreSQL integration:      %s\n' "$GO_TEST_STATUS"
    printf 'Kafka/projection in-process e2e:  %s\n' "$GO_TEST_STATUS"
    printf 'REST smoke (50 assertions):       %s\n' "$REST_SMOKE_STATUS"
    printf 'Full-stack e2e:                   %s\n' "$FULL_STACK_E2E_STATUS"
    printf 'Coverage:                         %s\n' "$COVERAGE_TOTAL"
    echo "======================================================"
}

cleanup_resources() {
    local cleanup_status=0

    if ((${#started_containers[@]} > 0)); then
        echo "Stopping throwaway test services..."
        docker stop "${started_containers[@]}" >/dev/null || cleanup_status=1
        started_containers=()
    fi
    if ((${#paused_containers[@]} > 0)); then
        echo "Restarting previously running containers: ${paused_containers[*]}"
        docker start "${paused_containers[@]}" >/dev/null || cleanup_status=1
        paused_containers=()
    fi
    rm -rf -- "$TEST_TMP_DIR"

    return "$cleanup_status"
}

on_exit() {
    local exit_code=$?
    trap - EXIT
    set +e

    if ! cleanup_resources; then
        echo "ERROR: one or more test resources could not be restored or removed" >&2
        exit_code=1
    fi
    print_summary

    if [ "$OVERALL_STATUS" -ne 0 ]; then
        exit_code="$OVERALL_STATUS"
    fi
    exit "$exit_code"
}

trap on_exit EXIT

require_command() {
    if ! command -v "$1" >/dev/null 2>&1; then
        echo "ERROR: required command '$1' was not found" >&2
        return 1
    fi
}

# wait_ready <description> <command...>: poll up to 60x2s, then fail loudly
# instead of hanging forever on a container that never comes up.
wait_ready() {
    local description="$1"
    shift
    for _ in $(seq 1 60); do
        if "$@" >/dev/null 2>&1; then
            return 0
        fi
        sleep 2
    done
    echo "ERROR: $description did not become ready within 120s" >&2
    return 1
}

require_command docker
require_command go

# This dev machine keeps long-lived throwaway containers on the integration
# ports. Pause only containers that are currently running and restore exactly
# those containers from the EXIT trap.
for existing in fastrg-test-etcd fastrg-test-pg; do
    if [ "$(docker inspect -f '{{.State.Running}}' "$existing" 2>/dev/null)" = "true" ]; then
        echo "Stopping conflicting container $existing (will restart on exit)..."
        docker stop "$existing" >/dev/null
        paused_containers+=("$existing")
    fi
done

echo "Starting throwaway etcd..."
docker run --rm --detach \
    --name "$ETCD_CONTAINER" \
    --publish 12379:2379 \
    quay.io/coreos/etcd:v3.5.17 \
    etcd \
    --name=etcd0 \
    --advertise-client-urls=http://localhost:12379 \
    --listen-client-urls=http://0.0.0.0:2379 \
    --initial-cluster-state=new >/dev/null
started_containers+=("$ETCD_CONTAINER")

echo "Starting throwaway PostgreSQL..."
docker run --rm --detach \
    --name "$POSTGRES_CONTAINER" \
    --publish 15432:5432 \
    --env POSTGRES_USER=fastrg \
    --env POSTGRES_PASSWORD=fastrg \
    --env POSTGRES_DB=fastrg \
    postgres:16-alpine >/dev/null
started_containers+=("$POSTGRES_CONTAINER")

echo "Starting throwaway Kafka..."
docker run --rm --detach \
    --name "$KAFKA_CONTAINER" \
    --publish 19092:19092 \
    --env KAFKA_NODE_ID=1 \
    --env KAFKA_PROCESS_ROLES=broker,controller \
    --env KAFKA_LISTENERS=PLAINTEXT://:19092,CONTROLLER://:19093 \
    --env KAFKA_ADVERTISED_LISTENERS=PLAINTEXT://localhost:19092 \
    --env KAFKA_CONTROLLER_QUORUM_VOTERS=1@localhost:19093 \
    --env KAFKA_CONTROLLER_LISTENER_NAMES=CONTROLLER \
    --env KAFKA_LISTENER_SECURITY_PROTOCOL_MAP=PLAINTEXT:PLAINTEXT,CONTROLLER:PLAINTEXT \
    --env KAFKA_INTER_BROKER_LISTENER_NAME=PLAINTEXT \
    --env KAFKA_OFFSETS_TOPIC_REPLICATION_FACTOR=1 \
    --env KAFKA_TRANSACTION_STATE_LOG_REPLICATION_FACTOR=1 \
    --env KAFKA_TRANSACTION_STATE_LOG_MIN_ISR=1 \
    --env KAFKA_AUTO_CREATE_TOPICS_ENABLE=true \
    apache/kafka:3.9.0 >/dev/null
started_containers+=("$KAFKA_CONTAINER")

echo "Waiting for throwaway services to become ready..."
wait_ready "etcd" docker exec "$ETCD_CONTAINER" etcdctl endpoint health
wait_ready "PostgreSQL" docker exec "$POSTGRES_CONTAINER" pg_isready -U fastrg
wait_ready "Kafka" docker exec "$KAFKA_CONTAINER" /opt/kafka/bin/kafka-topics.sh \
    --bootstrap-server localhost:19092 --list

export TEST_ETCD_ENDPOINTS=127.0.0.1:12379
export TEST_DATABASE_URL='postgres://fastrg:fastrg@localhost:15432/fastrg?sslmode=disable'
export TEST_KAFKA_BROKERS=localhost:19092
export GOCACHE="${TEST_TMP_DIR}/go-build"

SETUP_STATUS="PASS"

echo "Running unit, integration, and in-process e2e tests with coverage..."
rm -f -- "$COVERAGE_FILE"
if (
    cd "$PROJECT_ROOT"
    go test -count=1 -coverpkg=./internal/... -coverprofile="$COVERAGE_FILE" ./...
); then
    GO_TEST_STATUS="PASS"
else
    GO_TEST_STATUS="FAIL"
    OVERALL_STATUS=1
fi

# The REST smoke suite drives a real controller binary; instrument it so its
# handler coverage is captured. The controller writes binary coverage data into
# GOCOVERDIR (SMOKE_COVER_DIR) on graceful exit; test_script.sh only rebuilds
# with -cover and forwards GOCOVERDIR when this variable is set.
SMOKE_COVER_DIR="${TEST_TMP_DIR}/smoke-covdata"
mkdir -p "$SMOKE_COVER_DIR"
export SMOKE_COVER_DIR

echo "Running REST smoke suite on dedicated local ports..."
if "$SCRIPT_DIR/test_script.sh" 127.0.0.1 run_all_tests; then
    REST_SMOKE_STATUS="PASS"
else
    REST_SMOKE_STATUS="FAIL"
    OVERALL_STATUS=1
fi

# Merge the smoke binary-coverage data into the go test profile so both the
# README table and the summary total reflect handler paths exercised only by the
# smoke suite. Only attempt this when the smoke run passed (a clean shutdown is
# what flushes the data) and the go profile exists. covdata textfmt emits its own
# `mode:` header line, so drop it before appending; the merged profile then has a
# single header followed by go blocks and smoke blocks. Duplicate blocks are safe
# under set mode: both the README updater's dedup aggregation and
# `go tool cover -func` OR-merge repeated blocks, producing equivalent totals.
# A conversion failure only warns and skips the append; it never fails the run.
if [ "$REST_SMOKE_STATUS" = "PASS" ] && [ -s "$COVERAGE_FILE" ]; then
    smoke_profile="${TEST_TMP_DIR}/smoke.out"
    # -pkg restricts the emitted profile to internal/... blocks. The smoke binary
    # is instrumented with -coverpkg=./... (main must be instrumented for the
    # exit-time writer to run), so its raw covdata also carries main/proto blocks;
    # the README updater only accepts internal/... paths, so filter here.
    if go tool covdata textfmt -i="$SMOKE_COVER_DIR" -pkg=fastrg-controller/internal/... -o "$smoke_profile"; then
        tail -n +2 "$smoke_profile" >> "$COVERAGE_FILE"
        echo "Merged REST smoke coverage into $COVERAGE_FILE"
    else
        echo "WARNING: failed to convert smoke coverage data; README/total will reflect go tests only" >&2
    fi
fi

# Regenerate the README coverage table from the (now smoke-augmented) profile.
# Gated on the go layer passing: when go tests fail there is no trustworthy
# profile to publish. When the smoke append did not happen (smoke failed or the
# conversion warned) the table simply degrades to the go-only numbers, which are
# still correct. An updater failure warns but never fails the run.
if [ "$GO_TEST_STATUS" = "PASS" ]; then
    if "$SCRIPT_DIR/update_readme_coverage.sh"; then
        echo "Readme coverage table updated"
    else
        echo "WARNING: failed to update Readme coverage table; continuing test run" >&2
    fi
fi

# Compute the summary total after the smoke merge so it matches the README table.
if [ -s "$COVERAGE_FILE" ]; then
    if coverage_line=$(go tool cover -func="$COVERAGE_FILE" | tail -1); then
        COVERAGE_TOTAL="$coverage_line"
    fi
fi

echo "Running full-stack failure and recovery e2e suite..."
if "$PROJECT_ROOT/e2e_test/run_e2e_test.sh"; then
    FULL_STACK_E2E_STATUS="PASS"
else
    FULL_STACK_E2E_STATUS="FAIL"
    OVERALL_STATUS=1
fi

exit "$OVERALL_STATUS"
