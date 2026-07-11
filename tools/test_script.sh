#!/bin/bash

# FastRG Controller REST/gRPC smoke + assertion suite.
# Usage: ./test_script.sh [ENDPOINT_ADDRESS] [FUNCTION_NAME]
# Default endpoint: 127.0.0.1
#
# Every check asserts an HTTP status and/or a response-body field and records a
# pass/fail. The suite exits non-zero if any assertion fails (see test_summary),
# so a controller that silently misbehaves now breaks CI instead of printing
# green. Ports are overridable via the same env vars the controller reads
# (HTTPS_PORT / HTTP_REDIRECT_PORT / GRPC_PORT), so the script can target a
# controller launched on non-default ports.

set -e

# Color output
RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
BLUE='\033[0;34m'
NC='\033[0m' # No Color

log_info()    { echo -e "${BLUE}[INFO]${NC} $1"; }
log_success() { echo -e "${GREEN}[SUCCESS]${NC} $1"; }
log_warning() { echo -e "${YELLOW}[WARNING]${NC} $1"; }
log_error()   { echo -e "${RED}[ERROR]${NC} $1"; }

# Endpoint + ports (ports match the controller's own env var names).
ENDPOINT=${1:-"127.0.0.1"}
HTTPS_PORT=${HTTPS_PORT:-8443}
HTTP_PORT=${HTTP_REDIRECT_PORT:-8080}
GRPC_PORT=${GRPC_PORT:-50051}
BASE="https://$ENDPOINT:$HTTPS_PORT"

log_info "Using endpoint $ENDPOINT (https:$HTTPS_PORT http:$HTTP_PORT grpc:$GRPC_PORT)"

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"

# ------------------------- assertion harness -------------------------
PASS_COUNT=0
FAIL_COUNT=0
FAILURES=""
TOKEN=""

pass() { PASS_COUNT=$((PASS_COUNT + 1)); log_success "$1"; }
fail() { FAIL_COUNT=$((FAIL_COUNT + 1)); FAILURES+="  - $1"$'\n'; log_error "$1"; }

# api METHOD PATH [JSON_BODY] -> sets HTTP_CODE and BODY. Adds bearer token if set.
api() {
    local method="$1" path="$2" data="$3"
    local args=(-s -k -w '\n%{http_code}' -X "$method")
    [ -n "$TOKEN" ] && args+=(-H "Authorization: $TOKEN")
    [ -n "$data" ] && args+=(-H 'Content-Type: application/json' -d "$data")
    local resp
    resp=$(curl "${args[@]}" "$BASE$path")
    HTTP_CODE=$(printf '%s' "$resp" | tail -n1)
    BODY=$(printf '%s' "$resp" | sed '$d')
}

assert_status() {
    if [ "$HTTP_CODE" = "$1" ]; then pass "$2 [HTTP $HTTP_CODE]"
    else fail "$2: expected HTTP $1, got $HTTP_CODE (body: $BODY)"; fi
}
assert_client_error() {
    if [ "${HTTP_CODE:-0}" -ge 400 ] && [ "${HTTP_CODE:-0}" -lt 500 ]; then pass "$1 [rejected HTTP $HTTP_CODE]"
    else fail "$1: expected 4xx rejection, got $HTTP_CODE (body: $BODY)"; fi
}
assert_json() { # jq_filter expected desc
    local got; got=$(printf '%s' "$BODY" | jq -r "$1" 2>/dev/null)
    if [ "$got" = "$2" ]; then pass "$3"
    else fail "$3: '$1' expected '$2', got '$got' (body: $BODY)"; fi
}
assert_json_true() { # jq_filter desc
    if printf '%s' "$BODY" | jq -e "$1" >/dev/null 2>&1; then pass "$2"
    else fail "$2: '$1' not truthy (body: $BODY)"; fi
}
assert_contains() { # needle desc
    if printf '%s' "$BODY" | grep -q -- "$1"; then pass "$2"
    else fail "$2: body missing '$1' (body: $BODY)"; fi
}
assert_not_contains() { # needle desc
    if printf '%s' "$BODY" | grep -q -- "$1"; then fail "$2: body unexpectedly contains '$1' (body: $BODY)"
    else pass "$2"; fi
}

