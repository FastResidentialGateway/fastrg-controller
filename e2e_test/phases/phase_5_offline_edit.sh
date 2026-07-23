#!/usr/bin/env bash

# Phase 5: ConfigOfflineEdit arbitration
# Inject protobuf events through Kafka and verify accepted edits, discarded
# edits with an audit row, and HSI tombstone DNS cascade behavior.

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

PHASE="Phase 5: Offline Edit Arbitration"
KAFKA_TOPIC="fastrg.node.events"
WIN_NODE="phase5-win"
WIN_USER="61"
DISCARD_NODE="phase5-discard"
DISCARD_USER="62"
TOMB_NODE="phase5-tomb"
TOMB_USER="63"

# Generated from proto/eventsv1/kafka-events.proto. The three events are an
# accepted HSI content edit, a losing HSI content edit, and an accepted HSI
# tombstone respectively.
WIN_EVENT_BASE64="CgpwaGFzZTUtd2luEgI2MRgDIN2pgtMGaroBCpwBeyJjb25maWciOnsidXNlcl9pZCI6IjYxIiwidmxhbl9pZCI6IjIwMCJ9LCJtZXRhZGF0YSI6eyJub2RlIjoicGhhc2U1LXdpbiIsInJlc291cmNlVmVyc2lvbiI6IjIiLCJ1cGRhdGVkQnkiOiJub2RlLWNsaSIsInVwZGF0ZWRBdCI6IjIwMjYtMDctMjJUMTA6MDE6MDBaIn19EgEyGNypgtMGIg5vZmZsaW5lIHdpbm5lcigB"
DISCARD_EVENT_BASE64="Cg5waGFzZTUtZGlzY2FyZBICNjIYAyDeqYLTBmq/AQqgAXsiY29uZmlnIjp7InVzZXJfaWQiOiI2MiIsInZsYW5faWQiOiI5OTkifSwibWV0YWRhdGEiOnsibm9kZSI6InBoYXNlNS1kaXNjYXJkIiwicmVzb3VyY2VWZXJzaW9uIjoiMiIsInVwZGF0ZWRCeSI6Im5vZGUtY2xpIiwidXBkYXRlZEF0IjoiMjAyNi0wNy0yMlQxMDowMTowMFoifX0SATIY3KmC0wYiD29mZmxpbmUgZGlzY2FyZCgB"
TOMB_EVENT_BASE64="CgtwaGFzZTUtdG9tYhICNjMYAyDfqYLTBmodEgE1GNypgtMGIg5vZmZsaW5lIGRlbGV0ZSgBMAE="

cleanup() {
    compose exec -T etcd etcdctl --endpoints=localhost:2379 del "configs/$WIN_NODE/hsi/$WIN_USER" >/dev/null 2>&1 || true
    compose exec -T etcd etcdctl --endpoints=localhost:2379 del "configs/$DISCARD_NODE/hsi/$DISCARD_USER" >/dev/null 2>&1 || true
    compose exec -T etcd etcdctl --endpoints=localhost:2379 del "configs/$TOMB_NODE/hsi/$TOMB_USER" >/dev/null 2>&1 || true
    compose exec -T etcd etcdctl --endpoints=localhost:2379 del "configs/$TOMB_NODE/dns/$TOMB_USER" >/dev/null 2>&1 || true
    db_query "DELETE FROM node_events WHERE node_uuid IN ('$WIN_NODE','$DISCARD_NODE','$TOMB_NODE');" >/dev/null || true
}
trap cleanup EXIT INT TERM

