#!/usr/bin/env bash

# Phase 3: Controller failure and recovery
# Verify that the controller serves and projects config changes made while it
# is stopped. Works both locally with Docker Compose and remotely over SSH.

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
PARENT_DIR="$(dirname "$SCRIPT_DIR")"

if [[ -f "${PARENT_DIR}/common.sh" ]]; then
    source "${PARENT_DIR}/common.sh"
elif [[ -f "$(pwd)/common.sh" ]]; then
    source "$(pwd)/common.sh"
else
    echo "[ERROR] common.sh not found"
    exit 1
fi

PHASE="Phase 3: Controller Failure & Recovery"
NODE_ID="test-node-3"
USER_ID="3"
RUN_SUFFIX="$(date +%s)-$$"
BOOTSTRAP_USERNAME="phase3-bootstrap-$RUN_SUFFIX"
READER_USERNAME="phase3-reader-$RUN_SUFFIX"
BOOTSTRAP_PASSWORD="phase3-bootstrap-password"
BOOTSTRAP_PASSWORD_HASH='$2b$10$znvck.bQC.mhFuIOZPO3vuubEa0QWHsd9DDrm9VqPDYAfUwFuFYXC'
READER_PASSWORD="phase3-reader-password"
API_BASE_URL="https://${CONTROLLER_HOST:-localhost}:28443/api"
HTTP_STATUS=""
HTTP_BODY=""

cleanup() {
    compose exec -T etcd etcdctl --endpoints=localhost:2379 del "configs/$NODE_ID/hsi/$USER_ID" >/dev/null 2>&1 || true
    compose exec -T etcd etcdctl --endpoints=localhost:2379 del "users/$BOOTSTRAP_USERNAME" >/dev/null 2>&1 || true
    compose exec -T etcd etcdctl --endpoints=localhost:2379 del "users/$READER_USERNAME" >/dev/null 2>&1 || true
    sleep 2
    db_query "DELETE FROM hsi_config_current WHERE node_uuid='$NODE_ID';" >/dev/null || true
    db_query "DELETE FROM hsi_config_history WHERE node_uuid='$NODE_ID';" >/dev/null || true
    db_query "DELETE FROM pppoe_status WHERE node_uuid='$NODE_ID';" >/dev/null || true
    db_query "DELETE FROM node_events WHERE node_uuid='$NODE_ID';" >/dev/null || true
}
trap cleanup EXIT INT TERM

history_has_config() {
    local resource_version=$1
    local desire_status=$2
    local count
    count=$(db_query "SELECT COUNT(*) FROM hsi_config_history WHERE node_uuid='$NODE_ID' AND user_id='$USER_ID' AND resource_version='$resource_version' AND desire_status='$desire_status' AND config->'config'->>'desire_status'='$desire_status';" 2>/dev/null | xargs)
    [[ "$count" =~ ^[0-9]+$ ]] && [ "$count" -gt 0 ]
}

