#!/bin/bash

# FastRG Controller Kubernetes deployment script
# Use Cilium LoadBalancer to directly expose to host IP

set -e  # Exit immediately on error

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

# Get image tag based on git version
get_image_tag() {
    # Get the latest git tag
    local latest_tag=$(git describe --tags --abbrev=0 2>/dev/null || echo "")
    
    if [ -z "$latest_tag" ]; then
        # No tags found, use latest
        echo "latest"
        return
    fi
    
    # Check if current commit is ahead of the latest tag
    local commits_ahead=$(git rev-list --count "${latest_tag}..HEAD" 2>/dev/null || echo "0")
    
    if [ "$commits_ahead" -gt 0 ]; then
        # Current commit is ahead of latest tag, use latest
        echo "latest"
    else
        # Current commit matches a tag, use the tag (remove 'v' prefix if present)
        echo "$latest_tag" | sed 's/^v//'
    fi
}

# Show usage information
show_usage() {
    echo "Usage: $0 [-n|--namespace NAMESPACE] [-e|--etcd-type TYPE] [-c|--install-cilium] [--cilium-only] [--test-only] [-h|--help]"
    echo ""
    echo "Options:"
    echo "  -n, --namespace NAMESPACE   Specify the Kubernetes namespace (default: default)"
    echo "  -e, --etcd-type TYPE        Specify etcd type: internal or external (default: internal)"
    echo "  -c, --install-cilium        Install Cilium CNI during deployment"
    echo "      --cilium-only           Only install Cilium CNI and exit (no application deployment)"
    echo "      --test-only             Only run service connectivity tests"
    echo "  -h, --help                  Show this help message"
    echo ""
    echo "Examples:"
    echo "  $0                          # Deploy to default namespace with internal etcd"
    echo "  $0 -n fastrg-system         # Deploy to fastrg-system namespace with internal etcd"
    echo "  $0 -e external              # Deploy with external etcd"
    echo "  $0 -c                       # Deploy with Cilium installation"
    echo "  $0 --cilium-only            # Only install Cilium CNI"
    echo "  $0 --test-only              # Only test service connections"
    echo "  $0 -n my-ns -e internal -c  # Deploy to my-ns namespace with internal etcd and Cilium"
}

# Parse command line arguments
parse_arguments() {
    NAMESPACE="default"
    ETCD_TYPE="internal"
    INSTALL_CILIUM="false"
    CILIUM_ONLY="false"
    TEST_ONLY="false"
    
    while [[ $# -gt 0 ]]; do
        case $1 in
            -n|--namespace)
                NAMESPACE="$2"
                shift 2
                ;;
            -e|--etcd-type)
                ETCD_TYPE="$2"
                if [[ "$ETCD_TYPE" != "internal" && "$ETCD_TYPE" != "external" ]]; then
                    echo "Error: etcd-type must be 'internal' or 'external'"
                    show_usage
                    exit 1
                fi
                shift 2
                ;;
            -c|--install-cilium)
                INSTALL_CILIUM="true"
                shift 1
                ;;
            --cilium-only)
                CILIUM_ONLY="true"
                INSTALL_CILIUM="true"  # Automatically enable Cilium installation
                shift 1
                ;;
            --test-only)
                TEST_ONLY="true"
                shift 1
                ;;
            -h|--help)
                show_usage
                exit 0
                ;;
            *)
                echo "Unknown option: $1"
                show_usage
                exit 1
                ;;
        esac
    done
}

# Check dependencies
check_dependencies() {
    log_info "Checking dependencies..."
    
    if ! command -v kubectl &> /dev/null; then
        log_error "kubectl is not installed"
        exit 1
    fi
    
    if ! kubectl cluster-info &> /dev/null; then
        log_error "Unable to connect to Kubernetes cluster"
        exit 1
    fi
    
    if ! command -v git &> /dev/null; then
        log_error "git is not installed"
        exit 1
    fi
    
    log_success "Dependency check completed"
}

# Get host IP
get_host_ip() {
    # In Kind environment, must use the host's real IP, not the Docker internal IP
    local ip=$(ip route get 8.8.8.8 | awk '{print $7; exit}' 2>/dev/null)
    
    if [ -z "$ip" ]; then
        ip=$(hostname -I | awk '{print $1}')
    fi
    
    if [ -z "$ip" ]; then
        ip=$(ip addr show | grep 'inet ' | grep -v '127.0.0.1' | head -1 | awk '{print $2}' | cut -d/ -f1)
    fi
    
    echo $ip
}

