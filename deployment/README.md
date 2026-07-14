# FastRG Controller Kubernetes Deployment

This directory contains Kubernetes deployment configurations for FastRG
Controller. There are **three** ways to deploy, picked by environment:

| Path | Topology | Use it for |
|------|----------|-----------|
| [`quickstart_k8s/`](quickstart_k8s/) | single-node **kind**, all backends internal, **1 replica**, `hostPort` | local dev / demo / CI |
| [`k8s/`](k8s/) | **HA** plain YAML: **3 replicas**, no hostPort, etcd/PostgreSQL/Kafka **external** | production (raw manifests) |
| [`helm/fastrg-controller/`](helm/fastrg-controller/) | same **HA** topology as `k8s/`, packaged | production (Helm) |

The two HA paths run **only the stateless controller** in the cluster; etcd,
PostgreSQL and Kafka live outside it. Only the etcd-elected leader replica runs
the singleton background workers (projection / stale-node eviction / per-node
scraping); see [`k8s/README.md`](k8s/README.md).

## Directory Structure

```
deployment/
├── quickstart_k8s/                   # Kind quickstart (single replica, all-internal, hostPort)
│   ├── deploy.sh / undeploy.sh / test-env.sh
│   ├── kind-config.yml               # Kind cluster (extraPortMappings)
│   ├── {etcd,postgresql,kafka}-{internal,external}.yml
│   ├── ingress.yml                   # LoadBalancer (Cilium)
│   └── fastrg_controller.yml         # Controller Deployment + Service (1 replica)
├── k8s/                              # HA plain YAML (3 replicas, external backends)
│   ├── deploy.sh / undeploy.sh
│   ├── namespace.yml / rbac.yml
│   ├── controller.yml                # Deployment (3 replicas, no hostPort) + ClusterIP service
│   ├── controller-loadbalancer.yml   # External LoadBalancer
│   ├── controller-pdb.yml            # PodDisruptionBudget
│   ├── {etcd,postgresql,kafka}-external.yml   # external Service + Endpoints
│   ├── cilium-lb-pool.yml            # optional Cilium LB-IPAM
│   └── README.md
└── helm/                             # Helm Chart (HA defaults)
    └── fastrg-controller/
        ├── Chart.yaml / values.yaml
        └── templates/
            ├── _helpers.tpl / namespace.yaml / rbac.yaml
            ├── etcd.yaml / postgresql.yaml / kafka.yaml   # internal/external
            ├── controller.yaml       # Deployment (3 replicas) + Services
            └── extras.yaml           # PDB and HPA
```

## Component Overview

| Component | Role | Storage |
|-----------|------|---------|
| **fastrg-controller** | REST API, gRPC server, config projection | PVC (logs) |
| **etcd** | System source of truth — HSI configs, node registry | PVC (10 Gi) |
| **PostgreSQL** | CQRS read model — config history, PPPoE status, node events | PVC (10 Gi) |
| **Kafka** | Node-event stream (KRaft, single-broker) | PVC (20 Gi) |

## Port Reference

### Controller (external access via LoadBalancer at `HOST_IP`)

| Port | Protocol | Description |
|------|----------|-------------|
| `8443` | HTTPS | Web UI + REST API |
| `8080` | HTTP | → HTTPS redirect |
| `8444` | HTTPS | Log file viewer |
| `50051` | TCP | gRPC — fastrg-node RegisterNode / Heartbeat |
| `55688` | TCP | Prometheus metrics |

### etcd

| Port | Deployment mode | Description |
|------|-----------------|-------------|
| `2378` | internal — external access | `hostPort: 2378` on pod — used by fastrg-node to reach etcd running inside K8s |
| `2379` | external — external access | fastrg-node connects directly to the external etcd server (standard etcd port) |
| `2380` | internal only | Peer port for etcd cluster replication, not exposed externally |

### PostgreSQL

| Port | Description | Access method |
|------|-------------|---------------|
| `5432` | Client port (in-cluster) | ClusterIP Service `postgresql-endpoint` |

