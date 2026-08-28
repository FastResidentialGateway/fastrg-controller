#!/bin/bash

# Phase 7: config apply confirmation after a lost result
#
# A node reports "I applied this config" once, over Kafka. Nothing re-sends it,
# so a result that never reaches the consumer would leave hsi_config_current
# stuck on an older version forever — the controller would keep believing the
# node runs a config it stopped running.
#
# This phase creates exactly that loss and then checks the controller repairs it:
# with Kafka stopped a config is pushed, the node applies it and cannot publish
# the result, and restarting the node drops the queued result for good. Once
# Kafka is back the controller has to notice the gap on its own and ask the node
# to restate what it is running.
#
# Two triggers can carry that request — the node re-registering, and the periodic
# sweep over configs that stayed unconfirmed. The phase does not try to tell them
# apart; what it asserts is the behaviour they exist for: the row was still stale
# while Kafka was down, it converges to the pushed revision afterwards, and no
# audit row is invented for a config the node never actually changed.
#
# Every assertion is a read-only query: the phase never deletes or truncates.

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

PHASE="Phase 7: Config Apply Confirmation"

# Resolved from the node host (/etc/fastrg/node_uuid) in step 2.
NODE_UUID=""
USER_ID="2"

# Write the subscriber's HSI config into etcd at the given resourceVersion. The
# DNS records and subscriber count come along because the node needs a complete
# fixture to bring the subscriber up at all. Values mirror phase 6's known-good
# config for this test environment's BNG.
seed_node_config() {
    local uuid=$1
    local resource_version=$2
    local dhcp_gateway=$3
    local hsi_config dns_records user_count
    hsi_config='{"config":{"user_id":"2","vlan_id":"3","password":"admin","dhcp_subnet":"255.255.255.0","account_name":"the","dhcp_gateway":"'"$dhcp_gateway"'","desire_status":"connect","dhcp_addr_pool":"192.168.4.2-192.168.4.10","dns_proxy_enable":true,"tcp_conntrack_enable":true},"metadata":{"node":"'"$uuid"'","updatedAt":"2026-06-10T05:07:07Z","updatedBy":"admin","resourceVersion":"'"$resource_version"'"}}'
    dns_records='{"records":[{"domain":"www.fastrg.org","ip":"192.168.201.11","ttl":30}],"metadata":{"node":"'"$uuid"'","resourceVersion":"1","updatedAt":"2026-06-10T05:07:19Z","updatedBy":"admin"}}'
    user_count='{"metadata":{"node":"'"$uuid"'","resourceVersion":"199","updatedAt":"2026-06-10T05:07:19Z","updatedBy":"admin"},"subscriber_count":"2"}'

    compose exec -T etcd etcdctl --endpoints=localhost:2379 put "configs/$uuid/hsi/$USER_ID" "$hsi_config" >/dev/null || return 1
    compose exec -T etcd etcdctl --endpoints=localhost:2379 put "configs/$uuid/dns/$USER_ID" "$dns_records" >/dev/null || return 1
    compose exec -T etcd etcdctl --endpoints=localhost:2379 put "user_counts/$uuid/" "$user_count" >/dev/null || return 1
}

# etcd's ModRevision for the subscriber's HSI config key: the exact identity of
# the version the controller pushed.
etcd_config_revision() {
    compose exec -T etcd etcdctl --endpoints=localhost:2379 \
        get "configs/$NODE_UUID/hsi/$USER_ID" --write-out=fields 2>/dev/null \
        | awk '$1 == "\"ModRevision\"" { print $3 }' | tr -d '[:space:]'
}

# The ModRevision the node has confirmed applying, as recorded in
# hsi_config_current. Empty when the node has confirmed nothing at all.
confirmed_config_revision() {
    db_query "SELECT mod_revision FROM hsi_config_current WHERE node_uuid='$NODE_UUID' AND user_id='$USER_ID';" \
        | tr -d '[:space:]'
}

# True once the broker answers a metadata request. The kafka service defines no
# container healthcheck, and "the container is up" is anyway too early — the
# broker needs a few more seconds before it can serve.
kafka_ready() {
    compose exec -T kafka /opt/kafka/bin/kafka-topics.sh \
        --bootstrap-server localhost:9092 --list >/dev/null 2>&1
}

