#!/bin/bash

# FastRG Controller Kubernetes undeployment script

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

# Show usage information
show_usage() {
    echo "Usage: $0 [-n|--namespace NAMESPACE] [-e|--etcd-type TYPE] [-y|--yes] [--uninstall-cilium] [--cilium-only] [-h|--help]"
    echo ""
    echo "Options:"
    echo "  -n, --namespace NAMESPACE   Specify the Kubernetes namespace (default: default)"
    echo "  -e, --etcd-type TYPE        Specify etcd type: internal or external (default: internal)"
    echo "  -y, --yes                   Non-interactive mode, automatically delete ETCD and namespace"
    echo "      --uninstall-cilium      Also uninstall Cilium CNI after removing application"
    echo "      --cilium-only           Only uninstall Cilium CNI (no application removal)"
    echo "  -h, --help                  Show this help message"
    echo ""
    echo "Examples:"
    echo "  $0                          # Undeploy from default namespace with internal etcd (interactive)"
    echo "  $0 -n fastrg-system         # Undeploy from fastrg-system namespace with internal etcd (interactive)"
    echo "  $0 -e external              # Undeploy with external etcd (interactive)"
    echo "  $0 --uninstall-cilium       # Undeploy application and uninstall Cilium"
    echo "  $0 --cilium-only            # Only uninstall Cilium CNI"
    echo "  $0 -y --uninstall-cilium    # Non-interactive undeploy with Cilium uninstall"
    echo "  $0 -n fastrg-system -y     # Non-interactive undeploy from fastrg-system namespace"
}

# Parse command line arguments
parse_arguments() {
    NAMESPACE="default"
    ETCD_TYPE="internal"
    NON_INTERACTIVE=false
    UNINSTALL_CILIUM=false
    CILIUM_ONLY=false
    
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
            -y|--yes)
                NON_INTERACTIVE=true
                shift
                ;;
            --uninstall-cilium)
                UNINSTALL_CILIUM=true
                shift
                ;;
            --cilium-only)
                CILIUM_ONLY=true
                UNINSTALL_CILIUM=true  # Automatically enable Cilium uninstall
                shift
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

# Uninstall Cilium CNI
uninstall_cilium() {
    log_info "Uninstalling Cilium CNI..."
    
    # Check if Cilium is installed
    if ! kubectl get pods -n kube-system -l k8s-app=cilium --no-headers 2>/dev/null | grep -q cilium; then
        log_info "Cilium is not installed or already removed"
        return 0
    fi
    
    # Uninstall Cilium
    cilium uninstall --wait
    log_success "Cilium CNI uninstalled successfully"
}