test_offline_edit_arbitration() {
    log_info "========== $PHASE =========="

    wait_for_service "etcd" || return 1
    wait_for_service "postgres" || return 1
    wait_for_service "controller" || return 1
    kafka_ensure_topic "$KAFKA_TOPIC" || return 1
    cleanup

    local win_current='{"config":{"user_id":"61","vlan_id":"100"},"metadata":{"node":"phase5-win","resourceVersion":"1","updatedBy":"controller","updatedAt":"2026-07-22T10:00:00Z"}}'
    local discard_current='{"config":{"user_id":"62","vlan_id":"300"},"metadata":{"node":"phase5-discard","resourceVersion":"3","updatedBy":"controller","updatedAt":"2026-07-22T10:00:00Z"}}'
    local tomb_hsi='{"config":{"user_id":"63","vlan_id":"400"},"metadata":{"node":"phase5-tomb","resourceVersion":"5","updatedBy":"controller","updatedAt":"2026-07-22T10:00:00Z"}}'
    local tomb_dns='{"records":[{"domain":"orphan.test","ip":"192.0.2.63","ttl":30}],"metadata":{"node":"phase5-tomb","resourceVersion":"1","updatedBy":"controller","updatedAt":"2026-07-22T10:00:00Z"}}'

    log_info "Seeding arbitration inputs"
    compose exec -T etcd etcdctl --endpoints=localhost:2379 put "configs/$WIN_NODE/hsi/$WIN_USER" "$win_current" >/dev/null
    compose exec -T etcd etcdctl --endpoints=localhost:2379 put "configs/$DISCARD_NODE/hsi/$DISCARD_USER" "$discard_current" >/dev/null
    compose exec -T etcd etcdctl --endpoints=localhost:2379 put "configs/$TOMB_NODE/hsi/$TOMB_USER" "$tomb_hsi" >/dev/null
    compose exec -T etcd etcdctl --endpoints=localhost:2379 put "configs/$TOMB_NODE/dns/$TOMB_USER" "$tomb_dns" >/dev/null

    log_info "Producing accepted content edit"
    kafka_produce_base64 "$KAFKA_TOPIC" "$WIN_EVENT_BASE64" || return 1
    if ! wait_for "[ \"\$(etcd_get 'configs/$WIN_NODE/hsi/$WIN_USER' | jq -r '.config.vlan_id // empty')\" = '200' ]" 30 1; then
        log_error "Accepted offline edit did not update etcd payload"
        return 1
    fi
    local win_value
    win_value=$(etcd_get "configs/$WIN_NODE/hsi/$WIN_USER")
    if [ "$(printf '%s' "$win_value" | jq -r '.metadata.resourceVersion')" != "2" ] ||
       [ "$(printf '%s' "$win_value" | jq -r '.metadata.updatedBy')" != "node-offline-edit" ]; then
        log_error "Accepted offline edit did not receive fresh controller metadata"
        return 1
    fi
    log_success "Accepted content edit updated payload and re-stamped metadata"

    log_info "Producing losing content edit"
    kafka_produce_base64 "$KAFKA_TOPIC" "$DISCARD_EVENT_BASE64" || return 1
    if ! wait_for "[ \"\$(db_query \"SELECT count(*) FROM node_events WHERE node_uuid='$DISCARD_NODE' AND event_type='CONFIG_OFFLINE_EDIT';\" | xargs)\" = '1' ]" 30 1; then
        log_error "Discarded offline edit did not create node_events audit row"
        return 1
    fi
    local discard_value
    discard_value=$(etcd_get "configs/$DISCARD_NODE/hsi/$DISCARD_USER")
    if [ "$(printf '%s' "$discard_value" | jq -r '.config.vlan_id')" != "300" ] ||
       [ "$(printf '%s' "$discard_value" | jq -r '.metadata.resourceVersion')" != "3" ]; then
        log_error "Discarded offline edit changed controller state"
        return 1
    fi
    log_success "Losing content edit preserved etcd and created an audit row"

    log_info "Producing accepted tombstone"
    kafka_produce_base64 "$KAFKA_TOPIC" "$TOMB_EVENT_BASE64" || return 1
    if ! wait_for "[ -z \"\$(etcd_get 'configs/$TOMB_NODE/hsi/$TOMB_USER')\" ] && [ -z \"\$(etcd_get 'configs/$TOMB_NODE/dns/$TOMB_USER')\" ]" 30 1; then
        log_error "Accepted tombstone did not delete both HSI and DNS keys"
        return 1
    fi
    log_success "Accepted tombstone deleted HSI and cascaded DNS"

    log_success "$PHASE completed successfully!"
}

test_offline_edit_arbitration
