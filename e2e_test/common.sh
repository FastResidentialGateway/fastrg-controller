#!/bin/bash

# Common utilities for E2E tests

set -e

# Colors for output
RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
BLUE='\033[0;34m'
NC='\033[0m' # No Color

# Logging functions
log_info() {
    echo -e "${BLUE}[INFO]${NC} $1"
}

log_success() {
    echo -e "${GREEN}[✓]${NC} $1"
}

log_warn() {
    echo -e "${YELLOW}[WARN]${NC} $1"
}

log_error() {
    echo -e "${RED}[✗]${NC} $1"
}

compose() {
    if [[ "${E2E_COMPOSE_VIA_SSH:-0}" == "1" ]]; then
        local quoted_dir
        local quoted_args=""
        local arg

        printf -v quoted_dir '%q' "${COMPOSE_DIR:-/root/fastrg-controller/e2e_test}"
        for arg in "$@"; do
            local quoted_arg
            printf -v quoted_arg '%q' "$arg"
            quoted_args+=" ${quoted_arg}"
        done

        ssh_controller "cd ${quoted_dir} && if command -v docker-compose >/dev/null 2>&1; then docker-compose${quoted_args}; else docker compose${quoted_args}; fi"
    elif command -v docker-compose >/dev/null 2>&1; then
        docker-compose "$@"
    else
        docker compose "$@"
    fi
}

compose_quiet() {
    compose "$@" >/dev/null 2>&1
}

# Wait for service to be healthy
wait_for_service() {
    local service=$1
    local max_attempts=30
    local attempt=0

    log_info "Waiting for $service to be healthy..."

    while [ $attempt -lt $max_attempts ]; do
        if compose ps "$service" | grep -q "healthy"; then
            log_success "$service is healthy"
            return 0
        fi

        attempt=$((attempt + 1))
        sleep 1
    done

    log_error "Timeout waiting for $service to become healthy"
    return 1
}

# Check if service is up
is_service_up() {
    local service=$1
    compose ps "$service" 2>/dev/null | grep -q "Up\|healthy" && return 0 || return 1
}

# http_get_code <url>: print the HTTP status code (000 on connection failure),
# ignoring TLS verification. The e2e runner host is not guaranteed to have
# curl (a bare Ubuntu runner only ships wget/python3), so fall back to
# python3 — phase 1 used to fail on the runner solely because curl was absent.
http_get_code() {
    local url=$1
    if command -v curl >/dev/null 2>&1; then
        curl -s -k -o /dev/null -m 5 -w '%{http_code}' "$url" 2>/dev/null || echo 000
    else
        python3 - "$url" 2>/dev/null <<'PY' || echo 000
import ssl, sys, urllib.error, urllib.request
ctx = ssl.create_default_context()
ctx.check_hostname = False
ctx.verify_mode = ssl.CERT_NONE
try:
    print(urllib.request.urlopen(sys.argv[1], timeout=5, context=ctx).status)
except urllib.error.HTTPError as e:
    print(e.code)
except Exception:
    print("000")
PY
    fi
}

# http_get_body <url>: print the response body (empty on failure), ignoring
# TLS verification. Same curl-or-python3 fallback as http_get_code.
http_get_body() {
    local url=$1
    if command -v curl >/dev/null 2>&1; then
        curl -s -k -H "Content-Type: application/json" "$url" 2>/dev/null || echo ""
    else
        python3 - "$url" 2>/dev/null <<'PY' || echo ""
import ssl, sys, urllib.request
ctx = ssl.create_default_context()
ctx.check_hostname = False
ctx.verify_mode = ssl.CERT_NONE
try:
    print(urllib.request.urlopen(sys.argv[1], timeout=10, context=ctx).read().decode())
except Exception:
    print("")
PY
    fi
}

# True if the controller's HTTPS API layer answers at all (any HTTP status),
# proving process/listener liveness — a stronger check than container "Up"
# state. HTTP code 000 means the connection was refused (server not answering).
controller_http_alive() {
    local code
    code=$(http_get_code "https://${CONTROLLER_HOST:-localhost}:28443/api/health")
    [ "$code" != "000" ]
}

# Stop a service
stop_service() {
    local service=$1
    log_info "Stopping $service..."
    compose stop "$service" || true
    sleep 2
}

# Start a service
start_service() {
    local service=$1
    log_info "Starting $service..."
    compose start "$service" || compose up -d "$service"
    sleep 2
}

# Query etcd
etcd_get() {
    local key=$1
    compose exec -T etcd etcdctl --endpoints=localhost:2379 get "$key" --print-value-only 2>/dev/null || echo ""
}

# Query database
# Returns only the data rows, skipping psql header/footer output
db_query() {
    local query=$1
    compose exec -T postgres psql -U fastrg -d fastrg -t -c "$query" 2>/dev/null | grep -v '^$' || echo ""
}