# Undeploy function
undeploy() {
    # Parse command line arguments
    parse_arguments "$@"
    
    echo "=========================================="
    echo "  FastRG Controller K8s Undeployment Script"
    echo "=========================================="
    echo
    
    log_info "Target namespace: $NAMESPACE"
    log_info "ETCD type: $ETCD_TYPE"
    log_info "Uninstall Cilium: $UNINSTALL_CILIUM"
    if [ "$CILIUM_ONLY" = true ]; then
        log_info "Mode: Cilium-only uninstall"
    elif [ "$NON_INTERACTIVE" = true ]; then
        log_info "Mode: Non-interactive (will auto-delete ETCD and namespace)"
    else
        log_info "Mode: Interactive (will ask for confirmation)"
    fi
    echo

    SCRIPT_PATH="$(cd -P "$(dirname "${BASH_SOURCE[0]}")" >/dev/null 2>&1 && pwd)"

    # Handle Cilium-only mode
    if [ "$CILIUM_ONLY" = true ]; then
        uninstall_cilium
        echo
        echo "==================== Cilium Uninstall Complete ===================="
        echo
        log_success "Cilium CNI has been successfully uninstalled!"
        echo
        echo -e "${BLUE}Verify uninstallation:${NC}"
        echo "  Check pods: kubectl get pods -n kube-system -l k8s-app=cilium"
        echo "  Check nodes: kubectl get nodes"
        echo
        echo "=================================================="
        return 0
    fi

    # Delete LoadBalancer Service
    log_info "Deleting LoadBalancer Service..."
    kubectl delete service fastrg-controller-loadbalancer -n "$NAMESPACE" --ignore-not-found
    log_success "LoadBalancer Service deleted"

    # Delete application
    log_info "Deleting FastRG Controller..."
    # Create a temporary file with the correct namespace
    sed "s/namespace: default/namespace: $NAMESPACE/g" ${SCRIPT_PATH}/fastrg_controller.yml > /tmp/fastrg_controller-ns.yml
    kubectl delete -f /tmp/fastrg_controller-ns.yml --ignore-not-found
    rm -f /tmp/fastrg_controller-ns.yml
    log_success "FastRG Controller deleted"

    # Delete Cilium configuration
    log_info "Deleting Cilium configuration..."
    kubectl delete ciliuml2announcementpolicy fastrg-l2-policy --ignore-not-found
    kubectl delete ciliumloadbalancerippool fastrg-lb-pool --ignore-not-found
    log_success "Cilium configuration deleted"

    # Optional: Delete ETCD (ask user or use non-interactive mode)
    echo
    local delete_etcd=false
    
    if [ "$NON_INTERACTIVE" = true ]; then
        log_info "Non-interactive mode: automatically deleting ETCD"
        delete_etcd=true
    else
        read -p "Do you also want to delete ETCD? [y/N]: " -n 1 -r
        echo
        if [[ $REPLY =~ ^[Yy]$ ]]; then
            delete_etcd=true
        fi
    fi
    
    if [ "$delete_etcd" = true ]; then
        log_info "Deleting $ETCD_TYPE ETCD..."
        
        # Choose the appropriate ETCD configuration file
        local etcd_file
        if [ "$ETCD_TYPE" = "internal" ]; then
            etcd_file="${SCRIPT_PATH}/etcd-internal.yml"
        else
            etcd_file="${SCRIPT_PATH}/etcd-external.yml"
        fi
        
        # Create a temporary file with the correct namespace
        sed "s/namespace: default/namespace: $NAMESPACE/g" "$etcd_file" > /tmp/etcd-ns.yml
        kubectl delete -f /tmp/etcd-ns.yml --ignore-not-found
        rm -f /tmp/etcd-ns.yml
        log_success "ETCD deleted"
    else
        log_info "Keeping ETCD"
    fi
    
    # Delete namespace if it's not default and it's empty
    if [ "$NAMESPACE" != "default" ]; then
        echo
        local delete_namespace=false
        
        if [ "$NON_INTERACTIVE" = true ]; then
            log_info "Non-interactive mode: automatically deleting namespace $NAMESPACE"
            delete_namespace=true
        else
            read -p "Do you want to delete the namespace '$NAMESPACE'? [y/N]: " -n 1 -r
            echo
            if [[ $REPLY =~ ^[Yy]$ ]]; then
                delete_namespace=true
            fi
        fi
        
        if [ "$delete_namespace" = true ]; then
            log_info "Deleting namespace $NAMESPACE..."
            kubectl delete namespace "$NAMESPACE" --ignore-not-found
            log_success "Namespace $NAMESPACE deleted"
        else
            log_info "Keeping namespace $NAMESPACE"
        fi
    fi

    # Uninstall Cilium if requested
    if [ "$UNINSTALL_CILIUM" = true ]; then
        echo
        uninstall_cilium
    fi

    echo
    log_success "Undeployment completed!"
    echo
    log_info "You can run the following commands to check remaining resources:"
    echo "  kubectl get all"
    echo "  kubectl get pv,pvc"
    if [ "$UNINSTALL_CILIUM" = false ]; then
        echo "  kubectl get pods -n kube-system -l k8s-app=cilium  # Check Cilium pods (if installed)"
    fi
}

# Execute undeployment
undeploy "$@"
