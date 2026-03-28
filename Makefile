# =========== Main Help ==========
# Help for main Makefile
help:
	@echo "FastRG Controller Build System"
	@echo "=============================="
	@echo ""
	@echo "Build Targets:"
	@echo "  build               - Build both backend and frontend"
	@echo "  build-backend       - Build backend only"
	@echo "  build-frontend      - Build frontend only"
	@echo "  build-all          - Same as build"
	@echo "  clean              - Clean build artifacts"
	@echo ""
	@echo "Certificate Management:"
	@echo "  generate-dev-certs  - Generate self-signed certificates"
	@echo "  clean-dev-certs     - Remove certificates"
	@echo ""
	@echo "Node Management:"
	@echo "  create-node         - Create default test node"
	@echo "  create-node-custom  - Create custom node (see variables)"
	@echo "  create-multiple-nodes - Create multiple test nodes"
	@echo "  create-test-nodes   - Create all test nodes"
	@echo "  list-nodes-etcd     - List nodes from etcd"
	@echo "  list-nodes-api      - List nodes via REST API"
	@echo ""
	@echo "Testing:"
	@echo "  test               - Run complete test suite"
	@echo "  test-help          - Show detailed test help"
	@echo ""
	@echo "Docker & Container:"
	@echo "  docker-build       - Build Docker image"
	@echo "  docker-run         - Run with Docker Compose"
	@echo "  docker-stop        - Stop Docker Compose"
	@echo "  docker-clean       - Clean Docker resources"
	@echo ""
	@echo "Kubernetes & Helm:"
	@echo "  k8s-deploy         - Deploy using native K8s YAML"
	@echo "  k8s-delete         - Delete K8s resources"
	@echo "  helm-install       - Install using Helm chart"
	@echo "  helm-upgrade       - Upgrade Helm release"
	@echo "  helm-uninstall     - Uninstall Helm release"
	@echo ""
	@echo "For detailed test information: make test-help"

# =========== Testing ==========
# Test targets delegate to tools/Makefile
# Usage: make test, make test-login, make test-hsi-workflow, etc.

.PHONY: build build-backend build-frontend build-all clean create-node \
	create-node-custom create-multiple-nodes create-test-nodes \
	list-nodes-etcd list-nodes-api generate-test-certs clean-test-certs \
	test test-help help docker-build docker-run docker-stop docker-clean

# =========== Build targets ==========
build: build-all

build-backend:
	@echo "Building backend..."
	@echo "Downloading and generating protobuf code..."
	@cd proto && go generate ./... || true
	@echo "Generating Swagger docs..."
	swag init -g main.go --output docs
	go build -o bin/controller .

build-frontend:
	@echo "Building frontend (create web/build)..."
	cd web && npm ci && npm run build

build-all: build-backend build-frontend

