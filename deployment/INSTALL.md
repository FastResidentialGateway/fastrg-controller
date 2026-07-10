# FastRG Controller — Full Deployment Runbook

Copy-paste runbook. Fill in the **variables** block once, then run the rest as-is.

Topology this guide assumes:

```
DATA VM  (1 host)            ── etcd (2379) + PostgreSQL (5432) + Kafka (9092)   [outside k8s]
K8S cluster                  ── 3× fastrg-controller (stateless, leader-elected)
   master is also a worker      external access via LoadBalancer VIP (Cilium LB-IPAM)
fastrg-node (gateways)       ── reach etcd:2379 + kafka:9092 on the DATA VM, and
                                the controller gRPC 50051 on the VIP
```

---

## 0. Variables — edit these, nothing else below should need changing

```bash
export DATA_HOST=192.168.10.50        # the VM running etcd + postgres + kafka
export LB_VIP=192.168.10.60           # a FREE IP in your LAN for the controller LoadBalancer
export PG_USER=fastrg
export PG_PASS=fastrg                 # change for production
export PG_DB=fastrg
export JWT_SECRET=$(openssl rand -base64 32)   # shared signing key (keep it secret)
export IMAGE=fastrg-controller:latest          # registry image, or local for kind
```

Open these ports on the DATA VM firewall to the k8s nodes **and** the fastrg-node subnet:
`2379` (etcd), `5432` (postgres), `9092` (kafka).

---

## 1. DATA VM — etcd + PostgreSQL + Kafka (Docker Compose)

On the **DATA VM**, create `backends.env` and `docker-compose.yml`:

`backends.env`:
```ini
DATA_HOST=192.168.10.50      # same as above
PG_USER=fastrg
PG_PASS=fastrg
PG_DB=fastrg
```

`docker-compose.yml`:
```yaml
services:
  etcd:
    image: quay.io/coreos/etcd:v3.5.17
    restart: unless-stopped
    command:
      - etcd
      - --name=etcd0
      - --advertise-client-urls=http://${DATA_HOST}:2379
      - --listen-client-urls=http://0.0.0.0:2379
      - --initial-cluster-state=new
    ports: ["2379:2379"]
    volumes: ["etcd-data:/var/run/etcd"]

  postgres:
    image: postgres:16-alpine
    restart: unless-stopped
    environment:
      POSTGRES_USER: ${PG_USER}
      POSTGRES_PASSWORD: ${PG_PASS}
      POSTGRES_DB: ${PG_DB}
    ports: ["5432:5432"]
    volumes: ["postgres-data:/var/lib/postgresql/data"]

  kafka:
    image: apache/kafka:3.9.0
    restart: unless-stopped
    ports: ["9092:9092"]
    environment:
      KAFKA_NODE_ID: 1
      KAFKA_PROCESS_ROLES: broker,controller
      KAFKA_LISTENERS: PLAINTEXT://:9092,CONTROLLER://:9093
      KAFKA_ADVERTISED_LISTENERS: PLAINTEXT://${DATA_HOST}:9092   # MUST be the VM IP, not localhost
      KAFKA_CONTROLLER_QUORUM_VOTERS: 1@localhost:9093
      KAFKA_CONTROLLER_LISTENER_NAMES: CONTROLLER
      KAFKA_LISTENER_SECURITY_PROTOCOL_MAP: PLAINTEXT:PLAINTEXT,CONTROLLER:PLAINTEXT
      KAFKA_INTER_BROKER_LISTENER_NAME: PLAINTEXT
      KAFKA_OFFSETS_TOPIC_REPLICATION_FACTOR: 1
      KAFKA_TRANSACTION_STATE_LOG_REPLICATION_FACTOR: 1
      KAFKA_TRANSACTION_STATE_LOG_MIN_ISR: 1
      KAFKA_AUTO_CREATE_TOPICS_ENABLE: "true"
    volumes: ["kafka-data:/var/lib/kafka/data"]

volumes:
  etcd-data:
  postgres-data:
  kafka-data:
```

```bash
docker compose --env-file backends.env up -d
docker compose ps          # all three Up
```

- PostgreSQL needs no schema setup — the controller auto-migrates on first start.
- Topic `fastrg.node.events` is auto-created (`KAFKA_AUTO_CREATE_TOPICS_ENABLE=true`).

> ⚠️ Three stateful services share one disk: etcd is fsync-sensitive — if it
> misbehaves under load, give etcd its own disk/volume. This single VM is also a
> single point of failure (fine for dev/test).

---

## 2. Kubernetes cluster (kubeadm + Cilium, master also a worker)

