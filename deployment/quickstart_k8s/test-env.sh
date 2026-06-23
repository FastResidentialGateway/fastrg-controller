#!/bin/bash

# FastRG Controller Kind Test Environment Management Script
# Manages Kind cluster creation and deletion for testing

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

# Show usage information
show_usage() {
    echo "Usage: $0 <create|delete> [OPTIONS]"
    echo ""
    echo "Commands:"
    echo "  create    Create Kind cluster with Cilium CNI"
    echo "  delete    Delete Kind cluster"
    echo ""
    echo "Options:"
    echo "  -n, --name NAME      Specify cluster name (default: fastrg-cluster)"
    echo "  -i, --image IMAGE    Specify Docker image name (default: fastrg-controller)"
    echo "                       Tag is determined from git version (latest if ahead of tags)"
    echo "  -h, --help           Show this help message"
    echo ""
    echo "Examples:"
    echo "  $0 create                                    # Create cluster with defaults"
    echo "  $0 create -n my-cluster -i my-app          # Create with custom cluster and image name"
    echo "  $0 delete                                   # Delete default cluster"
    echo "  $0 delete -n my-cluster                     # Delete specific cluster"
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

# Parse command line arguments
parse_arguments() {
    CLUSTER_NAME="fastrg-cluster"
    DOCKER_IMAGE_NAME="fastrg-controller"
    DOCKER_IMAGE_TAG=""
    COMMAND=""
    
    if [[ $# -eq 0 ]]; then
        echo "Error: No command specified"
        show_usage
        exit 1
    fi
    
    COMMAND="$1"
    shift
    
    case "$COMMAND" in
        create|delete)
            ;;
        -h|--help)
            show_usage
            exit 0
            ;;
        *)
            echo "Error: Unknown command '$COMMAND'"
            show_usage
            exit 1
            ;;
    esac
    
    while [[ $# -gt 0 ]]; do
        case $1 in
            -n|--name)
                CLUSTER_NAME="$2"
                shift 2
                ;;
            -i|--image)
                DOCKER_IMAGE_NAME="$2"
                shift 2
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
    
    # Determine image tag from git if not specified
    if [ -z "$DOCKER_IMAGE_TAG" ]; then
        DOCKER_IMAGE_TAG=$(get_image_tag)
    fi
    
    # Construct full Docker image name
    DOCKER_IMAGE="${DOCKER_IMAGE_NAME}:${DOCKER_IMAGE_TAG}"
}

# Check dependencies
check_dependencies() {
    log_info "Checking dependencies..."
    
    if ! command -v kind &> /dev/null; then
        log_error "kind is not installed"
        exit 1
    fi
    
    if ! command -v kubectl &> /dev/null; then
        log_error "kubectl is not installed"
        exit 1
    fi
    
    if ! command -v cilium &> /dev/null; then
        log_error "cilium CLI is not installed"
        exit 1
    fi
    
    if ! command -v git &> /dev/null; then
        log_error "git is not installed"
        exit 1
    fi
    
    log_success "Dependency check completed"
}

# Create Kind cluster
create_cluster() {
    log_info "Creating Kind cluster: $CLUSTER_NAME"
    
    local script_path="$(cd -P "$(dirname "${BASH_SOURCE[0]}")" >/dev/null 2>&1 && pwd)"
    
    # Check if cluster already exists
    if kind get clusters | grep -q "^${CLUSTER_NAME}$"; then
        log_warning "Cluster $CLUSTER_NAME already exists"
        read -p "Do you want to delete and recreate it? [y/N]: " -n 1 -r
        echo
        if [[ $REPLY =~ ^[Yy]$ ]]; then
            log_info "Deleting existing cluster..."
            kind delete cluster --name "$CLUSTER_NAME"
        else
            log_info "Using existing cluster"
            return 0
        fi
    fi
    
    # Create cluster
    kind create cluster --config "${script_path}/kind-config.yml" --name "$CLUSTER_NAME"
    
    # Load Docker image if specified
    if [ -n "$DOCKER_IMAGE" ]; then
        # Get the repository root directory
        local repo_root="$(cd -P "$(dirname "${BASH_SOURCE[0]}")/../.." >/dev/null 2>&1 && pwd)"
        
        # Always rebuild the Docker image
        log_info "Building Docker image: $DOCKER_IMAGE"
        if (cd "$repo_root" && docker build -t "$DOCKER_IMAGE" .); then
            log_success "Docker image built successfully"
        else
            log_error "Failed to build Docker image"
            return 1
        fi
        
        # Load image into Kind cluster
        log_info "Loading Docker image into Kind cluster: $DOCKER_IMAGE"
        if kind load docker-image "$DOCKER_IMAGE" --name "$CLUSTER_NAME"; then
            log_success "Docker image loaded successfully"
        else
            log_error "Failed to load Docker image into Kind cluster"
            return 1
        fi
    fi
    
    log_success "Kind cluster $CLUSTER_NAME created successfully!"
    echo
    log_info "Cluster information:"
    kubectl cluster-info --context "kind-${CLUSTER_NAME}"
}

# Delete Kind cluster
delete_cluster() {
    log_info "Deleting Kind cluster: $CLUSTER_NAME"
    
    # Check if cluster exists
    if ! kind get clusters | grep -q "^${CLUSTER_NAME}$"; then
        log_warning "Cluster $CLUSTER_NAME does not exist"
        return 0
    fi
    
    # Delete cluster
    kind delete cluster --name "$CLUSTER_NAME"
    log_success "Kind cluster $CLUSTER_NAME deleted successfully!"
}

# Main function
main() {
    # Parse command line arguments
    parse_arguments "$@"
    
    echo "=========================================="
    echo "  Kind Test Environment Management Script"
    echo "=========================================="
    echo
    
    log_info "Command: $COMMAND"
    log_info "Cluster name: $CLUSTER_NAME"
    if [ "$COMMAND" = "create" ]; then
        log_info "Docker image name: $DOCKER_IMAGE_NAME"
        log_info "Docker image tag: $DOCKER_IMAGE_TAG (git-based)"
        log_info "Full Docker image: $DOCKER_IMAGE"
    fi
    echo
    
    # Check dependencies
    check_dependencies
    
    # Execute command
    case "$COMMAND" in
        create)
            create_cluster
            ;;
        delete)
            delete_cluster
            ;;
    esac
    
    log_success "Operation completed!"
}

# Execute main function
main "$@"