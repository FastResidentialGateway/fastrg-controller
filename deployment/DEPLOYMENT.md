# FastRG Controller — Deployment Guide

This guide covers deploying the FastRG Controller and its backing
infrastructure after the control-plane refactor (etcd as SSOT, PostgreSQL as
the projection/history store, Kafka as the node→controller event pipeline).

You can run everything on a single host with Docker Compose, or wire each
dependency to in-cluster or managed services in Kubernetes. None of these need
a *separate* machine — they can all live on the same host/cluster.

---

## 1. Components and data flow

```
            ┌──────────── CLI / Web UI ───────────┐
            │            writes config            │
            ▼                                     │
        ┌────────┐   watch configs/   ┌────────────────────┐
 node ─▶│  etcd  │◀──────────────────▶│  fastrg-controller │
        └────────┘   (SSOT)           └────────────────────┘
            ▲                            │        ▲
            │ apply config               │ project│ consume
            │                            ▼        │
 node ──────┘                       ┌──────────┐  │  ┌──────────┐
   │  protobuf NodeEvent            │PostgreSQL│  └──│  Kafka   │◀── node
   └────────────────────────────────────────────────┘ events    producer
                                    (current/history,    (fastrg.node.events)
                                     pppoe_status,
                                     node_events)
```

| Component | Role | Required? |
|-----------|------|-----------|
| **etcd** | Configuration source of truth. Controller, nodes and CLI read/write here. | **Yes** |
| **PostgreSQL** | Controller's read/history projection: `hsi_config_current`, `hsi_config_history`, `pppoe_status`, `node_events`, `etcd_watch_progress`. | Optional* |
| **Kafka** | Node→controller event stream (config-apply results, runtime errors, PPPoE transitions) as **protobuf**. | Optional* |

\* The controller runs from etcd alone if neither is configured. PostgreSQL is
required for the node-events / PPPoE-status views and the config history; Kafka
additionally requires PostgreSQL (it writes there). Enable them by setting the
environment variables below.

---

## 2. Environment variables

| Variable | Default | Purpose |
|----------|---------|---------|
| `ETCD_ENDPOINTS` | `localhost:2379` | Comma-separated etcd endpoints. |
| `DATABASE_URL` | *(unset)* | PostgreSQL DSN, e.g. `postgres://user:pass@host:5432/fastrg?sslmode=disable`. When unset (and `POSTGRES_HOST` unset) the projection is disabled. |
| `POSTGRES_HOST` / `POSTGRES_PORT` / `POSTGRES_USER` / `POSTGRES_PASSWORD` / `POSTGRES_DB` / `POSTGRES_SSLMODE` | `-/5432/fastrg/fastrg/fastrg/disable` | Alternative to `DATABASE_URL`, assembled when `POSTGRES_HOST` is set. `DATABASE_URL` wins if both are present. |
| `KAFKA_BROKERS` | *(unset)* | Comma-separated brokers, e.g. `kafka:9092`. When unset the Kafka consumer is not started. Requires a configured database. |
| `KAFKA_TOPIC` | `fastrg.node.events` | Topic to consume node events from. |
| `KAFKA_GROUP` | `fastrg-controller` | Consumer group id. |
| `CERT_FILE` / `KEY_FILE` | `./certs/server.crt` / `.key` | TLS cert/key for the HTTPS listeners. |
| `JWT_SECRET` | random per boot | Signing secret for API tokens. **Set a stable value in production** (otherwise tokens are invalidated on restart). |
| `GRPC_PORT` / `HTTPS_PORT` / `HTTP_REDIRECT_PORT` / `LOG_HTTPS_PORT` | `50051` / `8443` / `8080` / `8444` | Listener ports. |
| `PROMETHEUS_LISTEN_IP` | `127.0.0.1` | Bind IP for the metrics server (port is fixed at `55688`). Set `0.0.0.0` in containers. |

Ports exposed by the controller: `8443` (REST + Web UI), `8080` (HTTP→HTTPS
redirect), `8444` (log file), `50051` (gRPC), `55688` (Prometheus metrics).

---

## 3. PostgreSQL

The controller **creates its own schema on startup** — embedded migrations run
automatically (`internal/db/migrations/*.sql`, tracked in a `schema_migrations`
table). You only need to provision an empty database and a user that can create
tables in it.

```sql
CREATE DATABASE fastrg;
CREATE USER fastrg WITH PASSWORD 'fastrg';
GRANT ALL PRIVILEGES ON DATABASE fastrg TO fastrg;
```

Then point the controller at it with `DATABASE_URL`. Any PostgreSQL 14+ works,
including managed services (RDS, Cloud SQL, Azure Database). Use `sslmode=require`
(or stronger) for managed/remote instances.

---

## 4. Kafka

- **Wire format is protobuf** — the message schema is `docs/contracts/kafka-events.proto`
  (`fastrg.events.v1.NodeEvent`). Producers (the FastRG node) must serialize with
  this exact schema; the controller drops messages it cannot decode.
