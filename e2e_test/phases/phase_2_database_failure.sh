#!/bin/bash

# Phase 2: Database failure and recovery
# Test that Kafka messages are not committed while the database is down, then
# process successfully after the database recovers.
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
KAFKA_TOPIC="fastrg.node.events"
# Protobuf NodeEvent:
# node=e2e-db-retry-node, user=e2e-db-retry-user, type=PPPOE_CONNECTED, timestamp=1,
# payload.phase=CONNECTED, payload.hsi_ipv4=192.0.2.10.
RETRY_NODE_ID="e2e-db-retry-node"
RETRY_USER_ID="e2e-db-retry-user"
RETRY_EVENT_BASE64="ChFlMmUtZGItcmV0cnktbm9kZRIRZTJlLWRiLXJldHJ5LXVzZXIYCyABWg4IAhIKMTkyLjAuMi4xMA=="

test_database_failure() {
    log_info "========== $PHASE =========="

    # Step 1: Verify initial state
    log_info "Step 1: Verify initial state"
    wait_for_service "postgres" || return 1
    wait_for_service "controller" || return 1
    log_success "All services healthy"

    local before_history_count=$(config_history_count "$NODE_ID" "$USER_ID")
    before_history_count=${before_history_count:-0}
    local before_dlq_count=$(dlq_pending_count)
    before_dlq_count=${before_dlq_count:-0}
    db_query "DELETE FROM pppoe_status WHERE node_uuid='$RETRY_NODE_ID' AND user_id='$RETRY_USER_ID';" >/dev/null

    log_info "Ensuring Kafka topic exists"
    kafka_ensure_topic "$KAFKA_TOPIC" || return 1
    sleep 3

    # Step 2: Write config to etcd
    log_info "Step 2: Write config to etcd"
    local test_config='{"config":{"user_id":"user-2","desire_status":"connect"},"metadata":{"resourceVersion":"2","updatedBy":"e2e-test","updatedAt":"2026-06-05T00:00:00Z"}}'
    compose exec -T etcd etcdctl --endpoints=localhost:2379 put "configs/$NODE_ID/hsi/$USER_ID" "$test_config" > /dev/null
    log_success "Config written to etcd"

    # Step 3: Verify config change is recorded in history. hsi_config_current is
    # updated only after a node CONFIG_APPLY_OK event, so this phase validates the
    # etcd-to-history projection path instead.
    log_info "Step 3: Verifying config history projection"
    local history_count=""
    for _attempt in $(seq 1 15); do
        history_count=$(config_history_count "$NODE_ID" "$USER_ID")
        if [ -n "$history_count" ] && [ "$history_count" -gt "$before_history_count" ]; then
            break
        fi
        sleep 1
    done
    if [ -z "$history_count" ] || [ "$history_count" -le "$before_history_count" ]; then
        log_error "Config history was not projected to database"
        return 1
    fi
    log_success "Config history projected to database"

    # Step 4: Stop database
    log_info "Step 4: Stopping database for 30 seconds..."
    stop_service "postgres"
    log_info "Database is down as expected"

    # Step 5: Wait and verify database is down
    log_info "Step 5: Verifying database is inaccessible"
    sleep 2
    if compose_quiet exec -T postgres psql -U fastrg -d fastrg -c "SELECT 1;"; then
        log_error "Database should be down but still accessible!"
        return 1
    fi
    log_success "Database is confirmed DOWN"

    # Step 6: Produce a Kafka event while DB is unavailable. The consumer should
    # keep retrying the same message without committing the Kafka offset, then
    # process it after DB recovery.
    log_info "Step 6: Producing Kafka event while DB is unavailable"
    kafka_produce_base64 "$KAFKA_TOPIC" "$RETRY_EVENT_BASE64" || return 1
    log_success "Kafka event produced during DB outage"

    log_info "Step 7: Verifying controller remains healthy during DB outage"
    sleep 5
    if ! is_service_up "controller"; then
        log_error "Controller went down when database is unavailable"
        return 1
    fi
    log_success "Controller remains healthy during DB outage"

    # Step 8: Restart database
    log_info "Step 8: Restarting database..."
    start_service "postgres"
    wait_for_service "postgres" || return 1

    # Step 9: Verify recovery
    log_info "Step 9: Verifying data recovery after database restart"
    sleep 5

    # Check if the projected history row remains available after DB restart.
    local recovered_history=$(config_history_count "$NODE_ID" "$USER_ID")
    if [ -z "$recovered_history" ] || [ "$recovered_history" -lt "$history_count" ]; then
        log_error "Config history was not available after database restart"
        return 1
    fi
    log_success "Config history is available after database restart"

    # Step 10: Verify the DB-outage Kafka event was processed into pppoe_status.
    log_info "Step 10: Verifying Kafka event processing after DB recovery"
    local retry_phase=""
    for _attempt in $(seq 1 30); do
        retry_phase=$(pppoe_status "$RETRY_NODE_ID" "$RETRY_USER_ID" | xargs)
        if [ "$retry_phase" = "connected" ]; then
            break
        fi
        sleep 1
    done
    if [ "$retry_phase" != "connected" ]; then
        log_error "Kafka event produced during DB outage was not processed after recovery"
        return 1
    fi
    log_success "Kafka event processed after DB recovery"

    # Step 11: DB outages should not create same-DB DLQ rows.
    log_info "Step 11: Verifying DB outage did not create DLQ entry"
    local dlq_count=$(dlq_pending_count)
    dlq_count=${dlq_count:-0}
    if [ "$dlq_count" -gt "$before_dlq_count" ]; then
        log_error "DLQ increased during DB outage; expected Kafka retry instead"
        return 1
    fi
    log_success "No DLQ entry created for DB outage"

    log_success "$PHASE completed successfully!"
    return 0
}

# Run test
test_database_failure