### Kafka

| Port | Description | Access method |
|------|-------------|---------------|
| `9092` | Broker port | ClusterIP `kafka-endpoint` (in-cluster) / `hostPort: 9092` (external) |
| `9093` | KRaft controller port (in-cluster only) | — |

## Quick Start

### Prerequisites

- Kind (for local dev) or a kubeadm cluster with Cilium CNI
- `kubectl`, `helm`

### Kind Development Environment

```bash
# 1. Create Kind cluster and build image
make k8s-create-test-env

# 2. Deploy all components (Cilium + etcd + PostgreSQL + Kafka + Controller)
make k8s-deploy

# 3. Run connectivity tests only (after deployment)
deployment/quickstart_k8s/deploy.sh --test-only -n fastrg-system
```

#### fastrg-node `config.cfg`

**etcd internal** (etcd running inside K8s)
```
ControllerAddress = "HOST_IP:50051"
EtcdEndpoints     = "HOST_IP:2378"
KafkaBrokers      = "HOST_IP:9092"
```

**etcd external** (etcd running outside K8s)
```
ControllerAddress = "HOST_IP:50051"
EtcdEndpoints     = "ETCD_HOST:2379"
KafkaBrokers      = "HOST_IP:9092"
```

### deploy.sh Options

```
Usage: deploy.sh [-n NAMESPACE] [-e etcd-type] [--postgresql-type TYPE]
                 [--kafka-type TYPE] [-c] [--cilium-only] [--test-only] [-h]

Options:
  -n, --namespace NAMESPACE       Kubernetes namespace (default: default)
  -e, --etcd-type TYPE            internal | external  (default: internal)
      --postgresql-type TYPE      internal | external  (default: internal)
      --kafka-type TYPE           internal | external  (default: internal)
  -c, --install-cilium            Install Cilium CNI
      --cilium-only               Install Cilium and exit
      --test-only                 Run connectivity tests only

Examples:
  ./deploy.sh -n fastrg-system -c           # Full deploy with Cilium
  ./deploy.sh --postgresql-type external    # Use external PostgreSQL
  ./deploy.sh --kafka-type external         # Use external Kafka
  ./deploy.sh --test-only -n fastrg-system  # Connectivity tests only
```

### Makefile Targets

```bash
make k8s-create-test-env   # Create Kind cluster + build Docker image
make k8s-delete-test-env   # Delete Kind cluster
make k8s-deploy            # Deploy to K8s (internal etcd/PG/Kafka + Cilium)
make k8s-delete            # Remove all K8s resources
make helm-install          # Install via Helm (Cilium-only first, then helm install)
```

### Helm (Recommended for Production)

```bash
# All internal services
helm install fastrg-controller deployment/helm/fastrg-controller/ \
  -n fastrg-system --create-namespace

# With external services
helm install fastrg-controller deployment/helm/fastrg-controller/ \
  -n fastrg-system --create-namespace \
  --set postgresql.type=external \
  --set postgresql.external.url="postgres://user:pass@host:5432/fastrg" \
  --set kafka.type=external \
  --set kafka.external.brokers="kafka1:9092,kafka2:9092" \
  --set etcd.type=external \
  --set etcd.external.endpoints[0].ip="192.168.10.12"
```

## Environment Variables (Controller)

| Variable | Default | Description |
|----------|---------|-------------|
| `ETCD_ENDPOINTS` | `etcd-endpoint:2379` | etcd client endpoints (comma-separated) |
| `DATABASE_URL` | *(empty)* | PostgreSQL DSN — enables CQRS projection when set |
| `KAFKA_BROKERS` | *(empty)* | Kafka brokers — enables event consumer when set |
| `KAFKA_TOPIC` | `fastrg.node.events` | Kafka topic for node events |
| `KAFKA_GROUP` | `fastrg-controller` | Kafka consumer group ID |
| `GRPC_PORT` | `50051` | gRPC server port |
| `HTTPS_PORT` | `8443` | HTTPS server port |
| `HTTP_REDIRECT_PORT` | `8080` | HTTP → HTTPS redirect port |
| `CERT_FILE` | `/app/certs/server.crt` | TLS certificate path |
| `KEY_FILE` | `/app/certs/server.key` | TLS key path |
| `PROMETHEUS_LISTEN_IP` | `127.0.0.1` | Prometheus metrics listen IP (`0.0.0.0` in K8s) |
| `JWT_SECRET` | *(shared in etcd)* | JWT signing secret — when unset, replicas use `auth/jwt_secret`; set a stable value to keep it out of etcd |