login() {
    local resp
    resp=$(curl -s -k -X POST "$BASE/api/login" -H 'Content-Type: application/json' \
        -d '{"username":"admin","password":"admin"}')
    TOKEN=$(printf '%s' "$resp" | jq -r .token 2>/dev/null)
    if [ -z "$TOKEN" ] || [ "$TOKEN" = null ]; then
        fail "login: no token returned (resp: $resp)"
        TOKEN=""
        return 0
    fi
}
ensure_token() { if [ -z "$TOKEN" ] || [ "$TOKEN" = null ]; then login; fi; }

test_summary() {
    trap - ERR
    echo
    echo "==================== TEST SUMMARY ===================="
    log_info "Passed: $PASS_COUNT   Failed: $FAIL_COUNT"
    if [ "$FAIL_COUNT" -gt 0 ]; then
        echo -e "Failures:\n$FAILURES"
        log_error "Test suite FAILED"
        exit 1
    fi
    log_success "All $PASS_COUNT assertions passed"
}

# ------------------------- lifecycle helpers -------------------------
start_backend() {
    log_info "Starting backend (background)..."
    cd "$SCRIPT_DIR/.."
    make build-backend
    nohup sudo ./bin/controller > backend.log 2>&1 &
    sleep 2
    pidof controller > backend.pid || pgrep -f "controller" > backend.pid || true
    log_success "Backend log -> backend.log"
}

stop_backend() {
    if [ -f "$SCRIPT_DIR/../backend.pid" ]; then
        sudo kill "$(cat "$SCRIPT_DIR/../backend.pid")" || true
        rm -f "$SCRIPT_DIR/../backend.pid" || true
        log_success "Stopped backend"
    else
        log_info "No backend.pid found"
    fi
    rm -f "$SCRIPT_DIR/../bin/controller" || true
    rm -f "$SCRIPT_DIR/../backend.log"
}

test_etcd_seed() {
    log_info "Seeding etcd with test user and sample node..."
    cd "$SCRIPT_DIR"
    go run ./create_user || true
    go run ./put_node || true
}

start_test_etcd() {
    log_info "Starting etcd (docker)..."
    docker run -d --rm --name test-etcd -p 2379:2379 -p 2380:2380 \
        gcr.io/etcd-development/etcd:v3.6.5 \
        /usr/local/bin/etcd --name=node1 \
        --advertise-client-urls=http://0.0.0.0:2379 \
        --listen-client-urls=http://0.0.0.0:2379 \
        --initial-cluster node1=http://0.0.0.0:2380 \
        --initial-advertise-peer-urls http://0.0.0.0:2380 \
        --listen-peer-urls http://0.0.0.0:2380
}

stop_test_etcd() {
    log_info "Stopping etcd (docker)..."
    docker stop test-etcd || true
}

# ------------------------- feature tests -------------------------
test_login() {
    log_info "POST /api/login"
    api POST /api/login '{"username":"admin","password":"admin"}'
    assert_status 200 "login returns 200"
    assert_json_true '.token != null and (.token | length) > 0' "login returns a token"
}

test_nodes() {
    log_info "GET /api/nodes"
    ensure_token
    api GET /api/nodes
    assert_status 200 "list nodes returns 200"
    assert_contains "node1" "seeded node1 appears in node list"
}

test_grpc() {
    log_info "gRPC node registration + heartbeat"
    cd "$SCRIPT_DIR"
    if go run test_grpc/main.go --addr "$ENDPOINT:$GRPC_PORT"; then
        pass "gRPC register/heartbeat succeeded"
    else
        fail "gRPC register/heartbeat failed"
    fi
}

test_unregister() {
    log_info "Node unregistration via REST"
    cd "$SCRIPT_DIR"
    go run test_grpc/main.go --addr "$ENDPOINT:$GRPC_PORT" >/dev/null 2>&1 || true
    ensure_token
    api DELETE /api/nodes/test-node-001
    assert_status 200 "unregister test-node-001 returns 200"
    api GET /api/nodes
    assert_not_contains "test-node-001" "test-node-001 gone after unregister"
}