config_history_count() {
    local node_id=$1
    local user_id=$2
    db_query "SELECT COUNT(*) FROM hsi_config_history WHERE node_uuid='$node_id' AND user_id='$user_id';" 2>/dev/null | xargs
}

dlq_pending_count() {
    db_query "SELECT COUNT(*) FROM kafka_dlq WHERE status='pending';" 2>/dev/null | xargs
}

kafka_ensure_topic() {
    local topic=${1:-fastrg.node.events}
    compose exec -T kafka /opt/kafka/bin/kafka-topics.sh \
        --bootstrap-server localhost:9092 \
        --create \
        --if-not-exists \
        --topic "$topic" \
        --partitions 1 \
        --replication-factor 1 >/dev/null
}

kafka_produce_base64() {
    local topic=$1
    local payload_base64=$2

    # kafka-producer-perf-test.sh --payload-file treats binary as text and splits
    # on newlines, which corrupts protobuf payloads that contain 0x0A bytes.
    # Use the kafka_produce Go tool on the controller host instead; it writes the
    # full binary payload in a single Kafka message without any line-splitting.
    local kafka_brokers="${KAFKA_BROKERS:-localhost:29092}"
    ssh_controller "cd /root/fastrg-controller && KAFKA_BROKERS='$kafka_brokers' KAFKA_TOPIC='$topic' /usr/local/go/bin/go run ./tools/kafka_produce/main.go '$payload_base64'"
}

# Query controller REST API
api_get() {
    local endpoint=$1
    http_get_body "https://${CONTROLLER_HOST:-localhost}:28443/api$endpoint"
}

# Get node status from controller
node_status() {
    local node_uuid=$1
    api_get "/node/$node_uuid" | jq '.' 2>/dev/null || echo ""
}

# Get config from etcd
config_get() {
    local node_id=$1
    local user_id=$2
    etcd_get "configs/$node_id/hsi/$user_id"
}

# Get PPPoE status from database
pppoe_status() {
    local node_uuid=$1
    local user_id=$2
    db_query "SELECT phase FROM pppoe_status WHERE node_uuid='$node_uuid' AND user_id='$user_id';" 2>/dev/null
}

# Wait for condition with timeout
wait_for() {
    local condition=$1
    local timeout=${2:-30}
    local interval=${3:-1}
    local elapsed=0

    while [ $elapsed -lt $timeout ]; do
        if eval "$condition"; then
            return 0
        fi
        sleep $interval
        elapsed=$((elapsed + interval))
    done

    return 1
}

# Verify config in etcd and database match
verify_config_sync() {
    local node_id=$1
    local user_id=$2

    log_info "Verifying config sync for node=$node_id user=$user_id"

    local etcd_config=$(config_get "$node_id" "$user_id")
    local db_config=$(db_query "SELECT config FROM hsi_config_current WHERE node_uuid='$node_id' AND user_id='$user_id';" 2>/dev/null)

    if [ -z "$etcd_config" ] && [ -z "$db_config" ]; then
        log_success "Config not set (OK)"
        return 0
    fi

    if [ "$etcd_config" == "$db_config" ]; then
        log_success "Config matches between etcd and database"
        return 0
    else
        log_error "Config mismatch!"
        log_error "etcd: $etcd_config"
        log_error "db:   $db_config"
        return 1
    fi
}

# Get Kafka consumer lag
kafka_lag() {
    compose exec -T kafka kafka-consumer-groups.sh \
        --bootstrap-server localhost:9092 \
        --group fastrg-controller \
        --describe 2>/dev/null | tail -1 || echo "unknown"
}

# ---------------------------------------------------------------------------
# FastRG Node control helpers (Phase 4)
# ---------------------------------------------------------------------------
# The node config lives at /etc/fastrg/config.cfg and normally points at the
# production controller. For the e2e docker-compose controller the endpoints use
# different ports (etcd 22379, kafka 29092, gRPC 50052), so the phase rewrites
# config.cfg before starting the node and restores it afterwards.
NODE_CONFIG="/etc/fastrg/config.cfg"
NODE_CONFIG_BACKUP="/etc/fastrg/config.cfg.e2e-bak"
NODE_LOG="/tmp/fastrg-e2e-node.log"
NODE_BIN="/root/fastrg-node/fastrg"
NODE_ARGS="-l 1-8 -n 4 -a 0000:07:00.0 -a 0000:08:00.0"

# ---- BRAS (PPPoE server) lifecycle -------------------------------------
# The lab's PPPoE terminator is dpdk-bras, an on-demand simulator on the BRAS
# host (started per test run, e.g. by the fastrg-node repo's own e2e — it is
# NOT an always-on service). Phase 4 must ensure one is running before the
# node dials, following the shared-resource rule: a dpdk-bras we did not
# start is left strictly alone; one we started is stopped when we are done.
BRAS_HOST="${BRAS_HOST:-192.168.10.215}"

ssh_bras() {
    ssh $SSH_OPTS "root@${BRAS_HOST}" "$@" 2>&1
}