On the **k8s master** (skip kubeadm if you already have a cluster):

```bash
# Prereq: container runtime (containerd) + kubeadm/kubelet/kubectl installed.

# Init WITHOUT kube-proxy (Cilium replaces it). Use a pod CIDR that does not
# clash with your LAN or the DATA VM subnet.
sudo kubeadm init --pod-network-cidr=10.244.0.0/16 --skip-phases=addon/kube-proxy

mkdir -p $HOME/.kube && sudo cp /etc/kubernetes/admin.conf $HOME/.kube/config \
  && sudo chown $(id -u):$(id -g) $HOME/.kube/config

# Cilium CNI + kube-proxy replacement (needs cilium-cli).
API_IP=$(kubectl get node -o jsonpath='{.items[0].status.addresses[0].address}')
cilium install --set kubeProxyReplacement=true \
  --set k8sServiceHost=$API_IP --set k8sServicePort=6443
cilium status --wait

# Make the master schedulable as a worker (run controller pods on it).
kubectl taint nodes --all node-role.kubernetes.io/control-plane- 2>/dev/null || true
```

Join any extra workers with the `kubeadm join …` line printed by `kubeadm init`.

LoadBalancer IP pool (Cilium LB-IPAM) — give it a range that includes `LB_VIP`:

```bash
cat <<EOF | kubectl apply -f -
apiVersion: cilium.io/v2alpha1
kind: CiliumLoadBalancerIPPool
metadata: { name: fastrg-lb-pool }
spec:
  blocks:
  - start: "${LB_VIP}"
    stop:  "${LB_VIP}"
---
apiVersion: cilium.io/v2alpha1
kind: CiliumL2AnnouncementPolicy
metadata: { name: fastrg-l2 }
spec:
  loadBalancerIPs: true
  interfaces: ["^eth[0-9]+$"]   # adjust to your node NIC name
  nodeSelector: {}
EOF
```

---

## 3. Controller image

From a checkout of this repo:

```bash
make docker-build                       # builds fastrg-controller:latest

# Real cluster: push to a registry the nodes can pull from.
docker tag fastrg-controller:latest ${IMAGE}
docker push ${IMAGE}
```

(If a node can't pull `${IMAGE}`, load it onto each node directly, or use a private
registry + `imagePullSecrets`.)

---

## 4. Deploy the controller (Helm — HA, external backends)

From the repo root, with `kubectl` pointed at the cluster:

```bash
kubectl create namespace fastrg-system 2>/dev/null || true

# Shared JWT key + DB DSN as a Secret (NOT in the chart values / git).
kubectl create secret generic fastrg-controller-secrets -n fastrg-system \
  --from-literal=jwt-secret="${JWT_SECRET}" \
  --from-literal=database-url="postgres://${PG_USER}:${PG_PASS}@${DATA_HOST}:5432/${PG_DB}?sslmode=disable"

helm install fastrg-controller deployment/helm/fastrg-controller/ \
  -n fastrg-system \
  --set controller.image.repository="${IMAGE%%:*}" \
  --set controller.image.tag="${IMAGE##*:}" \
  --set controller.image.pullPolicy=IfNotPresent \
  --set controller.secrets.existingSecret=fastrg-controller-secrets \
  --set etcd.external.endpoints[0].ip="${DATA_HOST}" \
  --set kafka.external.brokers="${DATA_HOST}:9092" \
  --set global.lbIP="${LB_VIP}"
```

`postgresql.external.url` is read from the Secret above, so it is not passed here.
Defaults already set: 3 replicas, etcd/postgresql/kafka external, no hostPort,
leader election, PDB.

> Plain-YAML alternative (no Helm): edit `deployment/k8s/*-external.yml` + `secret.yml`,
> then `deployment/k8s/deploy.sh --data-host ${DATA_HOST} --lb-ip ${LB_VIP} --cilium-pool`.

---

## 5. Verify

```bash
kubectl get pods -n fastrg-system                 # 3× fastrg-controller Running
kubectl get svc  -n fastrg-system                 # LoadBalancer EXTERNAL-IP == LB_VIP
kubectl logs -n fastrg-system -l app=fastrg-controller | grep -i "acquired leadership"
                                                  # exactly ONE pod logs it
curl -k https://${LB_VIP}:8443/api/health         # 200
```

Web UI: `https://${LB_VIP}:8443` (self-signed cert → browser warning is expected).

---

## 6. Point fastrg-node at this deployment (cross-repo)

Each gateway's config must use:
- etcd: `${DATA_HOST}:2379`
- kafka: `${DATA_HOST}:9092`
- controller gRPC (register/heartbeat): `${LB_VIP}:50051`
