#!/bin/bash

# Phase 1: etcd failure and recovery
# Test that controller can recover when etcd is down for extended period
# Works both locally (with docker-compose) and remotely (via SSH)

# Determine if running locally or remotely
SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
PARENT_DIR="$(dirname "$SCRIPT_DIR")"

# Source common helpers
if [[ -f "${PARENT_DIR}/common.sh" ]]; then
    source "${PARENT_DIR}/common.sh"
elif [[ -f "$(pwd)/common.sh" ]]; then
    source "$(pwd)/common.sh"
else
    echo "[ERROR] common.sh not found"
    exit 1
fi

PHASE="Phase 1: etcd Failure & Recovery"
NODE_ID="test-node-1"
USER_ID="user-1"

test_etcd_failure() {
    log_info "========== $PHASE =========="

    # Step 1: Verify initial state
    log_info "Step 1: Verify initial state"
    wait_for_service "etcd" || return 1
    wait_for_service "controller" || return 1
    log_success "All services healthy"

    # Step 2: Write test config to etcd
    log_info "Step 2: Write test config to etcd"
    local test_config='{"config":{"user_id":"user-1","desire_status":"connect"},"metadata":{"resourceVersion":"1","updatedBy":"e2e-test","updatedAt":"2026-06-05T00:00:00Z"}}'
    docker-compose exec -T etcd etcdctl --endpoints=localhost:2379 put "configs/$NODE_ID/hsi/$USER_ID" "$test_config" > /dev/null
    sleep 2

    # Verify config is readable
    local etcd_config=$(config_get "$NODE_ID" "$USER_ID")
    if [ -z "$etcd_config" ]; then
        log_error "Failed to write config to etcd"
        return 1
    fi
    log_success "Config written to etcd"

    # Step 3: Stop etcd
    log_info "Step 3: Stopping etcd for 30 seconds..."
    stop_service "etcd"
    log_warn "etcd is DOWN"

    # Step 4: Try to access etcd (should fail)
    log_info "Step 4: Verifying etcd is inaccessible"
    if docker-compose exec -T etcd etcdctl --endpoints=localhost:2379 get "configs/$NODE_ID/hsi/$USER_ID" 2>/dev/null; then
        log_error "etcd should be down but still accessible!"
        return 1
    fi
    log_success "etcd is confirmed DOWN"

    # Step 5: Controller should still serve (using cached data)
    log_info "Step 5: Verifying controller is still operational"
    sleep 5
    if ! is_service_up "controller"; then
        log_error "Controller went down when etcd is unavailable"
        return 1
    fi
    log_success "Controller still operational (serving cached data)"

    # Step 6: Restart etcd
    log_info "Step 6: Restarting etcd..."
    start_service "etcd"
    wait_for_service "etcd" || return 1

    # Step 7: Verify config recovery
    log_info "Step 7: Verifying config is accessible after etcd recovery"
    local recovered_config=$(config_get "$NODE_ID" "$USER_ID")
    if [ -z "$recovered_config" ]; then
        log_error "Config was lost after etcd recovery!"
        return 1
    fi

    if [ "$etcd_config" == "$recovered_config" ]; then
        log_success "Config recovered successfully from etcd"
    else
        log_error "Config changed during outage"
        return 1
    fi

    # Step 8: Verify controller resumes normal operation
    log_info "Step 8: Verifying controller resumes normal operation"
    wait_for_service "controller" || return 1
    log_success "Controller resumed normal operation"

    log_success "$PHASE completed successfully!"
    return 0
}

# Run test
test_etcd_failure
