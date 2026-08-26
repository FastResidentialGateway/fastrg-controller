#!/bin/bash

# Phase 6: PPPoE status republish
# Brings a real fastrg node up against the e2e controller and then checks the two
# triggers that make a node re-send its PPPoE state: restarting the controller
# (the Kafka consumer asks every registered node once per session) and restarting
# the node (re-registration asks that one node). A republished event carries a
# newer event_time, and the upsert only overwrites a row when the incoming
# event_time is newer — so "every row's event_time moved forward" is the
# observable proof that the rows were actually rewritten.
# The last part is the recovery this exists for: with the database down the
# sessions are dropped, so the table is left claiming "connected" while reality
# says otherwise; restarting the controller has to bring the table back in line
# once the database returns.
# Every assertion is a read-only query: the phase never deletes or truncates.
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

PHASE="Phase 6: PPPoE Status Republish"

# Resolved from the node host (/etc/fastrg/node_uuid) in step 2.
NODE_UUID=""

# Snapshot of this node's pppoe_status rows taken before a republish trigger,
# one row per line as "<user_id> <phase> <event_time>". rows_republished()
# compares the live table against it.
BASELINE_ROWS=""

# Seed the node's HSI/DNS/user-count config into the (freshly wiped) e2e etcd so
# the real node can dial PPPoE and reach the connected phase. The stack starts
# from clean volumes each run, so without this seed user 2 stays "not
# configured". Values mirror a known-good working config for the test
# environment's BNG (account "the" / password "admin" / vlan 3).
seed_node_config() {
    local uuid=$1
    local hsi_config dns_records user_count
    hsi_config='{"config":{"user_id":"2","vlan_id":"3","password":"admin","dhcp_subnet":"255.255.255.0","account_name":"the","dhcp_gateway":"192.168.4.1","port-mapping":[{"dip":"192.168.4.2","dport":"8080","eport":"12345","index":"0"}],"desire_status":"connect","dhcp_addr_pool":"192.168.4.2-192.168.4.10","dns_proxy_enable":true,"tcp_conntrack_enable":true},"metadata":{"node":"'"$uuid"'","updatedAt":"2026-06-10T05:07:07Z","updatedBy":"admin","resourceVersion":"239"}}'
    dns_records='{"records":[{"domain":"www.fastrg.org","ip":"192.168.201.11","ttl":30}],"metadata":{"node":"'"$uuid"'","resourceVersion":"1","updatedAt":"2026-06-10T05:07:19Z","updatedBy":"admin"}}'
    user_count='{"metadata":{"node":"'"$uuid"'","resourceVersion":"199","updatedAt":"2026-06-10T05:07:19Z","updatedBy":"admin"},"subscriber_count":"2"}'

    compose exec -T etcd etcdctl --endpoints=localhost:2379 put "configs/$uuid/hsi/2" "$hsi_config" >/dev/null || return 1
    compose exec -T etcd etcdctl --endpoints=localhost:2379 put "configs/$uuid/dns/2" "$dns_records" >/dev/null || return 1
    compose exec -T etcd etcdctl --endpoints=localhost:2379 put "user_counts/$uuid/" "$user_count" >/dev/null || return 1
}

# This node's pppoe_status rows, one per line: "<user_id> <phase> <event_time>".
# event_time is printed as fixed-width UTC digits (YYYYMMDDHHMMSSuuuuuu) so it
# can be compared exactly as a string, without float rounding.
node_pppoe_rows() {
    db_query "SELECT user_id || ' ' || phase || ' ' || to_char(event_time AT TIME ZONE 'UTC', 'YYYYMMDDHH24MISSUS') FROM pppoe_status WHERE node_uuid='$NODE_UUID' ORDER BY user_id;" \
        | awk 'NF { $1=$1; print }'
}

# Number of controller log lines reporting a completed republish for this node.
# The count only grows, so comparing it across a trigger tells us a republish
# ran because of that trigger.
republish_log_count() {
    compose logs --no-color controller 2>/dev/null \
        | grep -c "Node $NODE_UUID republished " || true
}

# Event count reported by the most recent republish log line for this node.
republish_last_event_count() {
    compose logs --no-color controller 2>/dev/null \
        | sed -n "s/.*Node $NODE_UUID republished \([0-9][0-9]*\) PPPoE status event.*/\1/p" \
        | tail -1
}