test_redirect() {
    log_info "HTTP -> HTTPS redirect on :$HTTP_PORT"
    local code
    code=$(curl -s -o /dev/null -w '%{http_code}' "http://$ENDPOINT:$HTTP_PORT/")
    if [ "$code" = 301 ] || [ "$code" = 308 ] || [ "$code" = 302 ]; then
        pass "HTTP redirect returns $code"
    else
        fail "HTTP redirect: expected 301/302/308, got $code"
    fi
}

test_logout() {
    log_info "Logout + token blacklist"
    login
    api GET /api/nodes
    assert_status 200 "token valid before logout"
    api POST /api/logout
    assert_status 200 "logout accepted"
    api GET /api/nodes
    assert_client_error "token rejected after logout"
    assert_json '.error' 'Token has been revoked' "revoked-token error message"
    TOKEN=""  # token is now blacklisted; force re-login for any later call
}

# ------------------------- HSI tests -------------------------
test_hsi_create() {
    log_info "POST /api/config/node1/hsi (create)"
    ensure_token
    api POST /api/config/node1/hsi \
        '{"user_id":"1001","vlan_id":"100","account_name":"test@example.com","password":"testpass123","dhcp_addr_pool":"192.168.1.10~192.168.1.200","dhcp_subnet":"255.255.255.0","dhcp_gateway":"192.168.1.1"}'
    assert_status 200 "create HSI 1001 returns 200"
    assert_json '.message' 'HSI config created successfully' "create HSI 1001 message"
}

test_hsi_users() {
    log_info "GET /api/config/node1/hsi/users"
    ensure_token
    api GET /api/config/node1/hsi/users
    assert_status 200 "list HSI users returns 200"
    assert_json_true '.user_ids | type == "array"' "list HSI users returns an array"
}

test_hsi_get_config() {
    log_info "GET /api/config/node1/hsi/1001"
    ensure_token
    api GET /api/config/node1/hsi/1001
    assert_status 200 "get HSI 1001 returns 200"
    assert_json_true '.account_name == "test@example.com" or .config.account_name == "test@example.com"' \
        "get HSI 1001 returns the created account_name"
}

test_hsi_update() {
    log_info "PUT /api/config/node1/hsi/1001 (update)"
    ensure_token
    api PUT /api/config/node1/hsi/1001 \
        '{"user_id":"1001","vlan_id":"200","account_name":"updated@example.com","password":"newpass456","dhcp_addr_pool":"10.0.1.50~10.0.1.150","dhcp_subnet":"255.0.0.0","dhcp_gateway":"10.0.1.1"}'
    assert_status 200 "update HSI 1001 returns 200"
    assert_json '.message' 'HSI config updated successfully' "update HSI 1001 message"
}

test_hsi_delete() {
    log_info "DELETE /api/config/node1/hsi/1001"
    ensure_token
    api DELETE /api/config/node1/hsi/1001
    assert_status 200 "delete HSI 1001 returns 200"
    assert_json '.message' 'HSI config deleted successfully' "delete HSI 1001 message"
}