clean: 
	@echo "Cleaning up..."
	@rm -rf bin web/build proto/*.pb.go proto/fastrgnodepb/*.pb.go

# =========== Certificate Management ==========
# Generate self-signed certificates for development
generate-dev-certs:
	@echo "Generating self-signed certificates..."
	@mkdir -p certs
	@openssl req -x509 -newkey rsa:4096 -keyout certs/server.key -out certs/server.crt \
		-days 365 -nodes -subj "/CN=localhost/O=FastRG Controller/C=TW" \
		-addext "subjectAltName=DNS:localhost,IP:127.0.0.1,IP:0.0.0.0"
	@chmod 600 certs/server.key
	@chmod 644 certs/server.crt
	@echo "Certificates generated in certs/ directory"

# Clean certificates
clean-dev-certs:
	@echo "Removing certificates..."
	@rm -rf certs/
	@echo "Certificates removed"

# =========== Node Management ==========
# Create a default test node
create-node:
	@echo "Creating default test node..."
	@go run tools/put_node/main.go

# Create a custom node with parameters
# Usage: make create-node-custom NODE_ID=mynode NODE_IP=192.168.1.50 NODE_TYPE=router NODE_VERSION=1.5.0 LOCATION="my-location" DESC="My custom node"
NODE_ID ?= custom-node
NODE_IP ?= 192.168.10.100  
NODE_TYPE ?= gateway
NODE_VERSION ?= 1.0.0
LOCATION ?= test-location
DESC ?= Custom node created via Makefile
create-node-custom:
	@echo "Creating custom node: $(NODE_ID)..."
	@go run tools/put_node/main.go "$(NODE_ID)" "$(NODE_IP)" "$(NODE_TYPE)" "$(NODE_VERSION)" "$(LOCATION)" "$(DESC)"

# Create multiple nodes for testing
create-multiple-nodes:
	@echo "Creating multiple test nodes..."
	@go run tools/put_node/main.go gateway1 192.168.1.1 gateway 1.0.0 "main-office" "Main office gateway"
	@go run tools/put_node/main.go router1 192.168.1.10 router 1.2.0 "server-room" "Core router"
	@go run tools/put_node/main.go switch1 192.168.1.20 switch 1.1.0 "network-closet" "Access switch"
	@go run tools/put_node/main.go gateway2 10.0.1.1 gateway 2.0.0 "branch-office" "Branch office gateway"

# Create nodes for testing environment
create-test-nodes: create-node create-multiple-nodes

# List all nodes via etcd
list-nodes-etcd:
	@echo "Listing all nodes from etcd..."
	@etcdctl get --prefix nodes/

# List all nodes via REST API (requires running server)
list-nodes-api:
	@echo "Listing nodes via REST API..."
	@TOKEN=$$(curl -s -k -X POST https://127.0.0.1:8443/api/login -H 'Content-Type: application/json' -d '{"username":"admin","password":"secret"}' | jq -r .token); \
	if [ -z "$$TOKEN" ] || [ "$$TOKEN" = "null" ]; then \
		echo "Authentication failed"; exit 1; \
	fi; \
	curl -s -k -H "Authorization: $$TOKEN" https://127.0.0.1:8443/api/nodes | jq -C '.'

# Certificate Management for Tests
generate-test-certs:
	@$(MAKE) generate-dev-certs

clean-test-certs:
	@$(MAKE) clean-dev-certs

# Main Test Target
test:
	go clean -testcache
	go test -count=1 -v ./internal/utils/
	@$(MAKE) -C tools test

# Test Help
test-help:
	@$(MAKE) -C tools help

# =========== Docker & Container Management ==========
# Build Docker image
docker-build:
	@echo "Building Docker image..."
	docker build -t fastrg-controller:latest .

# Run with Docker Compose (includes etcd)
docker-run:
	@echo "Starting services with Docker Compose..."
	docker-compose up -d
	@echo "Services started. Web interface will be available at:"
	@echo "https://localhost:8443 (after containers are ready)"
	@echo ""
	@echo "To check logs: docker-compose logs -f"
	@echo "To stop: make docker-stop"

# Stop Docker Compose services
docker-stop:
	@echo "Stopping Docker Compose services..."
	docker-compose down

# Clean Docker resources
docker-clean:
	@echo "Cleaning Docker resources..."
	docker-compose down -v
	docker system prune -f
	@echo "Docker resources cleaned"

# =========== Kubernetes & Helm Management ==========
# Deploy using native Kubernetes YAML
k8s-deploy:
	@echo "Deploying with native Kubernetes YAML..."
	deployment/k8s/deploy.sh --etcd-type internal -n fastrg-system --install-cilium 

# Delete Kubernetes resources
k8s-delete:
	@echo "Deleting Kubernetes resources..."
	deployment/k8s/undeploy.sh --etcd-type internal -n fastrg-system
	@echo "Resources deleted"

k8s-create-test-env:
	@echo "Deploying test environment with native Kubernetes YAML..."
	deployment/k8s/test-env.sh create --name fastrg-cluster

k8s-delete-test-env:
	@echo "Deleting test environment Kubernetes resources..."
	deployment/k8s/test-env.sh delete --name fastrg-cluster

# Install using Helm chart
helm-install:
	@echo "Installing FastRG Controller using Helm..."
	deployment/k8s/deploy.sh -n fastrg-system --cilium-only
	helm install fastrg-controller deployment/helm/fastrg-controller/
# To use external etcd, uncomment the following lines and comment the above line
#helm install fastrg-controller deployment/helm/fastrg-controller/ \
#        --set etcd.type=external \
#        --set etcd.external.endpoints[0].ip=192.168.10.12 \
#        --set etcd.external.endpoints[0].port=2379
	@echo "Installation complete. Check status with: helm status fastrg-controller"

# Upgrade Helm release
helm-upgrade:
	@echo "Upgrading FastRG Controller Helm release..."
	helm upgrade fastrg-controller deployment/helm/fastrg-controller/
	@echo "Upgrade complete"

# Uninstall Helm release
helm-uninstall:
	@echo "Uninstalling FastRG Controller Helm release..."
	helm uninstall fastrg-controller
	deployment/k8s/undeploy.sh -y --cilium-only
	@echo "Uninstallation complete"

# Helm template (dry run)
helm-template:
	@echo "Generating Kubernetes manifests from Helm chart..."
	helm template fastrg-controller deployment/helm/fastrg-controller/