request_json() {
    local method=$1
    local url=$2
    local body=${3:-}
    local token=${4:-}
    local request_url=$url
    local response
    local curl_args=(-sS -k -w $'\n%{http_code}' -X "$method" -H "Content-Type: application/json")

    if [ -n "$token" ]; then
        curl_args+=(-H "Authorization: $token")
    fi
    if [ -n "$body" ]; then
        curl_args+=(--data "$body")
    fi

    if command -v curl >/dev/null 2>&1; then
        if ! response=$(curl "${curl_args[@]}" "$request_url"); then
            return 1
        fi
    else
        request_url="${url/$API_BASE_URL/https://localhost:8443/api}"
        if ! response=$(compose exec -T controller curl "${curl_args[@]}" "$request_url"); then
            return 1
        fi
    fi

    HTTP_STATUS=${response##*$'\n'}
    HTTP_BODY=${response%$'\n'*}
}

test_controller_failure() {
    log_info "========== $PHASE =========="

    # Step 1: Verify initial state
    log_info "Step 1: Verify initial state"
    wait_for_service "controller" || return 1
    wait_for_service "etcd" || return 1
    wait_for_service "postgres" || return 1
    cleanup
    log_success "All services healthy"

    # Step 2: Write config to etcd
    log_info "Step 2: Write config to etcd"
    local test_config='{"config":{"user_id":"3","desire_status":"connect"},"metadata":{"resourceVersion":"3","updatedBy":"e2e-test","updatedAt":"2026-06-05T00:00:00Z"}}'
    compose exec -T etcd etcdctl --endpoints=localhost:2379 put "configs/$NODE_ID/hsi/$USER_ID" "$test_config" > /dev/null
    if ! wait_for "history_has_config '3' 'connect'" 30 1; then
        log_error "Projection did not record the initial config"
        return 1
    fi
    log_success "Initial config was written to etcd and projected to history"

    # Step 3: Record initial state
    log_info "Step 3: Recording initial state"
    local initial_etcd
    initial_etcd=$(config_get "$NODE_ID" "$USER_ID")
    if ! printf '%s' "$initial_etcd" | jq -e \
        '.config.desire_status == "connect" and .metadata.resourceVersion == "3"' >/dev/null; then
        log_error "Initial etcd config does not contain the expected values"
        return 1
    fi
    log_success "Initial etcd and projection state are ready"

    # Step 4: Stop controller
    log_info "Step 4: Stopping controller..."
    stop_service "controller"
    log_info "Controller is down as expected"

    # Step 5: Verify other services still work
    log_info "Step 5: Verifying etcd and database still operational"
    local etcd_still_up=$(etcd_get "configs/$NODE_ID/hsi/$USER_ID")
    local db_still_up=$(db_query "SELECT 1;" 2>/dev/null)
    if [ -n "$etcd_still_up" ] && [ -n "$db_still_up" ]; then
        log_success "etcd and database still operational"
    else
        log_error "etcd or database went down when controller stopped"
        return 1
    fi

    # Step 6: Modify config while controller is down
    log_info "Step 6: Modifying config while controller is down"
    local updated_config='{"config":{"user_id":"3","desire_status":"disconnect"},"metadata":{"resourceVersion":"4","updatedBy":"e2e-test-downtime","updatedAt":"2026-06-05T00:01:00Z"}}'
    compose exec -T etcd etcdctl --endpoints=localhost:2379 put "configs/$NODE_ID/hsi/$USER_ID" "$updated_config" > /dev/null
    log_success "Config modified in etcd during controller downtime"

    # Step 7: Restart controller
    log_info "Step 7: Restarting controller..."
    start_service "controller"
    wait_for_service "controller" || return 1

    # Step 8: Verify the recovered controller serves the downtime config
    log_info "Step 8: Verifying the recovered REST API serves the downtime config"
    compose exec -T etcd etcdctl --endpoints=localhost:2379 put \
        "users/$BOOTSTRAP_USERNAME" "$BOOTSTRAP_PASSWORD_HASH" >/dev/null

    local bootstrap_login
    bootstrap_login=$(printf '{"username":"%s","password":"%s"}' \
        "$BOOTSTRAP_USERNAME" "$BOOTSTRAP_PASSWORD")
    if ! request_json "POST" "$API_BASE_URL/login" "$bootstrap_login"; then
        log_error "Bootstrap login request could not reach the recovered controller"
        return 1
    fi
    if [ "$HTTP_STATUS" != "200" ]; then
        log_error "Bootstrap login returned HTTP $HTTP_STATUS: $HTTP_BODY"
        return 1
    fi

    local bootstrap_token
    if ! bootstrap_token=$(printf '%s' "$HTTP_BODY" | jq -er '.token | select(length > 0)'); then
        log_error "Bootstrap login response did not contain a JWT"
        return 1
    fi

    local reader_registration
    reader_registration=$(printf '{"username":"%s","password":"%s"}' \
        "$READER_USERNAME" "$READER_PASSWORD")
    if ! request_json "POST" "$API_BASE_URL/users" "$reader_registration" "$bootstrap_token"; then
        log_error "Reader creation request could not reach the recovered controller"
        return 1
    fi
    if [ "$HTTP_STATUS" != "200" ]; then
        log_error "Reader creation returned HTTP $HTTP_STATUS: $HTTP_BODY"
        return 1
    fi

    local reader_login
    reader_login=$(printf '{"username":"%s","password":"%s"}' \
        "$READER_USERNAME" "$READER_PASSWORD")
    if ! request_json "POST" "$API_BASE_URL/login" "$reader_login"; then
        log_error "Reader login request could not reach the recovered controller"
        return 1
    fi
    if [ "$HTTP_STATUS" != "200" ]; then
        log_error "Reader login returned HTTP $HTTP_STATUS: $HTTP_BODY"
        return 1
    fi

    local reader_token
    if ! reader_token=$(printf '%s' "$HTTP_BODY" | jq -er '.token | select(length > 0)'); then
        log_error "Reader login response did not contain a JWT"
        return 1
    fi

    if ! request_json "GET" \
        "$API_BASE_URL/config/$NODE_ID/hsi/$USER_ID" "" "$reader_token"; then
        log_error "Config request could not reach the recovered controller"
        return 1
    fi
    if [ "$HTTP_STATUS" != "200" ]; then
        log_error "Config request returned HTTP $HTTP_STATUS: $HTTP_BODY"
        return 1
    fi
    if ! printf '%s' "$HTTP_BODY" | jq -e \
        '.config.desire_status == "disconnect" and .metadata.resourceVersion == "4"' >/dev/null; then
        log_error "Recovered REST response does not contain the downtime config"
        return 1
    fi
    log_success "Recovered REST API returned resource version 4 in disconnect state"

    # Step 9: Verify the projection replayed the downtime config
    log_info "Step 9: Verifying the projection replayed the downtime config"
    local expected_resource_version="4"
    if ! wait_for "history_has_config '$expected_resource_version' 'disconnect'" 45 1; then
        log_error "Projection did not record downtime resource version $expected_resource_version"
        return 1
    fi
    log_success "Projection recorded downtime resource version $expected_resource_version"

    log_success "$PHASE completed successfully!"
    return 0
}

test_controller_failure