## Helm Values Reference

### PostgreSQL

```yaml
postgresql:
  type: internal          # internal | external | none
  internal:
    auth:
      username: fastrg
      password: fastrg    # Change in production
      database: fastrg
    persistence:
      size: 10Gi
  external:
    url: ""               # postgres://user:pass@host:5432/db?sslmode=disable
```

### Kafka

```yaml
kafka:
  type: internal          # internal | external | none
  topic: "fastrg.node.events"
  group: "fastrg-controller"
  internal:
    persistence:
      size: 20Gi
  external:
    brokers: ""           # host1:9092,host2:9092
```

### etcd

```yaml
etcd:
  type: internal          # internal | external
  internal:
    persistence:
      size: 10Gi
  external:
    service:
      type: ClusterIP     # ClusterIP or ExternalName
      port: 2379
    endpoints:
      - ip: "192.168.10.12"
```

## Connectivity Tests

`deploy.sh` runs these tests automatically after each deployment, and can be run standalone with `--test-only`:

| Test | Method | Expected |
|------|--------|---------|
| HTTPS `HOST_IP:8443` | `curl -k` | HTTP response |
| HTTP `HOST_IP:8080` | `curl` | HTTP response |
| gRPC `HOST_IP:50051` | `nc -z` | Port open |
| etcd `etcd-endpoint:2379` | `etcdctl endpoint health` (in-cluster pod) | healthy |
| PostgreSQL `postgresql-endpoint:5432` | `pg_isready` (in-cluster pod) | accepting connections |
| Kafka `kafka-endpoint:9092` | `kafka-topics.sh --list` (in-cluster pod) | topic list returned |

## Production Considerations

### Use Managed Services

| Component | Recommended managed alternative |
|-----------|--------------------------------|
| etcd | Dedicated 3-node cluster outside K8s |
| PostgreSQL | RDS, Cloud SQL, CloudNativePG operator |
| Kafka | MSK, Confluent Cloud, Redpanda Cloud |

### JWT Secret

When `JWT_SECRET` is unset, the controller creates and reuses a cluster-shared secret at `auth/jwt_secret` in etcd. Set a stable `JWT_SECRET` to keep the signing secret out of etcd.

### TLS

Auto-generated self-signed certificates work for dev. For production, provide certificates via `CERT_FILE` / `KEY_FILE` env vars or mount a Kubernetes Secret.

## Troubleshooting

```bash
# Pod status
kubectl get pods -n fastrg-system

# Controller logs
kubectl logs -f deployment/fastrg-controller -n fastrg-system

# etcd health
kubectl exec -n fastrg-system etcd-0 -- etcdctl endpoint health

# Services and LoadBalancer IPs
kubectl get svc -n fastrg-system

# Re-run connectivity tests
deployment/quickstart_k8s/deploy.sh --test-only -n fastrg-system

# etcd backup
kubectl exec -n fastrg-system etcd-0 -- \
  etcdctl snapshot save /etcd-data/backup.db
kubectl cp fastrg-system/etcd-0:/etcd-data/backup.db \
  ./etcd-backup-$(date +%Y%m%d).db
```

## Cleanup

```bash
# Helm
helm uninstall fastrg-controller -n fastrg-system

# Native YAML
make k8s-delete

# Kind cluster
make k8s-delete-test-env

# Remove PVCs (destroys data)
kubectl delete pvc -n fastrg-system --all
```