# Validation exercises rules that actually exist: a missing required field must
# be rejected (400), and a duplicate VLAN must conflict (409). (There is no
# numeric user-id/VLAN range check without a configured subscriber count, so the
# old "user_id 2001 out of range" cases proved nothing.)
test_hsi_validation() {
    log_info "HSI validation"
    ensure_token

    log_info "1. Missing required field (account_name) -> 400"
    api POST /api/config/node1/hsi \
        '{"user_id":"1500","vlan_id":"150","account_name":"","password":"x","dhcp_addr_pool":"192.168.1.10~192.168.1.200","dhcp_subnet":"255.255.255.0","dhcp_gateway":"192.168.1.1"}'
    assert_status 400 "missing account_name is rejected"
    assert_json '.error' 'Account Name is required' "missing account_name error message"

    log_info "2. Duplicate VLAN -> 409 conflict"
    api POST /api/config/node1/hsi \
        '{"user_id":"1501","vlan_id":"1599","account_name":"a@example.com","password":"x","dhcp_addr_pool":"192.168.1.10~192.168.1.200","dhcp_subnet":"255.255.255.0","dhcp_gateway":"192.168.1.1"}'
    assert_status 200 "seed HSI 1501 (vlan 1599) for conflict test"
    api POST /api/config/node1/hsi \
        '{"user_id":"1502","vlan_id":"1599","account_name":"b@example.com","password":"x","dhcp_addr_pool":"192.168.1.10~192.168.1.200","dhcp_subnet":"255.255.255.0","dhcp_gateway":"192.168.1.1"}'
    assert_status 409 "duplicate VLAN 1599 is rejected as conflict"
    # cleanup
    api DELETE /api/config/node1/hsi/1501 >/dev/null 2>&1 || true
    api DELETE /api/config/node1/hsi/1502 >/dev/null 2>&1 || true
}

test_dhcp_configs() {
    log_info "Create configs across private-network classes (A/B/C)"
    ensure_token
    api POST /api/config/node1/hsi \
        '{"user_id":"1002","vlan_id":"101","account_name":"class_a@example.com","password":"testpass123","dhcp_addr_pool":"10.0.1.2~10.0.1.254","dhcp_subnet":"255.0.0.0","dhcp_gateway":"10.0.1.1"}'
    assert_status 200 "create Class-A config (1002)"
    api POST /api/config/node1/hsi \
        '{"user_id":"1003","vlan_id":"102","account_name":"class_b@example.com","password":"testpass123","dhcp_addr_pool":"172.16.1.10~172.16.1.100","dhcp_subnet":"255.255.0.0","dhcp_gateway":"172.16.1.1"}'
    assert_status 200 "create Class-B config (1003)"
    api POST /api/config/node1/hsi \
        '{"user_id":"1004","vlan_id":"103","account_name":"class_c@example.com","password":"testpass123","dhcp_addr_pool":"192.168.100.50~192.168.100.150","dhcp_subnet":"255.255.255.0","dhcp_gateway":"192.168.100.1"}'
    assert_status 200 "create Class-C config (1004)"
}

# Autofill relies on reading an existing config back; assert the round-trip
# instead of asking a human to open the web UI.
test_autofill() {
    log_info "Auto-fill data source (create -> read-back)"
    ensure_token
    api POST /api/config/node1/hsi \
        '{"user_id":"1006","vlan_id":"106","account_name":"autofill@example.com","password":"autofillpass","dhcp_addr_pool":"192.168.6.20~192.168.6.200","dhcp_subnet":"255.255.255.0","dhcp_gateway":"192.168.6.1"}'
    assert_status 200 "create autofill config (1006)"
    api GET /api/config/node1/hsi/1006
    assert_status 200 "read autofill config (1006)"
    assert_json_true '.account_name == "autofill@example.com" or .config.account_name == "autofill@example.com"' \
        "autofill read-back returns stored account_name"
    api DELETE /api/config/node1/hsi/1006 >/dev/null 2>&1 || true
}

test_hsi_workflow() {
    log_info "Complete HSI workflow (create/list/get/update/dial/hangup/delete)"
    ensure_token
    test_hsi_create
    test_hsi_users
    api GET /api/config/node1/hsi/users
    assert_contains "1001" "1001 present in user list after create"
    test_hsi_get_config
    test_hsi_update
    test_dhcp_configs

    log_info "PPPoE dial"
    api POST /api/pppoe/dial '{"node_id":"node1","user_id":"1001"}'
    assert_status 200 "PPPoE dial accepted"
    assert_json '.message' 'PPPoE dial request accepted' "PPPoE dial message"

    log_info "PPPoE hangup"
    api POST /api/pppoe/hangup '{"node_id":"node1","user_id":"1001"}'
    assert_status 200 "PPPoE hangup accepted"

    log_info "Delete workflow configs"
    test_hsi_delete
    for id in 1002 1003 1004; do
        api DELETE "/api/config/node1/hsi/$id" >/dev/null 2>&1 || true
    done
    api GET /api/config/node1/hsi/users
    assert_not_contains "1001" "1001 gone after delete"
}