- **Topic**: `fastrg.node.events` (override with `KAFKA_TOPIC`).
- **Partition key**: `node_uuid` — guarantees per-node ordering. Choose a
  partition count appropriate to your node fan-out (e.g. 6–12); more partitions
  allow more parallel consumers in the group.
- **Consumer group**: `fastrg-controller`. Delivery is **at-least-once**; the
  controller's DB writes are idempotent, so redelivery is safe.

Create the topic (if auto-create is disabled):

```bash
kafka-topics.sh --bootstrap-server kafka:9092 \
  --create --topic fastrg.node.events --partitions 6 --replication-factor 1
```

Any Kafka 3.x works (KRaft or ZooKeeper), as do managed services (MSK,
Confluent Cloud, Redpanda). For managed/secured brokers you will also need the
appropriate SASL/TLS client settings (not yet wired — see "Roadmap" below).

---

## 5. etcd

Unchanged from before the refactor: a standard etcd 3.5+ endpoint reachable by
the controller, the nodes and the CLI. For production use a 3- or 5-member
cluster and enable TLS + auth (the config — including PPPoE credentials — lives
here; see Security below).

---

## 6. Option A — Docker Compose (single host)

A ready-to-run stack (etcd + PostgreSQL + Kafka + controller) is provided in
[`docker-compose.yml`](../docker-compose.yml) at the repo root:

```bash
# from the repo root, on the target machine (needs Docker + network access)
docker compose up -d --build      # or: make docker-run

docker compose ps                 # check health
docker compose logs -f controller # watch startup
```

Then browse to `https://<host>:8443` (self-signed cert; accept the warning).

Stop / clean:

```bash
docker compose down               # keep volumes (data persists)
docker compose down -v            # also drop etcd/postgres/kafka data
```

The controller image self-generates a self-signed TLS cert at build time.
For real use, mount your own cert/key and set `CERT_FILE`/`KEY_FILE`, and set a
stable `JWT_SECRET`.

---

## 7. Option B — Kubernetes

The controller manifests are in `deployment/k8s/` (plain) and
`deployment/helm/fastrg-controller/` (Helm). They already include **optional**
`DATABASE_URL` / `KAFKA_*` env wiring — uncomment (k8s) or set values (Helm):

```yaml
# deployment/helm/fastrg-controller/values.yaml
controller:
  database:
    url: "postgres://fastrg:fastrg@postgres:5432/fastrg?sslmode=require"
  kafka:
    brokers: "kafka-bootstrap:9092"
    topic: "fastrg.node.events"
    group: "fastrg-controller"
```

You supply PostgreSQL and Kafka themselves via one of:

- **Managed services** (recommended for prod): point the env vars at RDS/Cloud SQL
  and MSK/Confluent Cloud. Nothing to deploy in-cluster.
- **In-cluster operators/charts**:
  - PostgreSQL: Bitnami `oci://registry-1.docker.io/bitnamicharts/postgresql`,
    or the CloudNativePG operator.
  - Kafka: the **Strimzi** operator (`Kafka` + `KafkaTopic` CRDs), or Bitnami Kafka chart.

The controller auto-migrates the database on startup, so no migration Job is
needed. Ensure the controller Deployment can reach both services (NetworkPolicy,
DNS names) before it starts.

---

## 8. Verifying a deployment

```bash
# 1. Controller is up (self-signed TLS)
curl -k https://<host>:8443/api/health

# 2. Metrics
curl http://<host>:55688/metrics | grep fastrg_

# 3. Config projection: create/update an HSI config via the API or CLI, then
#    confirm it landed in PostgreSQL.
psql "$DATABASE_URL" -c "SELECT node_uuid,user_id,desire_status,mod_revision FROM hsi_config_current;"
psql "$DATABASE_URL" -c "SELECT count(*) FROM hsi_config_history;"

# 4. Kafka pipeline: once nodes produce events, check the event tables.
psql "$DATABASE_URL" -c "SELECT node_uuid,user_id,phase,updated_at FROM pppoe_status;"
psql "$DATABASE_URL" -c "SELECT node_uuid,event_type,error_message,event_time FROM node_events ORDER BY event_time DESC LIMIT 20;"
```

Controller startup logs tell you which optional subsystems are active:
`Started config projection (etcd -> PostgreSQL)` and
`Started Kafka consumer for node events`, or the corresponding
"not set; running without …" lines.

---

## 9. Security (production checklist)

- **etcd**: enable TLS + RBAC. PPPoE credentials are stored in etcd config
  values; restrict access and consider encryption at rest.
- **PostgreSQL / Kafka**: use TLS (`sslmode=require`+) and authentication; keep
  them on a private network.
- **Controller**: set a stable `JWT_SECRET`; replace the self-signed cert with a
  real one via `CERT_FILE`/`KEY_FILE`.
- **Clocks**: PostgreSQL guards and the (future) offline-queue merge rely on
  reasonably synced clocks — run NTP on nodes and the controller host.

---

## 10. Roadmap / not yet wired

- Kafka **SASL/TLS** client options are not yet configurable via env (only plain
  `KAFKA_BROKERS`). Add them before using a secured/managed broker.
- The node-side **producer** (C / librdkafka) and the CLI three-tier write
  fallback are separate work items in the node repo.