# Stop potentially conflicting docker-proxy processes
stop_conflicting_proxies() {
    log_info "Checking port conflicts..."
    
    local pids=$(ps aux | grep docker-proxy | grep -E '8080|8443|50051' | awk '{print $2}' | grep -v grep || true)
    
    if [ ! -z "$pids" ]; then
        log_warning "Found conflicting docker-proxy processes, stopping..."
        echo $pids | xargs sudo kill 2>/dev/null || true
        sleep 2
        log_success "Stopped conflicting processes"
    fi
}

# Create namespace if it doesn't exist
create_namespace() {
    local namespace=$1
    
    if [ "$namespace" != "default" ]; then
        log_info "Creating namespace: $namespace"
        kubectl create namespace "$namespace" --dry-run=client -o yaml | kubectl apply -f -
        log_success "Namespace $namespace created or already exists"
    else
        log_info "Using default namespace"
    fi
}

# Deploy ETCD
deploy_etcd() {
    local namespace=$1
    log_info "Deploying ETCD ($ETCD_TYPE)..."
    
    # Choose the appropriate ETCD configuration file
    local etcd_file
    if [ "$ETCD_TYPE" = "internal" ]; then
        etcd_file="${SCRIPT_PATH}/etcd-internal.yml"
        log_info "Using internal ETCD deployment"
    else
        etcd_file="${SCRIPT_PATH}/etcd-external.yml"
        log_info "Using external ETCD service"
    fi
    
    # Create a temporary file with the correct namespace
    sed "s/namespace: default/namespace: $namespace/g" "$etcd_file" > /tmp/etcd-ns.yml
    kubectl apply -f /tmp/etcd-ns.yml
    
    # Wait for ETCD to be ready
    log_info "Waiting for ETCD to be ready..."
    if [ "$ETCD_TYPE" = "internal" ]; then
        # For internal ETCD, wait for the StatefulSet to be ready
        kubectl wait --for=condition=ready pod -l app=etcd -n "$namespace" --timeout=240s
    fi
    log_success "ETCD deployment completed"
}

# Wait for Cilium CRDs to be established
wait_for_cilium_crds() {
    log_info "Waiting for Cilium CRDs to be established..."
    
    local crds=(
        "ciliumloadbalancerippools.cilium.io"
        "ciliuml2announcementpolicies.cilium.io"
    )
    
    for crd in "${crds[@]}"; do
        local retry=30
        while [ $retry -gt 0 ]; do
            if kubectl wait --for=condition=Established --timeout=10s crd/$crd 2>/dev/null; then
                log_success "CRD $crd is established"
                break
            fi
            retry=$((retry - 1))
            if [ $retry -eq 0 ]; then
                log_error "Timeout waiting for CRD $crd to be established"
                return 1
            fi
            sleep 2
        done
    done

    log_success "All required Cilium CRDs are established"

    log_info "Verifying CRD API endpoints are accessible..."
    local api_retry=20
    while [ $api_retry -gt 0 ]; do
        if kubectl get ciliuml2announcementpolicies.cilium.io 2>/dev/null; then
            log_success "CRD API endpoints are accessible"
            return 0
        fi
        if [ $api_retry -eq 1 ]; then
            log_warning "CRD API endpoint probe timed out, continuing anyway..."
        fi
        sleep 3
        api_retry=$((api_retry - 1))
    done
}

# Retry kubectl apply with exponential backoff
retry_kubectl_apply() {
    local file=$1
    local description=$2
    local max_attempts=10
    local attempt=1
    local wait_time=2
    
    while [ $attempt -le $max_attempts ]; do
        log_info "Attempting to apply $description (attempt $attempt/$max_attempts)..."
        if kubectl apply -f "$file" 2>&1; then
            return 0
        fi
        
        if [ $attempt -lt $max_attempts ]; then
            log_warning "$description failed, retrying in ${wait_time}s..."
            sleep $wait_time
            wait_time=$((wait_time * 2))
            attempt=$((attempt + 1))
        else
            log_error "$description failed after $max_attempts attempts"
            return 1
        fi
    done
}

# Create Cilium LoadBalancer IP Pool
create_ip_pool() {
    local host_ip=$1
    log_info "Creating Cilium LoadBalancer IP Pool (IP: $host_ip)..."
    
    cat > /tmp/cilium-lb-pool.yml <<EOF
apiVersion: cilium.io/v2alpha1
kind: CiliumLoadBalancerIPPool
metadata:
  name: fastrg-lb-pool
spec:
  blocks:
  - cidr: $host_ip/32
  serviceSelector:
    matchLabels:
      app: fastrg-controller
EOF
    
    retry_kubectl_apply /tmp/cilium-lb-pool.yml "IP Pool creation"
    log_success "IP Pool created successfully"
}