test_hsi_apis() {
    test_hsi_create
    test_hsi_users
    test_hsi_get_config
    test_hsi_update
    test_dhcp_configs
    test_hsi_validation
    test_autofill
    test_hsi_delete
    for id in 1002 1003 1004; do
        api DELETE "/api/config/node1/hsi/$id" >/dev/null 2>&1 || true
    done
}

generate_test_certs() {
    cd "$SCRIPT_DIR/.."
    make generate-dev-certs
}

clean_test_certs() {
    cd "$SCRIPT_DIR/.."
    make clean-dev-certs
}

run_feature_tests() {
    test_etcd_seed
    ensure_token
    test_login
    test_nodes
    test_grpc
    test_redirect
    test_hsi_workflow
    test_hsi_apis
    test_unregister
    test_logout
    test_summary
}

run_all_tests() {
    start_test_etcd
    generate_test_certs
    test_etcd_seed
    start_backend
    run_feature_tests
    stop_backend
    stop_test_etcd
    clean_test_certs
}

show_usage() {
    echo "FastRG Controller Test Script"
    echo "Usage: $0 [ENDPOINT_ADDRESS] [FUNCTION_NAME]"
    echo ""
    echo "Arguments:"
    echo "  ENDPOINT_ADDRESS    Controller IP (default: 127.0.0.1)"
    echo "  FUNCTION_NAME       Specific test function to run (optional)"
    echo ""
    echo "Env overrides: HTTPS_PORT (8443), HTTP_REDIRECT_PORT (8080), GRPC_PORT (50051)"
    echo ""
    echo "Common functions: test_login test_nodes test_grpc test_redirect"
    echo "  test_logout test_hsi_workflow test_hsi_apis run_feature_tests run_all_tests"
    echo ""
    echo "Examples:"
    echo "  $0                                # default endpoint 127.0.0.1"
    echo "  $0 192.168.1.100 run_feature_tests"
}

# Main
if [ "$1" = "-h" ] || [ "$1" = "--help" ]; then
    show_usage
    exit 0
fi

# ---- CI failure diagnostics ----
on_error() {
    echo
    echo "==================== CI DIAGNOSTICS (on error) ===================="
    echo "Date: $(date)"
    echo "ENDPOINT: $ENDPOINT  (https:$HTTPS_PORT http:$HTTP_PORT grpc:$GRPC_PORT)"
    echo "ETCD_ENDPOINTS: ${ETCD_ENDPOINTS:-unset}"
    env | grep -E 'GITHUB|CI|ETCD|ENDPOINT|PORT' || true
    echo
    echo "Processes (controller / etcd / docker):"
    ps aux | egrep 'controller|etcd|dockerd|docker' || true
    echo
    echo "Built binary:"
    ls -l "$SCRIPT_DIR/../bin" || true
    echo
    echo "backend.log (tail):"
    [ -f "$SCRIPT_DIR/../backend.log" ] && tail -200 "$SCRIPT_DIR/../backend.log" || echo "no backend.log"
    echo
    echo "Open ports:"
    if command -v ss >/dev/null 2>&1; then ss -lntp || true; elif command -v netstat >/dev/null 2>&1; then netstat -lntp || true; fi
    echo
    echo "etcd health:"
    curl -v --max-time 5 "http://127.0.0.1:2379/health" || true
    echo
    echo "HTTPS health (controller):"
    curl -vk --max-time 5 "$BASE/api/health" || true
    echo "================================================================="
}
trap on_error ERR

if [ -n "$2" ]; then
    if type "$2" > /dev/null 2>&1; then
        log_info "Running specific function: $2"
        $2
    else
        log_error "Function '$2' not found"
        show_usage
        exit 1
    fi
else
    log_info "Running complete test suite with endpoint: $ENDPOINT"
    run_all_tests
fi
