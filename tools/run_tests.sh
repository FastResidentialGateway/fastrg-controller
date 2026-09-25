#!/usr/bin/env bash

# Run the complete local test stack against disposable etcd, PostgreSQL, and
# Kafka services. The integration tests are destructive: never replace these
# endpoints with services that contain real data.

set -euo pipefail

readonly SCRIPT_DIR="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)"
readonly PROJECT_ROOT="$(cd -- "${SCRIPT_DIR}/.." && pwd)"

# shellcheck source=tools/test_deps.sh
source "${SCRIPT_DIR}/test_deps.sh"

if [ "$#" -ne 0 ]; then
    echo "Usage: $0" >&2
    exit 2
fi

readonly TEST_TMP_DIR="$(mktemp -d)"
readonly COVERAGE_FILE="${PROJECT_ROOT}/coverage.out"

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
    printf 'Go tests (unit + integration + in-process e2e): %s\n' "$GO_TEST_STATUS"
    printf 'REST smoke:                       %s\n' "$REST_SMOKE_STATUS"
    printf 'Full-stack e2e:                   %s\n' "$FULL_STACK_E2E_STATUS"
    printf 'Coverage:                         %s\n' "$COVERAGE_TOTAL"
    echo "======================================================"
}

cleanup_resources() {
    local cleanup_status=0

    test_deps_stop || cleanup_status=1
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

require_command go

test_deps_start --with-kafka
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