# Create L2 Announcement Policy
create_l2_policy() {
    log_info "Creating L2 Announcement Policy..."
    
    # Dynamically detect network interface in Kind environment
    local interface=$(docker exec ${CLUSTER_NAME}-control-plane ip route | grep default | awk '{print $5}' | head -1)
    if [ -z "$interface" ]; then
        interface="eth0"  # Default interface inside Kind container
    fi
    
    cat > /tmp/cilium-l2-policy.yml <<EOF
apiVersion: cilium.io/v2alpha1
kind: CiliumL2AnnouncementPolicy
metadata:
  name: default
spec:
  serviceSelector:
    matchLabels:
      app: fastrg-controller
  nodeSelector: {}
  loadBalancerIPs: true
  externalIPs: true
  interfaces:
  - $interface
EOF
    
    retry_kubectl_apply /tmp/cilium-l2-policy.yml "L2 Policy creation"
    log_success "L2 Policy created successfully (interface: $interface)"
}

# Deploy application
deploy_application() {
    local namespace=$1
    log_info "Deploying FastRG Controller..."
    
    # Get image tag from git
    local image_tag=$(get_image_tag)
    log_info "Using image tag: $image_tag (git-based)"
    
    # Create a temporary file with the correct namespace and image tag
    sed -e "s/namespace: default/namespace: $namespace/g" \
        -e "s/image: fastrg-controller:latest/image: fastrg-controller:$image_tag/g" \
        ${SCRIPT_PATH}/fastrg_controller.yml > /tmp/fastrg_controller-ns.yml
    kubectl apply -f /tmp/fastrg_controller-ns.yml
    
    # Wait for Deployment to be ready
    log_info "Waiting for application to be ready..."
    kubectl wait --for=condition=available deployment/fastrg-controller -n "$namespace" --timeout=120s
    log_success "Application deployment completed"
}

# Create LoadBalancer Service
create_loadbalancer() {
    local namespace=$1
    log_info "Creating LoadBalancer Service..."
    
    cat > /tmp/loadbalancer-service.yml <<EOF
apiVersion: v1
kind: Service
metadata:
  name: fastrg-controller-loadbalancer
  namespace: $namespace
  labels:
    app: fastrg-controller
  annotations:
    service.cilium.io/lb-ipam-ips: "auto"
spec:
  type: LoadBalancer
  selector:
    app: fastrg-controller
  ports:
  - name: https
    port: 8443
    targetPort: 8443
    protocol: TCP
  - name: http
    port: 8080
    targetPort: 8080
    protocol: TCP
  - name: grpc
    port: 50051
    targetPort: 50051
    protocol: TCP
  - name: metrics
    port: 55688
    targetPort: 55688
    protocol: TCP
  - name: logging
    port: 8444
    targetPort: 8444
    protocol: TCP
EOF
    
    kubectl apply -f /tmp/loadbalancer-service.yml
    log_success "LoadBalancer Service created successfully"
}

# Wait for LoadBalancer to be ready
wait_for_loadbalancer() {
    local host_ip=$1
    local namespace=$2
    log_info "Waiting for LoadBalancer to get external IP..."
    
    local retry=30
    while [ $retry -gt 0 ]; do
        local external_ip=$(kubectl get service fastrg-controller-loadbalancer -n "$namespace" -o jsonpath='{.status.loadBalancer.ingress[0].ip}' 2>/dev/null || echo "")
        
        if [ "$external_ip" = "$host_ip" ]; then
            log_success "LoadBalancer got external IP: $external_ip"
            return 0
        fi
        
        echo -n "."
        sleep 2
        retry=$((retry - 1))
    done
    
    log_error "LoadBalancer failed to get expected external IP"
    return 1
}

# Test service connections
test_services() {
    local host_ip=$1
    local namespace=$2
    local test_failed=0
    log_info "Testing service connections..."
    
    # Test HTTPS
    if curl -k -s --max-time 10 https://$host_ip:8443/ > /dev/null; then
        log_success "HTTPS (8443) service is running"
    else
        log_warning "HTTPS (8443) service may not be ready"
        test_failed=1
    fi
    
    # Test HTTP
    if curl -s --max-time 10 http://$host_ip:8080/ > /dev/null; then
        log_success "HTTP (8080) service is running"
    else
        log_warning "HTTP (8080) service may not be ready"
        test_failed=1
    fi
    
    # Test gRPC port connectivity
    if nc -z -w5 $host_ip 50051 2>/dev/null; then
        log_success "gRPC (50051) port is reachable"
    else
        log_warning "gRPC (50051) port may not be ready"
        kubectl run test-pod --image=curlimages/curl:latest --rm -i --restart=Never -n "$2" -- curl -k -s --connect-timeout 5 fastrg-controller-service:8443/ && echo "Internal service is normal" || echo "Check internal service"
        test_failed=1
    fi
    
    return $test_failed
}

