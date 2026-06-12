#!/bin/bash

# FastRG Controller Test Script
# Migrated from Makefile lines 7-248
# Usage: ./test_script.sh [ENDPOINT_ADDRESS]
# Default endpoint: 127.0.0.1

set -e

# Color output
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
    echo -e "${GREEN}[SUCCESS]${NC} $1"
}

log_warning() {
    echo -e "${YELLOW}[WARNING]${NC} $1"
}

log_error() {
    echo -e "${RED}[ERROR]${NC} $1"
}

# Parse command line arguments
ENDPOINT=${1:-"127.0.0.1"}

log_info "Using endpoint address: $ENDPOINT"

# Get the script directory
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"

# Function: Start backend in background and write pid to backend.pid (for tests)
start_backend() {
    log_info "Starting backend (background)..."
    cd "$SCRIPT_DIR/.."
    log_info "Building backend..."
    make build-backend
    log_info "Starting backend..."
    nohup sudo ./bin/controller > backend.log 2>&1 &
    sleep 2
    pidof controller > backend.pid || pgrep -f "controller" > backend.pid || true
    log_success "Backend log -> backend.log"
}

# Function: Stop backend
stop_backend() {

    if [ -f "$SCRIPT_DIR/../backend.pid" ]; then
        sudo kill $(cat "$SCRIPT_DIR/../backend.pid") || true
        rm -f "$SCRIPT_DIR/../backend.pid" || true
        log_success "Stopped backend"
    else
        log_info "No backend.pid found"
    fi
    rm -f "$SCRIPT_DIR/../bin/controller" || true
    rm -f "$SCRIPT_DIR/../backend.log"
}

# Function: Create a test user in etcd
test_etcd_seed() {
    log_info "Seeding etcd with test user and sample node..."
    cd "$SCRIPT_DIR"
    go run ./create_user || true
    go run ./put_node || true
}

# Function: Test login via REST (HTTPS)
test_login() {
    log_info "Testing POST /api/login (HTTPS)..."
    curl -s -k -X POST https://$ENDPOINT:8443/api/login -H 'Content-Type: application/json' -d '{"username":"admin","password":"admin"}' | jq -C . || true
}

