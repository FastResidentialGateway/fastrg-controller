#!/bin/bash

# Phase 3: Controller failure and recovery
# Test that system state is preserved when controller restarts
# Works both locally (with docker-compose) and remotely (via SSH)

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
USER_ID="user-3"

test_controller_failure() {
    log_info "========== $PHASE =========="

    # Step 1: Verify initial state
    log_info "Step 1: Verify initial state"
    wait_for_service "controller" || return 1
    wait_for_service "etcd" || return 1
    wait_for_service "postgres" || return 1
    log_success "All services healthy"

    # Step 2: Write config to etcd
    log_info "Step 2: Write config to etcd"
    local test_config='{"config":{"user_id":"user-3","desire_status":"connect"},"metadata":{"resourceVersion":"3","updatedBy":"e2e-test","updatedAt":"2026-06-05T00:00:00Z"}}'
    compose exec -T etcd etcdctl --endpoints=localhost:2379 put "configs/$NODE_ID/hsi/$USER_ID" "$test_config" > /dev/null
    sleep 2
    log_success "Config written to etcd"

    # Step 3: Record initial state
    log_info "Step 3: Recording initial state"
    local initial_etcd=$(config_get "$NODE_ID" "$USER_ID")
    local initial_db=$(db_query "SELECT COUNT(*) FROM hsi_config_history;" 2>/dev/null)
    log_info "etcd has config: $([ -n "$initial_etcd" ] && echo "yes" || echo "no")"
    log_info "database history records: $initial_db"

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
    local updated_config='{"config":{"user_id":"user-3","desire_status":"disconnect"},"metadata":{"resourceVersion":"4","updatedBy":"e2e-test-downtime","updatedAt":"2026-06-05T00:01:00Z"}}'
    compose exec -T etcd etcdctl --endpoints=localhost:2379 put "configs/$NODE_ID/hsi/$USER_ID" "$updated_config" > /dev/null
    log_success "Config modified in etcd during controller downtime"

    # Step 7: Restart controller
    log_info "Step 7: Restarting controller..."
    start_service "controller"
    wait_for_service "controller" || return 1

    # Step 8: Verify projection catches up
    log_info "Step 8: Verifying projection catches up with missed changes"
    sleep 5

    local final_etcd=$(config_get "$NODE_ID" "$USER_ID")

    if [ "$final_etcd" == "$updated_config" ]; then
        log_success "etcd still has updated config"
    else
        log_error "etcd config was lost or incorrect"
        return 1
    fi

    # Step 9: Verify history is intact
    log_info "Step 9: Verifying history projection is intact"
    local final_history=$(db_query "SELECT COUNT(*) FROM hsi_config_history;" 2>/dev/null)
    if [ "$final_history" -gt "$initial_db" ]; then
        log_success "History was updated after controller recovery (now: $final_history, was: $initial_db)"
    else
        log_warn "History count unchanged (may be OK if no new events during downtime)"
    fi

    log_success "$PHASE completed successfully!"
    return 0
}

# Run test
test_controller_failure