# Audit rows recorded for this subscriber. A restated config must not add any.
node_event_count() {
    db_query "SELECT COUNT(*) FROM node_events WHERE node_uuid='$NODE_UUID' AND user_id='$USER_ID';" | xargs
}

# True once the node has confirmed the revision etcd currently holds.
config_confirmed() {
    local pushed confirmed
    pushed=$(etcd_config_revision)
    confirmed=$(confirmed_config_revision)
    [ -n "$pushed" ] && [ -n "$confirmed" ] && [ "$confirmed" -ge "$pushed" ]
}

# Print what a failed confirmation assertion needs to be explained.
dump_confirmation_evidence() {
    log_error "etcd ModRevision: $(etcd_config_revision)  confirmed: $(confirmed_config_revision)"
    log_error "Controller confirmation log lines (last 20):"
    compose logs --no-color controller 2>/dev/null \
        | grep -iE "confirmation sweep|config status|RepublishConfigStatus" | tail -20
    log_error "hsi_config_current row:"
    db_query "SELECT node_uuid, user_id, mod_revision, updated_by FROM hsi_config_current WHERE node_uuid='$NODE_UUID';"
    log_error "Node process log (last 20 lines):"
    ssh_node "tail -20 '$NODE_LOG' 2>/dev/null"
}

# cleanup runs on every exit path: stop the node this phase started, put its
# config back, and make sure Kafka is running again for whatever comes next.
# Guarded so it only acts once the phase has actually taken ownership.
CLEANUP_ARMED=0
cleanup() {
    [ "$CLEANUP_ARMED" = "1" ] || return 0
    CLEANUP_ARMED=0
    log_info "Cleanup: stopping node, restoring config, making sure Kafka is up"
    node_stop || true
    node_restore_config || true
    start_service "kafka" || true
}
trap cleanup EXIT INT TERM