# Function: Test fetch nodes (HTTPS)
test_nodes() {
    log_info "Testing GET /api/nodes (HTTPS)..."
    TOKEN=$(curl -s -k -X POST https://$ENDPOINT:8443/api/login -H 'Content-Type: application/json' -d '{"username":"admin","password":"admin"}' | jq -r .token)
    if [ -z "$TOKEN" ] || [ "$TOKEN" = "null" ]; then
        log_error "No token, aborting"
        exit 1
    fi
    curl -s -k -H "Authorization: $TOKEN" https://$ENDPOINT:8443/api/nodes | jq -C . || true
}

# Function: Start etcd in docker (detached) and map 2379
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

# Function: Stop etcd
stop_test_etcd() {
    log_info "Stopping etcd (docker)..."
    docker stop test-etcd || true
}

# Function: Test gRPC node registration and heartbeat with new proto fields
test_grpc() {
    log_info "Testing gRPC node registration and heartbeat with updated proto..."
    cd "$SCRIPT_DIR"
    go generate ../proto/... || true
    go run test_grpc/main.go --addr "$ENDPOINT:50051"
}

# Function: Test Node unregistration via REST API
test_unregister() {
    log_info "Testing node unregistration..."
    log_info "First, register a test node via gRPC..."
    cd "$SCRIPT_DIR"
    go run test_grpc/main.go --addr "$ENDPOINT:50051"
    
    log_info "Now test unregistration via REST API..."
    TOKEN=$(curl -s -k -X POST https://$ENDPOINT:8443/api/login -H "Content-Type: application/json" -d '{"username":"admin","password":"admin"}' | jq -r .token)
    curl -s -k -X DELETE -H "Authorization: $TOKEN" https://$ENDPOINT:8443/api/nodes/test-node-001 | jq .
    
    log_info "Verification - check remaining nodes..."
    TOKEN=$(curl -s -k -X POST https://$ENDPOINT:8443/api/login -H "Content-Type: application/json" -d '{"username":"admin","password":"admin"}' | jq -r .token)
    curl -s -k -H "Authorization: $TOKEN" https://$ENDPOINT:8443/api/nodes | jq .
}

# Function: Test HTTP to HTTPS redirect
test_redirect() {
    log_info "Testing HTTP to HTTPS redirect..."
    curl -I http://$ENDPOINT:8080/
    log_info "Testing full redirect flow..."
    curl -L -k -s http://$ENDPOINT:8080/ | head -3
}

# Function: Test logout with token blacklist
test_logout() {
    log_info "Testing logout with token blacklist..."
    log_info "1. Login and get token..."
    TOKEN=$(curl -s -k -X POST https://$ENDPOINT:8443/api/login -H "Content-Type: application/json" -d '{"username":"admin","password":"admin"}' | jq -r .token)
    
    log_info "2. Test token validity..."
    curl -s -k -H "Authorization: $TOKEN" https://$ENDPOINT:8443/api/nodes > /dev/null && log_success "Token is valid"
    
    log_info "3. Logout..."
    curl -s -k -X POST -H "Authorization: $TOKEN" https://$ENDPOINT:8443/api/logout | jq -r .message
    
    log_info "4. Test token after logout..."
    RESULT=$(curl -s -k -H "Authorization: $TOKEN" https://$ENDPOINT:8443/api/nodes | jq -r .error)
    if [ "$RESULT" = "Token has been revoked" ]; then
        log_success "Token successfully blacklisted"
    else
        log_error "Token blacklist failed"
    fi
}

# HSI (High Speed Internet) API Tests

# Function: Test Create HSI config
test_hsi_create() {
    log_info "Testing POST /api/config/:nodeId/hsi (Create HSI config with PPPoE and DHCP)..."
    TOKEN=$(curl -s -k -X POST https://$ENDPOINT:8443/api/login -H "Content-Type: application/json" -d '{"username":"admin","password":"admin"}' | jq -r .token)
    curl -s -k -X POST -H "Authorization: $TOKEN" -H "Content-Type: application/json" \
        https://$ENDPOINT:8443/api/config/node1/hsi \
        -d '{"user_id":"1001","vlan_id":"100","account_name":"test@example.com","password":"testpass123","dhcp_addr_pool":"192.168.1.10~192.168.1.200","dhcp_subnet":"255.255.255.0","dhcp_gateway":"192.168.1.1"}' | jq -C . || true
}

# Function: Test Get HSI user IDs list
test_hsi_users() {
    log_info "Testing GET /api/config/:nodeId/hsi/users (Get HSI user IDs)..."
    TOKEN=$(curl -s -k -X POST https://$ENDPOINT:8443/api/login -H "Content-Type: application/json" -d '{"username":"admin","password":"admin"}' | jq -r .token)
    curl -s -k -H "Authorization: $TOKEN" https://$ENDPOINT:8443/api/config/node1/hsi/users | jq -C . || true
}

# Function: Test Get specific HSI config
test_hsi_get_config() {
    log_info "Testing GET /api/config/:nodeId/hsi/:userId (Get HSI config)..."
    TOKEN=$(curl -s -k -X POST https://$ENDPOINT:8443/api/login -H "Content-Type: application/json" -d '{"username":"admin","password":"admin"}' | jq -r .token)
    curl -s -k -H "Authorization: $TOKEN" https://$ENDPOINT:8443/api/config/node1/hsi/1001 | jq -C . || true
}

# Function: Test Update HSI config
test_hsi_update() {
    log_info "Testing PUT /api/config/:nodeId/hsi/:userId (Update HSI config)..."
    TOKEN=$(curl -s -k -X POST https://$ENDPOINT:8443/api/login -H "Content-Type: application/json" -d '{"username":"admin","password":"admin"}' | jq -r .token)
    curl -s -k -X PUT -H "Authorization: $TOKEN" -H "Content-Type: application/json" \
        https://$ENDPOINT:8443/api/config/node1/hsi/1001 \
        -d '{"user_id":"1001","vlan_id":"200","account_name":"updated@example.com","password":"newpass456","dhcp_addr_pool":"10.0.1.50~10.0.1.150","dhcp_subnet":"255.0.0.0","dhcp_gateway":"10.0.1.1"}' | jq -C . || true
}

# Function: Test Delete HSI config
test_hsi_delete() {
    log_info "Testing DELETE /api/config/:nodeId/hsi/:userId (Delete HSI config)..."
    TOKEN=$(curl -s -k -X POST https://$ENDPOINT:8443/api/login -H "Content-Type: application/json" -d '{"username":"admin","password":"admin"}' | jq -r .token)
    curl -s -k -X DELETE -H "Authorization: $TOKEN" https://$ENDPOINT:8443/api/config/node1/hsi/1001 | jq -C . || true
}

# Function: Test HSI config with invalid data
test_hsi_validation() {
    log_info "Testing HSI config validation..."
    log_info "1. Testing invalid User ID (out of range)..."
    TOKEN=$(curl -s -k -X POST https://$ENDPOINT:8443/api/login -H "Content-Type: application/json" -d '{"username":"admin","password":"admin"}' | jq -r .token)
    curl -s -k -X POST -H "Authorization: $TOKEN" -H "Content-Type: application/json" \
        https://$ENDPOINT:8443/api/config/node1/hsi \
        -d '{"user_id":"2001","vlan_id":"100","account_name":"test@example.com","password":"testpass123","dhcp_addr_pool":"192.168.1.10~192.168.1.200","dhcp_subnet":"255.255.255.0","dhcp_gateway":"192.168.1.1"}' | jq -C . || true
    
    log_info "2. Testing invalid VLAN ID (out of range)..."
    TOKEN=$(curl -s -k -X POST https://$ENDPOINT:8443/api/login -H "Content-Type: application/json" -d '{"username":"admin","password":"admin"}' | jq -r .token)
    curl -s -k -X POST -H "Authorization: $TOKEN" -H "Content-Type: application/json" \
        https://$ENDPOINT:8443/api/config/node1/hsi \
        -d '{"user_id":"1500","vlan_id":"1","account_name":"test@example.com","password":"testpass123","dhcp_addr_pool":"192.168.1.10~192.168.1.200","dhcp_subnet":"255.255.255.0","dhcp_gateway":"192.168.1.1"}' | jq -C . || true
}

# Function: Test DHCP specific configurations
test_dhcp_configs() {
    log_info "Testing various DHCP configurations..."
    log_info "1. Testing Class A private network (10.x.x.x)..."
    TOKEN=$(curl -s -k -X POST https://$ENDPOINT:8443/api/login -H "Content-Type: application/json" -d '{"username":"admin","password":"admin"}' | jq -r .token)
    curl -s -k -X POST -H "Authorization: $TOKEN" -H "Content-Type: application/json" \
        https://$ENDPOINT:8443/api/config/node1/hsi \
        -d '{"user_id":"1002","vlan_id":"101","account_name":"test_class_a@example.com","password":"testpass123","dhcp_addr_pool":"10.0.1.2~10.0.1.254","dhcp_subnet":"255.0.0.0","dhcp_gateway":"10.0.1.1"}' | jq -C . || true
    
    log_info "2. Testing Class B private network (172.16.x.x)..."
    TOKEN=$(curl -s -k -X POST https://$ENDPOINT:8443/api/login -H "Content-Type: application/json" -d '{"username":"admin","password":"admin"}' | jq -r .token)
    curl -s -k -X POST -H "Authorization: $TOKEN" -H "Content-Type: application/json" \
        https://$ENDPOINT:8443/api/config/node1/hsi \
        -d '{"user_id":"1003","vlan_id":"102","account_name":"test_class_b@example.com","password":"testpass123","dhcp_addr_pool":"172.16.1.10~172.16.1.100","dhcp_subnet":"255.255.0.0","dhcp_gateway":"172.16.1.1"}' | jq -C . || true
    
    log_info "3. Testing Class C private network (192.168.x.x)..."
    TOKEN=$(curl -s -k -X POST https://$ENDPOINT:8443/api/login -H "Content-Type: application/json" -d '{"username":"admin","password":"admin"}' | jq -r .token)
    curl -s -k -X POST -H "Authorization: $TOKEN" -H "Content-Type: application/json" \
        https://$ENDPOINT:8443/api/config/node1/hsi \
        -d '{"user_id":"1004","vlan_id":"103","account_name":"test_class_c@example.com","password":"testpass123","dhcp_addr_pool":"192.168.100.50~192.168.100.150","dhcp_subnet":"255.255.255.0","dhcp_gateway":"192.168.100.1"}' | jq -C . || true
}

# Function: Test Complete HSI workflow
test_hsi_workflow() {
    log_info "Testing complete HSI workflow (PPPoE + DHCP)..."
    log_info "1. Create HSI config with DHCP settings..."
    test_hsi_create > /dev/null 2>&1
    
    log_info "2. List HSI users..."
    test_hsi_users
    
    log_info "3. Get specific HSI config..."
    test_hsi_get_config
    
    log_info "4. Update HSI config..."
    test_hsi_update > /dev/null 2>&1
    
    log_info "5. Test different DHCP configurations..."
    test_dhcp_configs > /dev/null 2>&1
    
    log_info "6. Test PPPoE dial with HSI config..."
    TOKEN=$(curl -s -k -X POST https://$ENDPOINT:8443/api/login -H "Content-Type: application/json" -d '{"username":"admin","password":"admin"}' | jq -r .token)
    curl -s -k -X POST -H "Authorization: $TOKEN" -H "Content-Type: application/json" \
        https://$ENDPOINT:8443/api/pppoe/dial \
        -d '{"node_id":"node1","user_id":"1001"}' | jq -C . || true
    
    log_info "7. Test PPPoE hangup..."
    TOKEN=$(curl -s -k -X POST https://$ENDPOINT:8443/api/login -H "Content-Type: application/json" -d '{"username":"admin","password":"admin"}' | jq -r .token)
    curl -s -k -X POST -H "Authorization: $TOKEN" -H "Content-Type: application/json" \
        https://$ENDPOINT:8443/api/pppoe/hangup \
        -d '{"node_id":"node1","user_id":"1001"}' | jq -C . || true
    
    log_info "8. Delete HSI configs..."
    test_hsi_delete > /dev/null 2>&1
    TOKEN=$(curl -s -k -X POST https://$ENDPOINT:8443/api/login -H "Content-Type: application/json" -d '{"username":"admin","password":"admin"}' | jq -r .token)
    curl -s -k -X DELETE -H "Authorization: $TOKEN" https://$ENDPOINT:8443/api/config/node1/hsi/1002 > /dev/null 2>&1 || true
    curl -s -k -X DELETE -H "Authorization: $TOKEN" https://$ENDPOINT:8443/api/config/node1/hsi/1003 > /dev/null 2>&1 || true
    curl -s -k -X DELETE -H "Authorization: $TOKEN" https://$ENDPOINT:8443/api/config/node1/hsi/1004 > /dev/null 2>&1 || true
    
    log_info "9. Verify deletion..."
    test_hsi_users
}

# Function: Test All HSI APIs
test_hsi_apis() {
    test_hsi_create
    test_hsi_users
    test_hsi_get_config
    test_hsi_update
    test_dhcp_configs
    test_hsi_validation
    test_autofill
    test_hsi_delete
}

# Function: Test Auto-fill feature for existing HSI configs
test_autofill() {
    log_info "Testing auto-fill feature..."
    log_info "1. First, create a test HSI config..."
    TOKEN=$(curl -s -k -X POST https://$ENDPOINT:8443/api/login -H "Content-Type: application/json" -d '{"username":"admin","password":"admin"}' | jq -r .token)
    curl -s -k -X POST -H "Authorization: $TOKEN" -H "Content-Type: application/json" \
        https://$ENDPOINT:8443/api/config/node1/hsi \
        -d '{"user_id":"1006","vlan_id":"106","account_name":"test-autofill2@example.com","password":"autofillpass","dhcp_addr_pool":"192.168.6.20~192.168.6.200","dhcp_subnet":"255.255.255.0","dhcp_gateway":"192.168.6.1"}' > /dev/null 2>&1
    
    log_info "2. Verify the config was created..."
    TOKEN=$(curl -s -k -X POST https://$ENDPOINT:8443/api/login -H "Content-Type: application/json" -d '{"username":"admin","password":"admin"}' | jq -r .token)
    curl -s -k -H "Authorization: $TOKEN" https://$ENDPOINT:8443/api/config/node1/hsi/1006 | jq -C .
    
    log_success "3. Test config created successfully!"
    log_info "4. Now open the web interface and try entering User ID '1006' in the '新增 PPPoE 設定' form."
    log_info "   The system should automatically detect the existing config and offer to auto-fill the fields."
    echo
    log_info "Web interface: https://$ENDPOINT:8443"
    log_info "Login: admin / secret"
    log_info "Navigate to any node -> HSI 設定管理 -> 新增 PPPoE 設定"
    log_info "Enter User ID: 1006 and wait 500ms for auto-fill detection"
}

# Function: Clean up test data
clean_test_data() {
    log_info "Cleaning up test HSI configs..."
    TOKEN=$(curl -s -k -X POST https://$ENDPOINT:8443/api/login -H "Content-Type: application/json" -d '{"username":"admin","password":"admin"}' | jq -r .token)
    for id in 1005 1006 2001; do
        curl -s -k -X DELETE -H "Authorization: $TOKEN" https://$ENDPOINT:8443/api/config/node1/hsi/$id > /dev/null 2>&1 || true
    done
    log_success "Test data cleaned"
}

# Function: Generate test certificates
generate_test_certs() {
    cd "$SCRIPT_DIR/.."
    make generate-dev-certs
}

# Function: Clean test certificates
clean_test_certs() {
    cd "$SCRIPT_DIR/.."
    make clean-dev-certs
}

run_feature_tests() {
    test_etcd_seed
    test_login
    test_nodes
    test_grpc
    test_redirect
    test_hsi_workflow
    test_hsi_apis
    test_unregister
    test_logout
}

# Function: Run all tests
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

# Show usage information
show_usage() {
    echo "FastRG Controller Test Script"
    echo "Usage: $0 [ENDPOINT_ADDRESS] [FUNCTION_NAME]"
    echo ""
    echo "Arguments:"
    echo "  ENDPOINT_ADDRESS    The IP address of the FastRG Controller (default: 127.0.0.1)"
    echo "  FUNCTION_NAME       Specific test function to run (optional)"
    echo ""
    echo "Available test functions:"
    echo "  start_backend       Start the backend service"
    echo "  stop_backend        Stop the backend service"
    echo "  test_etcd_seed      Seed etcd with test data"
    echo "  test_login          Test login functionality"
    echo "  test_nodes          Test nodes API"
    echo "  test_grpc           Test gRPC functionality"
    echo "  test_redirect       Test HTTP to HTTPS redirect"
    echo "  test_logout         Test logout functionality"
    echo "  test_hsi_*          Various HSI API tests"
    echo "  test_autofill       Test auto-fill feature"
    echo "  clean_test_data     Clean up test data"
    echo "  run_all_tests       Run complete test suite"
    echo ""
    echo "Examples:"
    echo "  $0                          # Run with default endpoint (127.0.0.1)"
    echo "  $0 192.168.1.100            # Run with custom endpoint"
    echo "  $0 192.168.1.100 test_login # Run specific test with custom endpoint"
}

# Main execution
if [ "$1" = "-h" ] || [ "$1" = "--help" ]; then
    show_usage
    exit 0
fi

# ---- CI failure diagnostics ----
on_error() {
    echo
    echo "==================== CI DIAGNOSTICS (on error) ===================="
    echo "Date: $(date)"
    echo "ENDPOINT: $ENDPOINT"
    echo "ETCD_ENDPOINTS: ${ETCD_ENDPOINTS:-unset}"
    echo "Environment summary:"
    env | grep -E 'GITHUB|CI|ETCD|ENDPOINT' || true
    echo
    echo "Which binaries:"
    for cmd in docker go make jq curl ss netstat openssl; do
      printf " - %-10s: " "$cmd"
      command -v "$cmd" >/dev/null 2>&1 && command -v "$cmd" || echo "NOT FOUND"
    done
    echo
    echo "Processes (controller / etcd / docker):"
    ps aux | egrep 'controller|etcd|dockerd|docker' || true
    echo
    echo "Check built binary:"
    ls -l "$SCRIPT_DIR/../bin" || true
    if [ -f "$SCRIPT_DIR/../bin/controller" ]; then
      echo "controller exists, ldd (if available):"
      ldd "$SCRIPT_DIR/../bin/controller" || echo "ldd not available"
    fi
    echo
    echo "backend.log (tail):"
    if [ -f "$SCRIPT_DIR/../backend.log" ]; then
      tail -200 "$SCRIPT_DIR/../backend.log" || true
    else
      echo "no backend.log found"
    fi
    echo
    echo "Open ports (ss/netstat):"
    if command -v ss >/dev/null 2>&1; then ss -lntp || true; elif command -v netstat >/dev/null 2>&1; then netstat -lntp || true; fi
    echo
    echo "Docker status (ps -a):"
    docker ps -a --no-trunc || true
    echo
    echo "etcd health (from CI runner):"
    curl -v --max-time 5 "http://127.0.0.1:2379/health" || true
    echo
    echo "HTTPS health (controller):"
    curl -vk --max-time 5 "https://$ENDPOINT:8443/api/health" || true
    echo "================================================================="
  }
trap on_error ERR

# If second argument is provided, run specific function
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
    # Run all tests by default
    log_info "Running complete test suite with endpoint: $ENDPOINT"
    run_all_tests
fi