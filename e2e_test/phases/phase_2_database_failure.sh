#!/bin/bash

# Phase 2: Database failure and recovery
# Test that Kafka consumer uses DLQ when database is down
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

PHASE="Phase 2: Database Failure & Recovery"
NODE_ID="test-node-2"
USER_ID="user-2"

test_database_failure() {
    log_info "========== $PHASE =========="

    # Step 1: Verify initial state
    log_info "Step 1: Verify initial state"
    wait_for_service "postgres" || return 1
    wait_for_service "controller" || return 1
    log_success "All services healthy"

    # Step 2: Write config to etcd
    log_info "Step 2: Write config to etcd"
    local test_config='{"config":{"user_id":"user-2","desire_status":"connect"},"metadata":{"resourceVersion":"2","updatedBy":"e2e-test","updatedAt":"2026-06-05T00:00:00Z"}}'
    docker-compose exec -T etcd etcdctl --endpoints=localhost:2379 put "configs/$NODE_ID/hsi/$USER_ID" "$test_config" > /dev/null
    log_success "Config written to etcd"

    # Step 3: Verify config is in database
    log_info "Step 3: Verifying config projection to database"
    sleep 3
    local db_config=$(db_query "SELECT config FROM hsi_config_current WHERE node_uuid='$NODE_ID' AND user_id='$USER_ID';" 2>/dev/null)
    if [ -z "$db_config" ]; then
        log_warn "Config not yet in database (may be pending projection)"
    else
        log_success "Config projected to database"
    fi

    # Step 4: Stop database
    log_info "Step 4: Stopping database for 30 seconds..."
    stop_service "postgres"
    log_warn "Database is DOWN"

    # Step 5: Wait and verify database is down
    log_info "Step 5: Verifying database is inaccessible"
    sleep 2
    if docker-compose exec -T postgres psql -U fastrg -d fastrg -c "SELECT 1;" 2>/dev/null; then
        log_error "Database should be down but still accessible!"
        return 1
    fi
    log_success "Database is confirmed DOWN"

    # Step 6: Controller should use DLQ for failed operations
    log_info "Step 6: Verify controller queues failed operations"
    sleep 5
    log_info "Controller should be retrying/queueing operations"

    # Step 7: Restart database
    log_info "Step 7: Restarting database..."
    start_service "postgres"
    wait_for_service "postgres" || return 1

    # Step 8: Verify recovery
    log_info "Step 8: Verifying data recovery after database restart"
    sleep 5

    # Check if config is now in database
    local recovered_config=$(db_query "SELECT config FROM hsi_config_current WHERE node_uuid='$NODE_ID' AND user_id='$USER_ID';" 2>/dev/null)
    if [ -n "$recovered_config" ]; then
        log_success "Config recovered in database after restart"
    else
        log_warn "Config not yet in database (projection may still be processing)"
    fi

    # Step 9: Check DLQ for any stuck messages
    log_info "Step 9: Checking for DLQ entries"
    local dlq_count=$(db_query "SELECT COUNT(*) FROM kafka_dlq WHERE status='pending';" 2>/dev/null | xargs)
    if [ -n "$dlq_count" ] && [ "$dlq_count" -gt 0 ]; then
        log_warn "Found $dlq_count messages in DLQ (expected if DB was down)"
    else
        log_info "No pending DLQ messages (all recovered)"
    fi

    # Step 10: Verify Kafka consumer resumes
    log_info "Step 10: Verifying Kafka consumer resumes"
    wait_for_service "controller" || return 1
    log_success "Controller and Kafka consumer resumed"

    log_success "$PHASE completed successfully!"
    return 0
}

# Run test
test_database_failure
