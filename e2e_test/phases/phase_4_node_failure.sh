#!/bin/bash

# Phase 4: Node registration, PPPoE connectivity and recovery
# Starts the real FastRG node against the e2e docker-compose controller, waits
# for it to register and bring its PPPoE sessions up, verifies the controller
# sees it, then stops the node and restores its original config.
# Works both locally (with docker-compose) and remotely (via SSH).

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

PHASE="Phase 4: Node Failure & Recovery"

# Resolve the node UUID from the node host (reused from /etc/fastrg/node_uuid).
NODE_UUID=""

# Seed the node's HSI/DNS/user-count config into the (freshly wiped) e2e etcd so
# the real node can dial PPPoE and reach the "Data phase". The stack starts from
# clean volumes each run, so without this seed user 2 stays "not configured".
# Values mirror a known-good working config for the test environment's BNG
# (account "the" / password "admin" / vlan 3).
seed_node_config() {
    local uuid=$1
    local hsi_config dns_records user_count
    hsi_config='{"config":{"user_id":"2","vlan_id":"3","password":"admin","dhcp_subnet":"255.255.255.0","account_name":"the","dhcp_gateway":"192.168.4.1","port-mapping":[{"dip":"192.168.4.2","dport":"8080","eport":"12345","index":"0"}],"desire_status":"connect","dhcp_addr_pool":"192.168.4.2-192.168.4.10","dns_proxy_enable":true,"tcp_conntrack_enable":true},"metadata":{"node":"'"$uuid"'","updatedAt":"2026-06-10T05:07:07Z","updatedBy":"admin","resourceVersion":"239"}}'
    dns_records='[{"domain":"www.fastrg.org","ip":"192.168.201.11","ttl":30}]'
    user_count='{"metadata":{"node":"'"$uuid"'","resourceVersion":"199","updatedAt":"2026-06-10T05:07:19Z","updatedBy":"admin"},"subscriber_count":"2"}'

    compose exec -T etcd etcdctl --endpoints=localhost:2379 put "configs/$uuid/hsi/2" "$hsi_config" >/dev/null || return 1
    compose exec -T etcd etcdctl --endpoints=localhost:2379 put "configs/$uuid/dns/2" "$dns_records" >/dev/null || return 1
    compose exec -T etcd etcdctl --endpoints=localhost:2379 put "user_counts/$uuid/" "$user_count" >/dev/null || return 1
}

# cleanup runs on every exit path (normal, error, or interrupt): stop the node
# process and restore its config so the node returns to its original
# (production) endpoints regardless of how the test ends. Guarded so it only
# acts after the config has actually been modified.
CLEANUP_ARMED=0
cleanup() {
    [ "$CLEANUP_ARMED" = "1" ] || return 0
    CLEANUP_ARMED=0
    log_info "Cleanup: stopping node and restoring config"
    node_stop || true
    node_restore_config || true
}
trap cleanup EXIT INT TERM

