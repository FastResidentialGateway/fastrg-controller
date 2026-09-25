#!/usr/bin/env bash

# Throwaway etcd / PostgreSQL / Kafka containers for the integration tests.
# The tests are destructive: never point TEST_* at services holding real data.
#
# Sourced: provides test_deps_start [--with-kafka] and test_deps_stop.
# Executed: `tools/test_deps.sh <command...>` starts etcd + PostgreSQL, runs the
# command with TEST_* exported, and always stops the containers afterwards.

TEST_DEPS_ETCD_CONTAINER="fastrg-tests-etcd"
TEST_DEPS_POSTGRES_CONTAINER="fastrg-tests-postgres"
TEST_DEPS_KAFKA_CONTAINER="fastrg-tests-kafka"
TEST_DEPS_ETCD_PORT=12379
TEST_DEPS_POSTGRES_PORT=15432
TEST_DEPS_KAFKA_PORT=19092

# Containers this shell started, and long-lived ones it stopped to free ports.
declare -a test_deps_started=()
declare -a test_deps_paused=()

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

# Refuse a busy port so the tests never reach a service this script did not start.
test_deps_require_free_port() {
    local port="$1"
    if ss -H -ltn "sport = :$port" 2>/dev/null | grep -q .; then
        echo "ERROR: local port $port is already in use; refusing to run integration tests against a service this script did not start" >&2
        return 1
    fi
}

# test_deps_start [--with-kafka]: start the containers, wait until ready, export TEST_*.
test_deps_start() {
    local with_kafka=0
    if [ "${1:-}" = "--with-kafka" ]; then
        with_kafka=1
    fi

    require_command docker || return 1
    require_command ss || return 1

    # This dev machine keeps long-lived throwaway containers on the integration
    # ports. Pause only containers that are currently running; test_deps_stop
    # restores exactly those.
    local existing
    for existing in fastrg-test-etcd fastrg-test-pg; do
        if [ "$(docker inspect -f '{{.State.Running}}' "$existing" 2>/dev/null)" = "true" ]; then
            echo "Stopping conflicting container $existing (will restart on exit)..."
            docker stop "$existing" >/dev/null || return 1
            test_deps_paused+=("$existing")
        fi
    done

    test_deps_require_free_port "$TEST_DEPS_ETCD_PORT" || return 1
    test_deps_require_free_port "$TEST_DEPS_POSTGRES_PORT" || return 1
    if [ "$with_kafka" -eq 1 ]; then
        test_deps_require_free_port "$TEST_DEPS_KAFKA_PORT" || return 1
    fi

    echo "Starting throwaway etcd..."
    docker run --rm --detach \
        --name "$TEST_DEPS_ETCD_CONTAINER" \
        --publish "$TEST_DEPS_ETCD_PORT:2379" \
        quay.io/coreos/etcd:v3.5.17 \
        etcd \
        --name=etcd0 \
        --advertise-client-urls="http://localhost:$TEST_DEPS_ETCD_PORT" \
        --listen-client-urls=http://0.0.0.0:2379 \
        --initial-cluster-state=new >/dev/null || return 1
    test_deps_started+=("$TEST_DEPS_ETCD_CONTAINER")

    echo "Starting throwaway PostgreSQL..."
    docker run --rm --detach \
        --name "$TEST_DEPS_POSTGRES_CONTAINER" \
        --publish "$TEST_DEPS_POSTGRES_PORT:5432" \
        --env POSTGRES_USER=fastrg \
        --env POSTGRES_PASSWORD=fastrg \
        --env POSTGRES_DB=fastrg \
        postgres:16-alpine >/dev/null || return 1
    test_deps_started+=("$TEST_DEPS_POSTGRES_CONTAINER")

    if [ "$with_kafka" -eq 1 ]; then
        echo "Starting throwaway Kafka..."
        docker run --rm --detach \
            --name "$TEST_DEPS_KAFKA_CONTAINER" \
            --publish "$TEST_DEPS_KAFKA_PORT:19092" \
            --env KAFKA_NODE_ID=1 \
            --env KAFKA_PROCESS_ROLES=broker,controller \
            --env KAFKA_LISTENERS=PLAINTEXT://:19092,CONTROLLER://:19093 \
            --env KAFKA_ADVERTISED_LISTENERS="PLAINTEXT://localhost:$TEST_DEPS_KAFKA_PORT" \
            --env KAFKA_CONTROLLER_QUORUM_VOTERS=1@localhost:19093 \
            --env KAFKA_CONTROLLER_LISTENER_NAMES=CONTROLLER \
            --env KAFKA_LISTENER_SECURITY_PROTOCOL_MAP=PLAINTEXT:PLAINTEXT,CONTROLLER:PLAINTEXT \
            --env KAFKA_INTER_BROKER_LISTENER_NAME=PLAINTEXT \
            --env KAFKA_OFFSETS_TOPIC_REPLICATION_FACTOR=1 \
            --env KAFKA_TRANSACTION_STATE_LOG_REPLICATION_FACTOR=1 \
            --env KAFKA_TRANSACTION_STATE_LOG_MIN_ISR=1 \
            --env KAFKA_AUTO_CREATE_TOPICS_ENABLE=true \
            apache/kafka:3.9.0 >/dev/null || return 1
        test_deps_started+=("$TEST_DEPS_KAFKA_CONTAINER")
    fi

    echo "Waiting for throwaway services to become ready..."
    wait_ready "etcd" docker exec "$TEST_DEPS_ETCD_CONTAINER" etcdctl endpoint health || return 1
    wait_ready "PostgreSQL" docker exec "$TEST_DEPS_POSTGRES_CONTAINER" pg_isready -U fastrg || return 1
    if [ "$with_kafka" -eq 1 ]; then
        wait_ready "Kafka" docker exec "$TEST_DEPS_KAFKA_CONTAINER" /opt/kafka/bin/kafka-topics.sh \
            --bootstrap-server "localhost:$TEST_DEPS_KAFKA_PORT" --list || return 1
        export TEST_KAFKA_BROKERS="localhost:$TEST_DEPS_KAFKA_PORT"
    fi

    export TEST_ETCD_ENDPOINTS="127.0.0.1:$TEST_DEPS_ETCD_PORT"
    export TEST_DATABASE_URL="postgres://fastrg:fastrg@localhost:$TEST_DEPS_POSTGRES_PORT/fastrg?sslmode=disable"
}

# test_deps_stop: gracefully stop the containers we started and restart the paused ones.
test_deps_stop() {
    local status=0

    if ((${#test_deps_started[@]} > 0)); then
        echo "Stopping throwaway test services..."
        docker stop "${test_deps_started[@]}" >/dev/null || status=1
        test_deps_started=()
    fi
    if ((${#test_deps_paused[@]} > 0)); then
        echo "Restarting previously running containers: ${test_deps_paused[*]}"
        docker start "${test_deps_paused[@]}" >/dev/null || status=1
        test_deps_paused=()
    fi

    return "$status"
}

test_deps_on_exit() {
    local exit_code=$?
    trap - EXIT
    set +e

    if ! test_deps_stop; then
        echo "ERROR: one or more test containers could not be stopped or restored" >&2
        [ "$exit_code" -ne 0 ] || exit_code=1
    fi
    exit "$exit_code"
}

if [ "${BASH_SOURCE[0]}" = "$0" ]; then
    set -euo pipefail
    if [ "$#" -eq 0 ]; then
        echo "Usage: $0 <command...>" >&2
        exit 2
    fi
    trap test_deps_on_exit EXIT
    trap 'exit 130' INT
    trap 'exit 143' TERM
    test_deps_start
    "$@"
fi