# Show access information
show_access_info() {
    local host_ip=$1
    echo
    echo "==================== Deployment Complete ===================="
    echo
    log_success "FastRG Controller has been successfully deployed!"
    echo
    echo -e "${BLUE}Access URLs:${NC}"
    echo -e "  HTTPS Web UI: ${GREEN}https://$host_ip:8443/${NC}"
    echo -e "  HTTP (redirect): ${GREEN}http://$host_ip:8080/${NC}"
    echo -e "  gRPC API:     ${GREEN}$host_ip:50051${NC}"
    echo
    echo -e "${BLUE}Management commands:${NC}"
    if [ "$NAMESPACE" != "default" ]; then
        echo "  View status: kubectl get all -n $NAMESPACE"
        echo "  View logs: kubectl logs -f deployment/fastrg-controller -n $NAMESPACE"
        echo "  Delete deployment: kubectl delete -f fastrg_controller.yml -n $NAMESPACE"
        echo "  Stop service: kubectl delete service fastrg-controller-loadbalancer -n $NAMESPACE"
    else
        echo "  View status: kubectl get all"
        echo "  View logs: kubectl logs -f deployment/fastrg-controller"
        echo "  Delete deployment: kubectl delete -f fastrg_controller.yml"
        echo "  Stop service: kubectl delete service fastrg-controller-loadbalancer"
    fi
    echo
    echo "=================================================="
}

# Clean up temporary files
cleanup() {
    rm -f /tmp/cilium-lb-pool.yml /tmp/cilium-l2-policy.yml /tmp/loadbalancer-service.yml /tmp/etcd-ns.yml /tmp/fastrg_controller-ns.yml
}

# Install and configure Cilium CNI
install_cilium() {
    log_info "Installing Cilium CNI..."
    
    # Check if Cilium is already installed
    if kubectl get pods -n kube-system -l k8s-app=cilium --no-headers 2>/dev/null | grep -q Running; then
        log_info "Cilium is already installed and running"
        return 0
    fi
    
    # Install Cilium and wait for readiness, with special configuration for Kind environment
    cilium install --version v1.18.2
    
    # Fix Cilium operator health check configuration
    log_info "Fixing Cilium operator configuration..."
    kubectl patch deployment -n kube-system cilium-operator --type='json' -p='[
        {
            "op": "replace", 
            "path": "/spec/template/spec/containers/0/livenessProbe", 
            "value": {
                "httpGet": {
                    "host": "0.0.0.0",
                    "path": "/healthz", 
                    "port": 9234,
                    "scheme": "HTTP"
                }, 
                "initialDelaySeconds": 60,
                "periodSeconds": 30,
                "timeoutSeconds": 5,
                "failureThreshold": 10
            }
        },
        {
            "op": "replace", 
            "path": "/spec/template/spec/containers/0/readinessProbe", 
            "value": {
                "httpGet": {
                    "host": "0.0.0.0",
                    "path": "/healthz", 
                    "port": 9234,
                    "scheme": "HTTP"
                }, 
                "initialDelaySeconds": 0,
                "periodSeconds": 15,
                "timeoutSeconds": 5,
                "failureThreshold": 5
            }
        }
    ]'

    # Wait for the patched operator deployment to roll out
    log_info "Waiting for Cilium operator to restart and be ready..."
    kubectl rollout status deployment/cilium-operator -n kube-system --timeout=180s || true

    # Wait for Cilium to restart and become ready
    log_info "Waiting for Cilium to restart and be ready..."
    sleep 10
    
    local retry=60
    while [ $retry -gt 0 ]; do
        local status_output=$(cilium status 2>/dev/null || echo "")
        if echo "$status_output" | grep -q "OK" && ! echo "$status_output" | grep -q "error"; then
            log_success "Cilium is ready"
            break
        fi
        
        # Check if pods are running
        local cilium_pods=$(kubectl get pods -n kube-system -l k8s-app=cilium --field-selector=status.phase=Running --no-headers | wc -l)
        if [ $cilium_pods -ge 1 ]; then
            log_success "Cilium agent pods are running"
            break
        fi
        
        echo -n "."
        sleep 5
        retry=$((retry - 1))
    done
    
    if [ $retry -eq 0 ]; then
        log_warning "Cilium wait timeout, but continuing deployment..."
        cilium status || true
    fi
}