test_node_failure() {
    log_info "========== $PHASE =========="

    # Step 1: Verify controller stack is healthy
    log_info "Step 1: Verify initial state"
    wait_for_service "controller" || return 1
    wait_for_service "etcd" || return 1
    wait_for_service "postgres" || return 1
    log_success "Controller stack is healthy"

    # Step 2: Read node UUID and ensure a clean slate on the node
    log_info "Step 2: Preparing node ($NODE_HOST)"
    NODE_UUID=$(ssh_node "cat /etc/fastrg/node_uuid 2>/dev/null" | tr -d '[:space:]')
    if [ -z "$NODE_UUID" ]; then
        log_error "Could not read /etc/fastrg/node_uuid on $NODE_HOST"
        return 1
    fi
    log_info "Node UUID: $NODE_UUID"

    # Stop any pre-existing fastrg process (it may be pointed at another env).
    node_stop
    log_success "Node prepared (no stale fastrg process)"

    # Step 3: Seed the node's HSI/DNS/user-count config into etcd so PPPoE can
    # actually connect (the stack starts from clean volumes each run).
    log_info "Step 3: Seeding node HSI/DNS/user-count config into etcd"
    if ! seed_node_config "$NODE_UUID"; then
        log_error "Failed to seed node config into etcd"
        return 1
    fi
    log_success "Node config seeded (hsi/2, dns/2, user_counts)"

    # Step 4: Point node config at the e2e controller and start the node.
    # Arm cleanup first so the EXIT trap restores config even if a step below
    # fails after the config has been rewritten.
    log_info "Step 4: Pointing node config at e2e controller and starting node"
    CLEANUP_ARMED=1
    node_point_config_to_e2e || { log_error "Failed to rewrite node config"; return 1; }

    if ! node_start | grep -q "fastrg started"; then
        log_error "fastrg process did not start"
        ssh_node "tail -20 '$NODE_LOG' 2>/dev/null"
        return 1
    fi
    log_success "fastrg node process started"

    # Step 5: Wait for the node to register with the controller (nodes/<uuid> in etcd)
    log_info "Step 5: Waiting for node registration in etcd"
    local registered=""
    for _attempt in $(seq 1 30); do
        registered=$(etcd_get "nodes/$NODE_UUID")
        if [ -n "$registered" ]; then
            break
        fi
        sleep 2
    done
    if [ -z "$registered" ]; then
        log_error "Node did not register with controller within timeout"
        ssh_node "tail -30 '$NODE_LOG' 2>/dev/null"
        return 1
    fi
    log_success "Node registered with controller (nodes/$NODE_UUID)"

    # Step 6: Wait for PPPoE sessions to come up and stabilise. The controller
    # polls each node session over gRPC and records the raw PPPoE status string
    # into pppoe_status; "Data phase" is the established, data-passing state.
    log_info "Step 6: Waiting for PPPoE connection to stabilise (phase='Data phase')"
    local connected_count=0
    for _attempt in $(seq 1 60); do
        connected_count=$(pppoe_connected_count "$NODE_UUID" "Data phase")
        connected_count=${connected_count:-0}
        if [ "$connected_count" -gt 0 ]; then
            break
        fi
        sleep 2
    done
    if [ "$connected_count" -le 0 ]; then
        log_error "No PPPoE session reached 'Data phase' within timeout"
        ssh_node "tail -30 '$NODE_LOG' 2>/dev/null"
        return 1
    fi
    log_success "PPPoE connection stabilised ($connected_count session(s) in Data phase)"

    # Step 7: Confirm the connected state is stable across a short observation
    # window (sessions stay in Data phase, no drop back to an earlier phase).
    log_info "Step 7: Confirming PPPoE state stability"
    sleep 5
    local stable_count=$(pppoe_connected_count "$NODE_UUID" "Data phase")
    stable_count=${stable_count:-0}
    if [ "$stable_count" -lt "$connected_count" ]; then
        log_error "PPPoE sessions dropped after stabilising (was $connected_count, now $stable_count)"
        return 1
    fi
    log_success "PPPoE connection is stable ($stable_count session(s) in Data phase)"

    # Step 8: Verify the node registration is still live in etcd after the
    # session has been up for a while (heartbeats keep nodes/<uuid> fresh).
    log_info "Step 8: Verifying node registration is still live"
    if [ -n "$(etcd_get "nodes/$NODE_UUID")" ]; then
        log_success "Node registration is live in etcd (nodes/$NODE_UUID)"
    else
        log_error "Node registration disappeared from etcd while node was up"
        return 1
    fi

    # Step 9: Node shutdown and de-registration. Stopping the node lets the
    # controller's stale-node monitor evict it after the heartbeat timeout.
    log_info "Step 9: Stopping node to verify clean shutdown"
    node_stop
    if node_is_running; then
        log_error "fastrg process is still running after stop"
        return 1
    fi
    log_success "Node process stopped cleanly"

    log_success "$PHASE completed successfully!"
    return 0
}

# Run test (cleanup trap inside restores node config and stops the process)
test_node_failure
result=$?
exit $result