# Print what a failed republish assertion needs to be explained: the rows, the
# controller's own account of the republish, and the node record it works from
# (RepublishAll skips anything that is not active with an address to dial).
# The stack is torn down when the run ends, so this has to be captured now.
dump_republish_evidence() {
    log_error "Current rows:"
    node_pppoe_rows
    log_error "Controller republish log lines (last 20):"
    compose logs --no-color controller 2>/dev/null \
        | grep -iE "republish|not being monitored" | tail -20
    log_error "Node record in etcd (nodes/$NODE_UUID):"
    etcd_get "nodes/$NODE_UUID"
    log_error "Node process log (last 15 lines):"
    ssh_node "tail -15 '$NODE_LOG' 2>/dev/null"
}

# True once every row in BASELINE_ROWS has a strictly newer event_time.
# With a phase argument, every row must also read that phase; without one, every
# row that was connected before the trigger has to be connected again.
rows_republished() {
    local want_phase=${1:-}
    local current user base_phase base_time cur_line cur_phase cur_time
    current=$(node_pppoe_rows)

    while read -r user base_phase base_time; do
        [ -n "$user" ] || continue
        cur_line=$(printf '%s\n' "$current" | awk -v u="$user" '$1 == u { print; exit }')
        [ -n "$cur_line" ] || return 1
        cur_phase=$(printf '%s\n' "$cur_line" | awk '{ print $2 }')
        cur_time=$(printf '%s\n' "$cur_line" | awk '{ print $3 }')
        [[ "$cur_time" > "$base_time" ]] || return 1
        if [ -n "$want_phase" ]; then
            [ "$cur_phase" = "$want_phase" ] || return 1
        elif [ "$base_phase" = "connected" ] && [ "$cur_phase" != "connected" ]; then
            return 1
        fi
    done <<< "$BASELINE_ROWS"

    return 0
}

# cleanup runs on every exit path (normal, error, or interrupt): stop the
# phase's own node process and restore the config so the node returns to its
# original (production) endpoints regardless of how the test ends. Guarded so
# it only acts after the config has actually been modified — and therefore
# only ever touches the fastrg process this phase started itself: a foreign
# fastrg (someone else's e2e) makes the phase skip before arming cleanup.
CLEANUP_ARMED=0
BRAS_STARTED=0
cleanup() {
    [ "$CLEANUP_ARMED" = "1" ] || return 0
    CLEANUP_ARMED=0
    log_info "Cleanup: stopping node and restoring config"
    node_stop || true
    node_restore_config || true
    # Only stop a dpdk-bras this phase started itself; one that was already
    # running belongs to someone else (or is a shared leftover) — leave it.
    if [ "$BRAS_STARTED" = "1" ]; then
        log_info "Cleanup: stopping the dpdk-bras this phase started"
        bras_stop || true
    fi
}
trap cleanup EXIT INT TERM