bras_is_running() {
    ssh_bras "pgrep -x dpdk-bras >/dev/null 2>&1"
}

# Start dpdk-bras (VLANs 3 and 5, matching the seeded subscribers). dpdk-bras
# runs in the foreground and keeps its SSH channel open (DPDK re-opens fds, so
# remote setsid/nohup cannot detach it) — run it over a locally-backgrounded
# SSH client instead and record that client's PID so bras_stop can clean up.
BRAS_SSH_PID=""
bras_start() {
    ssh_bras "cd /root/dpdk-bras && exec ./dpdk-bras -l 0-7 -n 4 -- --pri-dns 192.168.10.1 --drop-pcap ./test.pcap --vlans 3,5 >/var/log/dpdk-bras.log 2>&1" </dev/null >/dev/null 2>&1 &
    BRAS_SSH_PID=$!
    local _i
    for _i in $(seq 1 12); do
        sleep 2
        if bras_is_running; then
            return 0
        fi
    done
    ssh_bras "tail -20 /var/log/dpdk-bras.log 2>/dev/null || true"
    return 1
}

bras_stop() {
    ssh_bras "pkill -x dpdk-bras 2>/dev/null || true"
    if [ -n "$BRAS_SSH_PID" ]; then
        kill "$BRAS_SSH_PID" >/dev/null 2>&1 || true
        wait "$BRAS_SSH_PID" 2>/dev/null || true
        BRAS_SSH_PID=""
    fi
}

# Point the node config at the e2e docker-compose controller endpoints.
# Backs up the original config first so node_restore_config can undo it.
node_point_config_to_e2e() {
    local controller_host="${CONTROLLER_HOST:-192.168.10.212}"
    ssh_node "
        cp -f '$NODE_CONFIG' '$NODE_CONFIG_BACKUP' &&
        sed -i \
            -e 's|^ControllerAddress = .*|ControllerAddress = \"${controller_host}:50052\";|' \
            -e 's|^EtcdEndpoints = .*|EtcdEndpoints = \"${controller_host}:22379\";|' \
            -e 's|^KafkaBrokers = .*|KafkaBrokers = \"${controller_host}:29092\";|' \
            '$NODE_CONFIG' &&
        grep -E 'ControllerAddress|EtcdEndpoints|KafkaBrokers' '$NODE_CONFIG'
    "
}

# Restore the original node config from the backup made above.
node_restore_config() {
    ssh_node "
        if [ -f '$NODE_CONFIG_BACKUP' ]; then
            mv -f '$NODE_CONFIG_BACKUP' '$NODE_CONFIG' &&
            echo 'config restored'
        else
            echo 'no backup found, leaving config as-is'
        fi
    "
}

# Stop any running fastrg process on the node (idempotent).
node_stop() {
    ssh_node "pkill -x fastrg 2>/dev/null; sleep 2; pkill -9 -x fastrg 2>/dev/null; true"
}

# Start the fastrg node process in the background, logging to NODE_LOG.
# fastrg (DPDK) keeps file descriptors open that keep the SSH channel alive even
# when stdio is redirected, so the launching ssh would hang. setsid fully
# detaches the process from the SSH session, so we wrap the launch ssh in a
# short timeout (killing the ssh does not kill the already-detached fastrg) and
# then verify it is running with a separate ssh call.
node_start() {
    timeout 10 ssh $SSH_OPTS "root@${NODE_HOST}" \
        "cd /root/fastrg-node && setsid $NODE_BIN $NODE_ARGS < /dev/null > '$NODE_LOG' 2>&1" \
        >/dev/null 2>&1
    sleep 2
    if ssh_node "pgrep -x fastrg >/dev/null && echo up || echo down" | grep -q up; then
        echo "fastrg started"
    else
        echo "fastrg failed to start"
    fi
}

# Check whether the fastrg process is currently running on the node.
node_is_running() {
    ssh_node "pgrep -x fastrg >/dev/null && echo up || echo down" | grep -q up
}

# Count PPPoE rows in the given phase for a node. The controller records the raw
# per-session PPPoE status it polls from the node; "Data phase" is the
# established/data-passing state. Default matches that established state.
pppoe_connected_count() {
    local node_uuid=$1
    local phase=${2:-Data phase}
    db_query "SELECT COUNT(*) FROM pppoe_status WHERE node_uuid='$node_uuid' AND phase='$phase';" 2>/dev/null | xargs
}

export -f log_info log_success log_warn log_error compose compose_quiet
export -f wait_for_service is_service_up http_get_code http_get_body controller_http_alive stop_service start_service
export -f etcd_get db_query config_history_count dlq_pending_count kafka_ensure_topic kafka_produce_base64
export -f api_get node_status config_get pppoe_status
export -f wait_for verify_config_sync kafka_lag
export -f node_point_config_to_e2e node_restore_config node_stop node_start node_is_running pppoe_connected_count
export -f ssh_bras bras_is_running bras_start bras_stop
export BRAS_HOST
