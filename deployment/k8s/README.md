# FastRG Controller — HA Kubernetes deployment (plain YAML)

Production-oriented manifests: the cluster runs **only the stateless controller
(3 replicas)**; etcd, PostgreSQL and Kafka run **outside** the cluster.

For a local single-node demo (kind, all-internal, single replica, hostPort) use
[`../quickstart_k8s/`](../quickstart_k8s/) instead. The Helm chart in
[`../helm/fastrg-controller/`](../helm/fastrg-controller/) deploys the same HA
topology.

## Topology

```
k8s master/workers   ── 3× fastrg-controller (stateless, leader-elected workers)
                        external access via LoadBalancer VIP (8443/8080/8444/50051)
external data host   ── etcd (2379) + PostgreSQL (5432) + Kafka (9092)
```

The controller always dials the stable in-cluster names `etcd-endpoint`,
`postgresql-endpoint`, `kafka-endpoint`. Each is a headless `Service` + manual
`Endpoints` object pointing at the external host — so switching hosts only
touches `*-external.yml`, never the Deployment.

## Files

| File | Purpose |
|------|---------|
| `namespace.yml` | `fastrg-system` namespace |
| `rbac.yml` | ServiceAccount + Role (read-only, parity with Helm) + RoleBinding |
| `secret.yml` | shared `jwt-secret` + `database-url` (template — replace placeholders) |
| `etcd-external.yml` / `postgresql-external.yml` / `kafka-external.yml` | external backend Service+Endpoints |
| `controller.yml` | Deployment (3 replicas, no hostPort, anti-affinity, downward API) + ClusterIP service |
| `controller-loadbalancer.yml` | external LoadBalancer (provider-agnostic) |
| `controller-pdb.yml` | PodDisruptionBudget (`minAvailable: 2`) |
| `cilium-lb-pool.yml` | OPTIONAL Cilium LB-IPAM pool + L2 policy |
| `deploy.sh` / `undeploy.sh` | apply / remove in order |

## HA note — leader election

All 3 replicas serve REST/gRPC. The singleton background workers
(etcd→PostgreSQL projection, stale-node eviction, per-node stats scraping) run
**only on the elected leader**; the Kafka consumer runs on every replica (one
consumer group balances partitions). Leadership is elected via **etcd** (the
controller already depends on etcd), so no Kubernetes Lease / RBAC is involved;
`controller.yml` injects `POD_NAME` as the election identity.

## Before deploying

1. Set the controller image in `controller.yml` (a real registry, not the kind
   local image).
2. Point the three `*-external.yml` at your data host (or pass `--data-host IP`).
3. Set real secrets: edit `secret.yml` (`jwt-secret` + `database-url`) or
   pre-create the `fastrg-controller-secrets` Secret. `deploy.sh` seeds it from
   the placeholder values only if it does not already exist (and never
   overwrites an existing one). The `jwt-secret` **must be identical across all
   replicas** — that is the whole point of putting it in a shared Secret.
4. If using Cilium LB-IPAM, edit `cilium-lb-pool.yml` and deploy with
   `--cilium-pool`.

## Deploy

```bash
# defaults: namespace fastrg-system, external IPs as written in *-external.yml
deployment/k8s/deploy.sh

# override the external data host and pin the LoadBalancer VIP
deployment/k8s/deploy.sh --data-host 192.168.10.215 --lb-ip 192.168.10.240 --cilium-pool

# tear down (keeps the namespace; external backends untouched)
deployment/k8s/undeploy.sh
```