test_config_confirmation() {
    log_info "========== $PHASE =========="

    # Step 1: Verify controller stack is healthy
    log_info "Step 1: Verify initial state"
    wait_for_service "controller" || return 1
    wait_for_service "etcd" || return 1
    wait_for_service "postgres" || return 1
    log_success "Controller stack is healthy"

    # Step 2: Read node UUID and make sure no other test owns the node
    log_info "Step 2: Preparing node ($NODE_HOST)"
    NODE_UUID=$(ssh_node "cat /etc/fastrg/node_uuid 2>/dev/null" | tr -d '[:space:]')
    if [ -z "$NODE_UUID" ]; then
        log_error "Could not read /etc/fastrg/node_uuid on $NODE_HOST"
        return 1
    fi
    log_info "Node UUID: $NODE_UUID"

    if node_is_running; then
        log_warn "A fastrg process is already running on $NODE_HOST — likely another e2e in progress."
        log_warn "Refusing to touch it; SKIPPING $PHASE."
        return 2
    fi
    log_success "Node prepared (no fastrg process running)"

    # Step 3: Seed the subscriber config, then start the node against it. The
    # node loads this config straight from etcd at boot, a path that reports
    # nothing, so the first confirmation already has to come from the controller
    # asking for it.
    log_info "Step 3: Seeding config and starting the node"
    if ! seed_node_config "$NODE_UUID" "300" "192.168.4.1"; then
        log_error "Failed to seed node config into etcd"
        return 1
    fi
    CLEANUP_ARMED=1
    node_point_config_to_e2e || { log_error "Failed to rewrite node config"; return 1; }
    if ! node_start | grep -q "fastrg started"; then
        log_error "fastrg process did not start"
        ssh_node "tail -20 '$NODE_LOG' 2>/dev/null"
        return 1
    fi
    log_success "fastrg node process started"

    # Step 4: Wait for registration, so the controller has a node to ask.
    log_info "Step 4: Waiting for node registration in etcd"
    if ! wait_for "[ -n \"\$(etcd_get nodes/$NODE_UUID)\" ]" 60 2; then
        log_error "Node did not register with controller within timeout"
        ssh_node "tail -30 '$NODE_LOG' 2>/dev/null"
        return 1
    fi
    log_success "Node registered with controller (nodes/$NODE_UUID)"

    # Step 5: The controller has to notice this config was never confirmed and
    # ask for it. Until that happens hsi_config_current has no row at all.
    log_info "Step 5: Waiting for the first confirmation of the seeded config"
    if ! wait_for "config_confirmed" 240 5; then
        log_error "hsi_config_current never caught up with the seeded config"
        dump_confirmation_evidence
        return 1
    fi
    local baseline_revision baseline_events
    baseline_revision=$(confirmed_config_revision)
    baseline_events=$(node_event_count)
    log_success "Config confirmed at ModRevision $baseline_revision ($baseline_events audit row(s) so far)"

    # Step 6: Stop Kafka. From here the node can apply configs but cannot tell
    # anyone it did.
    log_info "Step 6: Stopping Kafka so apply results cannot be delivered"
    stop_service "kafka"
    log_success "Kafka stopped"

    # Step 7: Push a changed config. The node picks it up from etcd and applies
    # it; the result it produces has nowhere to go.
    log_info "Step 7: Pushing a changed config while Kafka is down"
    if ! seed_node_config "$NODE_UUID" "301" "192.168.4.254"; then
        log_error "Failed to push the changed config into etcd"
        return 1
    fi
    local pushed_revision
    pushed_revision=$(etcd_config_revision)
    if [ -z "$pushed_revision" ] || [ "$pushed_revision" -le "$baseline_revision" ]; then
        log_error "Pushed config revision ($pushed_revision) did not advance past the baseline ($baseline_revision)"
        dump_confirmation_evidence
        return 1
    fi
    log_success "Pushed config at ModRevision $pushed_revision"
    sleep 15

    # Step 8: Restart the node while Kafka is still down. The apply result was
    # only ever held in memory, so this is what makes the loss permanent.
    log_info "Step 8: Restarting the node so the undelivered result is lost"
    node_stop
    if node_is_running; then
        log_error "fastrg process is still running after stop"
        return 1
    fi
    if ! node_start | grep -q "fastrg started"; then
        log_error "fastrg process did not restart"
        ssh_node "tail -20 '$NODE_LOG' 2>/dev/null"
        return 1
    fi
    log_success "fastrg node process restarted; the apply result is gone"

    # Step 9: Prove the loss. The node is now running the new config while the
    # controller still records the old revision — if this already matched, the
    # rest of the phase would prove nothing.
    log_info "Step 9: Confirming the controller is still recording the old revision"
    local stale_revision
    stale_revision=$(confirmed_config_revision)
    if [ "${stale_revision:-0}" -ge "$pushed_revision" ]; then
        log_error "hsi_config_current already reads $stale_revision; the apply result was not lost, so this phase cannot test the repair"
        dump_confirmation_evidence
        return 1
    fi
    log_success "hsi_config_current still reads $stale_revision, behind the pushed $pushed_revision"

    # Step 10: Bring Kafka back. Nothing re-sends the lost result on its own; the
    # controller has to ask for it.
    log_info "Step 10: Restarting Kafka"
    start_service "kafka"
    if ! wait_for "kafka_ready" 180 5; then
        log_error "Kafka did not start serving again"
        compose logs --no-color --tail=20 kafka 2>/dev/null
        return 1
    fi
    log_success "Kafka is back"

    # Step 11: The controller notices the gap and asks the node to restate what
    # it runs, so the row catches up with the pushed revision. The window covers
    # the unconfirmed timeout plus the sweep interval, plus the trip through
    # Kafka and the consumer.
    log_info "Step 11: Waiting for hsi_config_current to converge"
    if ! wait_for "config_confirmed" 300 5; then
        log_error "hsi_config_current never converged to the pushed revision after Kafka came back"
        dump_confirmation_evidence
        return 1
    fi
    log_success "hsi_config_current converged to ModRevision $(confirmed_config_revision)"

    # Step 12: A restated config is not a change, so it must not have produced an
    # audit row. The node never reported a transition here — the only apply it
    # made was the one whose result was lost.
    log_info "Step 12: Verifying the restated config wrote no audit row"
    local final_events
    final_events=$(node_event_count)
    if [ "${final_events:-0}" -ne "${baseline_events:-0}" ]; then
        log_error "node_events grew from $baseline_events to $final_events; a restated config must not be audited"
        db_query "SELECT event_type, action, success, error_code, event_time FROM node_events WHERE node_uuid='$NODE_UUID' AND user_id='$USER_ID' ORDER BY event_time;"
        dump_confirmation_evidence
        return 1
    fi
    log_success "node_events unchanged at $final_events row(s)"

    log_success "$PHASE completed successfully!"
    return 0
}

# Run test (cleanup trap inside restores node config, stops the process and Kafka)
test_config_confirmation
result=$?
exit $result