# Main function
main() {
    # Parse command line arguments
    parse_arguments "$@"
    
    echo "=========================================="
    echo "  FastRG Controller K8s Deployment Script"
    echo "=========================================="
    echo
    
    log_info "Target namespace: $NAMESPACE"
    log_info "ETCD type: $ETCD_TYPE"
    log_info "Install Cilium: $INSTALL_CILIUM"
    if [ "$CILIUM_ONLY" = "true" ]; then
        log_info "Mode: Cilium-only installation"
    elif [ "$TEST_ONLY" = "true" ]; then
        log_info "Mode: Test-only (service connectivity tests)"
    fi
    echo

    CLUSTER_NAME="fastrg-cluster"
    DOCKER_IMAGE="fastrg-controller:latest"
    SCRIPT_PATH="$(cd -P "$(dirname "${BASH_SOURCE[0]}")" >/dev/null 2>&1 && pwd)"
    
    # If test-only mode, run tests and exit
    if [ "$TEST_ONLY" = "true" ]; then
        log_info "Running service connectivity tests..."
        
        # Get host IP
        local host_ip=$(get_host_ip)
        if [ -z "$host_ip" ]; then
            log_error "Unable to get host IP"
            exit 1
        fi
        log_info "Detected host IP: $host_ip"
        
        # Run tests
        if ! test_services $host_ip "$NAMESPACE"; then
            echo
            echo "==================== Test Results ===================="
            echo
            log_error "Service connectivity test failed!"
            echo
            echo -e "${BLUE}Test Details:${NC}"
            echo -e "  Target IP: ${GREEN}$host_ip${NC}"
            echo -e "  Namespace: ${GREEN}$NAMESPACE${NC}"
            echo -e "  HTTPS Test: https://$host_ip:8443/"
            echo -e "  HTTP Test:  http://$host_ip:8080/"
            echo -e "  gRPC Test:  $host_ip:50051"
            echo
            echo "=================================================="
            exit 2
        fi
        
        echo
        echo "==================== Test Results ===================="
        echo
        log_success "Service connectivity test completed!"
        echo
        echo -e "${BLUE}Test Details:${NC}"
        echo -e "  Target IP: ${GREEN}$host_ip${NC}"
        echo -e "  Namespace: ${GREEN}$NAMESPACE${NC}"
        echo -e "  HTTPS Test: https://$host_ip:8443/"
        echo -e "  HTTP Test:  http://$host_ip:8080/"
        echo -e "  gRPC Test:  $host_ip:50051"
        echo
        echo "=================================================="
        exit 0
    fi
    
    # Install Cilium if requested
    if [ "$INSTALL_CILIUM" = "true" ]; then
        install_cilium
        
        # If cilium-only mode, exit after installation
        if [ "$CILIUM_ONLY" = "true" ]; then
            log_success "Cilium-only installation completed!"
            echo
            echo "==================== Cilium Installation Complete ===================="
            echo
            log_success "Cilium CNI has been successfully installed!"
            echo
            echo -e "${BLUE}Verify installation:${NC}"
            echo "  Check status: cilium status"
            echo "  Check connectivity: cilium connectivity test"
            echo "  View pods: kubectl get pods -n kube-system -l k8s-app=cilium"
            echo
            echo "=================================================="
            exit 0
        fi
    else
        log_info "Skipping Cilium installation (use -c or --install-cilium to enable)"
    fi
    
    # Check dependencies
    check_dependencies
    # Create namespace if needed
    create_namespace "$NAMESPACE"

    # Get host IP
    local host_ip=$(get_host_ip)
    if [ -z "$host_ip" ]; then
        log_error "Unable to get host IP"
        exit 1
    fi
    log_info "Detected host IP: $host_ip"
    
    # Stop conflicting processes
    stop_conflicting_proxies
    
    # Deploy ETCD
    deploy_etcd "$NAMESPACE"
    
    # Wait for Cilium CRDs to be ready before applying resources
    wait_for_cilium_crds
    
    # Create Cilium configuration
    create_ip_pool $host_ip
    create_l2_policy
    
    # Deploy application
    deploy_application "$NAMESPACE"
    
    # Create LoadBalancer
    create_loadbalancer "$NAMESPACE"
    
    # Wait for LoadBalancer to be ready
    if wait_for_loadbalancer $host_ip "$NAMESPACE"; then
        # Test services
        test_services $host_ip "$NAMESPACE"
        
        # Show access information
        show_access_info $host_ip
    else
        log_error "LoadBalancer deployment failed"
        exit 1
    fi
    
    # Cleanup
    cleanup
    
    log_success "Deployment script execution completed!"
}

# Execute main function
main "$@"