test_pppoe_republish() {
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

    # Never touch a fastrg process this phase did not start: the node machine
    # is shared and a running fastrg most likely belongs to another test (the
    # node repo's own e2e starts and stops it too). Skip the phase entirely so
    # concurrent test runs cannot terminate each other's processes.
    if node_is_running; then
        log_warn "A fastrg process is already running on $NODE_HOST — likely another e2e in progress."
        log_warn "Refusing to touch it; SKIPPING $PHASE."
        return 2
    fi
    log_success "Node prepared (no fastrg process running)"

    # Step 3: Seed the node's HSI/DNS/user-count config into etcd so PPPoE can
    # actually connect (the stack starts from clean volumes each run).
    log_info "Step 3: Seeding node HSI/DNS/user-count config into etcd"
    if ! seed_node_config "$NODE_UUID"; then
        log_error "Failed to seed node config into etcd"
        return 1
    fi
    log_success "Node config seeded (hsi/2, dns/2, user_counts)"

    # Step 3b: Start our own PPPoE server. Unlike the other node phase, this one
    # takes the BRAS down mid-test to force a disconnect, so it may only run
    # against a dpdk-bras it started itself. One that is already running belongs
    # to someone else — killing it would break their test, so skip instead.
    log_info "Step 3b: Starting a dedicated dpdk-bras on $BRAS_HOST"
    if bras_is_running; then
        log_warn "A dpdk-bras is already running on $BRAS_HOST — likely another e2e in progress."
        log_warn "This phase has to stop the BRAS itself, so it will not touch that one; SKIPPING $PHASE."
        return 2
    fi
    CLEANUP_ARMED=1
    BRAS_STARTED=1
    if ! bras_start; then
        log_error "dpdk-bras did not start on $BRAS_HOST within 24s"
        return 1
    fi
    log_success "dpdk-bras started on $BRAS_HOST (VLANs 3,5)"

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

    # Step 6: Wait for the PPPoE sessions to come up, then record the state the
    # republish has to rewrite. An empty table (or no connected session) would
    # make every later assertion vacuous, so both are checked here.
    log_info "Step 6: Waiting for PPPoE sessions and recording the baseline rows"
    local connected_count=0
    for _attempt in $(seq 1 60); do
        connected_count=$(pppoe_connected_count "$NODE_UUID" "connected")
        connected_count=${connected_count:-0}
        if [ "$connected_count" -gt 0 ]; then
            break
        fi
        sleep 2
    done
    if [ "$connected_count" -le 0 ]; then
        log_error "No PPPoE session reached the 'connected' phase within timeout"
        ssh_node "tail -30 '$NODE_LOG' 2>/dev/null"
        return 1
    fi

    BASELINE_ROWS=$(node_pppoe_rows)
    local baseline_row_count
    baseline_row_count=$(printf '%s\n' "$BASELINE_ROWS" | grep -c . || true)
    if [ "${baseline_row_count:-0}" -lt 1 ]; then
        log_error "pppoe_status has no row for node $NODE_UUID — nothing to republish"
        return 1
    fi
    log_success "Baseline recorded ($baseline_row_count row(s), $connected_count connected)"

    # Step 7: Restart the controller. Its Kafka consumer asks every registered
    # active node to re-send its PPPoE state once per consuming session.
    log_info "Step 7: Restarting controller to trigger a republish of all nodes"
    local log_count_before
    log_count_before=$(republish_log_count)
    stop_service "controller"
    start_service "controller"
    wait_for_service "controller" || return 1

    # Step 8: Every recorded row must be rewritten with a newer event_time and
    # the connected sessions must still read as connected. The republish happens
    # after the consumer's topic-ready and negative-lag guards and then travels
    # Kafka -> consumer -> database, so allow a generous window.
    log_info "Step 8: Verifying every pppoe_status row was rewritten"
    if ! wait_for "rows_republished" 120 3; then
        log_error "Not every pppoe_status row was rewritten after the controller restart"
        log_error "Baseline rows:"
        printf '%s\n' "$BASELINE_ROWS"
        dump_republish_evidence
        return 1
    fi
    log_success "All $baseline_row_count row(s) rewritten with a newer event_time"

    local log_count_after
    log_count_after=$(republish_log_count)
    if [ "${log_count_after:-0}" -le "${log_count_before:-0}" ]; then
        log_error "Controller logged no republish for node $NODE_UUID after the restart"
        dump_republish_evidence
        return 1
    fi
    local event_count
    event_count=$(republish_last_event_count)
    if [ -z "$event_count" ] || [ "$event_count" -lt "$baseline_row_count" ]; then
        log_error "Republish covered $event_count event(s), fewer than the $baseline_row_count known row(s)"
        dump_republish_evidence
        return 1
    fi
    log_success "Controller logged the republish ($event_count event(s) for node $NODE_UUID)"

    # Step 9: Restart the node. Registering again makes the controller ask this
    # one node to re-send its PPPoE state.
    log_info "Step 9: Restarting node to trigger a republish on re-registration"
    BASELINE_ROWS=$(node_pppoe_rows)
    log_count_before=$(republish_log_count)

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
    log_success "fastrg node process restarted"

    # Step 10: The re-registration must produce its own republish, and the table
    # must converge back to the same rows: newer event_time everywhere and the
    # sessions connected again (they pass through disconnected while the node is
    # down, so only the settled state is asserted).
    log_info "Step 10: Verifying the re-registration republish"
    if ! wait_for "[ \"\$(republish_log_count)\" -gt $log_count_before ]" 120 3; then
        log_error "Controller logged no republish for node $NODE_UUID after it re-registered"
        dump_republish_evidence
        return 1
    fi
    log_success "Controller logged a republish triggered by the node registration"

    if ! wait_for "rows_republished" 180 3; then
        log_error "pppoe_status did not converge back after the node restart"
        log_error "Baseline rows:"
        printf '%s\n' "$BASELINE_ROWS"
        dump_republish_evidence
        return 1
    fi
    log_success "All $baseline_row_count row(s) rewritten; every session that was connected is connected again"

    # Step 11: Drive the table out of sync with reality. With the database down
    # the consumer cannot record anything, so when the sessions drop the rows
    # keep claiming "connected" while the node knows better.
    log_info "Step 11: Dropping the sessions while the database is unavailable"
    BASELINE_ROWS=$(node_pppoe_rows)
    log_count_before=$(republish_log_count)
    # How many sessions were actually up before the outage. Step 14 restores the
    # fixture to exactly this many, so the check cannot be satisfied by a table
    # that came back empty or reshaped.
    local connected_before
    connected_before=$(pppoe_connected_count "$NODE_UUID" "connected")
    connected_before=${connected_before:-0}
    if [ "$connected_before" -lt 1 ]; then
        log_error "No connected session to drop — this step would prove nothing"
        dump_republish_evidence
        return 1
    fi
    stop_service "postgres"
    bras_stop
    BRAS_STARTED=0
    log_success "Database stopped and dpdk-bras stopped — sessions will drop unrecorded"

    # Step 12: Restart the controller while the database is still down, then
    # bring the database back. The consumer only starts once the database is
    # reachable, so its republish request lands after recovery.
    log_info "Step 12: Restarting controller, then bringing the database back"
    stop_service "controller"
    start_service "controller"
    wait_for_service "controller" || return 1
    start_service "postgres"
    wait_for_service "postgres" || return 1
    log_success "Controller restarted and database is back"

    # Step 13: The rows must catch up with reality — every one of them
    # disconnected, with a newer event_time. The node needs a few missed LCP
    # echoes to notice the BRAS is gone, and the events still have to travel
    # Kafka -> consumer -> database, hence the long window.
    log_info "Step 13: Verifying the rows converge to the real (disconnected) state"
    if ! wait_for "rows_republished disconnected" 300 5; then
        log_error "pppoe_status did not converge to 'disconnected' after the database came back"
        log_error "Baseline rows:"
        printf '%s\n' "$BASELINE_ROWS"
        dump_republish_evidence
        return 1
    fi
    log_success "All $baseline_row_count row(s) now read 'disconnected' with a newer event_time"

    log_count_after=$(republish_log_count)
    if [ "${log_count_after:-0}" -le "${log_count_before:-0}" ]; then
        log_error "Controller logged no republish for node $NODE_UUID after the database recovered"
        dump_republish_evidence
        return 1
    fi
    log_success "Controller logged the republish that followed the recovery"

    # Step 14: Put the fixture back the way the phase found it — BRAS up, every
    # row written again, and the sessions that were up before the outage up
    # again. Only rows that had a session can return to 'connected': a
    # subscriber slot with no HSI config never has one, and the node always
    # reports it as disconnected. So the phase check is "whatever was connected
    # is connected again", backed by a count check that the same number of
    # sessions really came back.
    log_info "Step 14: Restoring connectivity"
    if ! bras_start; then
        log_error "dpdk-bras did not restart on $BRAS_HOST within 24s"
        return 1
    fi
    BRAS_STARTED=1
    if ! wait_for "rows_republished" 300 5; then
        log_error "PPPoE sessions did not come back to 'connected' after the BRAS returned"
        log_error "Baseline rows:"
        printf '%s\n' "$BASELINE_ROWS"
        dump_republish_evidence
        return 1
    fi

    local connected_after
    connected_after=$(pppoe_connected_count "$NODE_UUID" "connected")
    connected_after=${connected_after:-0}
    if [ "$connected_after" -ne "$connected_before" ]; then
        log_error "Expected $connected_before connected session(s) after the BRAS returned, found $connected_after"
        dump_republish_evidence
        return 1
    fi
    log_success "Sessions reconnected — fixture restored ($connected_after connected)"

    log_success "$PHASE completed successfully!"
    return 0
}

# Run test (cleanup trap inside restores node config and stops the process)
test_pppoe_republish
result=$?
exit $result
