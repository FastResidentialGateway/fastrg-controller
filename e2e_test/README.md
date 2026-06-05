# FastRG Controller E2E Tests

End-to-end recovery tests for the FastRG Controller stack: etcd, PostgreSQL,
Kafka, controller, and registered nodes.

The normal remote flow is:

```text
operator machine
  -> uploads scripts to runner host
  -> runner executes the phase scripts
  -> runner SSHes to controller host
  -> controller host runs docker-compose/docker compose commands
```

By default:

| Role | Default |
|------|---------|
| Runner host | `192.168.10.104` |
| Runner SSH port | `2222` |
| Controller host | `192.168.10.212` |
| etcd host | `192.168.10.212` |
| DB host | `192.168.10.212` |
| Node host | `192.168.10.211` |
| Compose directory on controller | `/root/fastrg-controller/e2e_test` |

## Architecture

```text
Runner host
  runs uploaded test scripts
  uses ssh_controller()
      |
      v
Controller host
  runs docker compose stack from COMPOSE_DIR
  services: etcd, postgres, kafka, controller

Node host
  optional real FastRG node registration source
```

The runner itself does not need Docker Compose for stack control. Compose
commands are executed on `CONTROLLER_HOST` through `ssh_controller`.

## Prerequisites

Operator machine:

- `bash`
- `ssh` and `scp`
- SSH key at `~/.ssh/id_ed25519` or `~/.ssh/id_rsa`, or pass `--ssh-key`

Runner host:

- SSH server reachable from the operator machine
- `bash`, `ssh`, `curl`, and `jq`
- SSH access from runner to the controller host

Controller host:

- Docker and either `docker-compose` or `docker compose`
- FastRG compose project at `COMPOSE_DIR`
- Stack built from the current code version before testing code changes

## Running

From this directory:

```bash
bash run_e2e_test.sh
```

Run a specific phase:

```bash
bash run_e2e_test.sh --phase 1
bash run_e2e_test.sh --phase 2
bash run_e2e_test.sh --phase 3
bash run_e2e_test.sh --phase 4
```

Override hosts and compose directory:

```bash
bash run_e2e_test.sh \
  --runner-host 192.168.10.104 \
  --runner-port 2222 \
  --controller-host 192.168.10.212 \
  --etcd-host 192.168.10.212 \
  --db-host 192.168.10.212 \
  --node-host 192.168.10.211 \
  --compose-dir /root/fastrg-controller/e2e_test
```

The runner uploads `run_e2e_test.sh`, `common.sh`, and `phases/*.sh` to
`~/fastrg_e2e`, runs them there, and cleans up afterward.

## Direct Phase Execution

You can run a phase directly from `e2e_test/`:

```bash
bash phases/phase_1_etcd_failure.sh
```

Direct phase execution uses local compose commands unless
`E2E_COMPOSE_VIA_SSH=1` and `ssh_controller` are provided by the runner.

## Test Phases

### Phase 1: etcd Failure & Recovery

Scenario: stop `etcd`, verify the controller remains up, restart `etcd`, and
verify the test config remains readable.

What it currently verifies:

- etcd starts healthy
- controller starts healthy
- test HSI config can be written to etcd
- etcd is actually inaccessible while stopped
- controller container remains up during etcd outage
- config value is still present after etcd restart

### Phase 2: Database Failure & Recovery

Scenario: stop PostgreSQL, produce a protobuf Kafka `NodeEvent`, restart
PostgreSQL, and verify the Kafka consumer processes the message after DB
recovery.

What it currently verifies:

- PostgreSQL and controller start healthy
- etcd-to-history projection writes to `hsi_config_history`
- PostgreSQL is actually inaccessible while stopped
- controller container remains up during DB outage
- Kafka topic `fastrg.node.events` exists
- a valid protobuf `PPPOE_CONNECTED` event can be produced while DB is down
- Kafka consumer keeps retrying the same message and does not commit it while DB
  is unavailable
- after PostgreSQL recovers, the event is written to `pppoe_status`
- DB outages do not create same-DB DLQ rows

Important: DB-unavailable errors are treated as transient and retried in the
consumer. DLQ is reserved for cases where PostgreSQL is reachable but a DB write
still fails with a SQL/table/constraint-level error.

### Phase 3: Controller Failure & Recovery

Scenario: stop the controller, mutate config directly in etcd, restart the
controller, and verify projection/history catches up.

What it currently verifies:

- controller, etcd, and PostgreSQL start healthy
- etcd and PostgreSQL remain usable while controller is stopped
- config can be changed in etcd during controller downtime
- config remains present after controller restart
- `hsi_config_history` count increases after recovery

### Phase 4: Node Failure & Recovery

Scenario: inspect currently registered nodes and verify controller/database
visibility. This phase does not currently create a real network partition.

What it currently verifies:

- controller starts healthy
- registered nodes exist under `nodes/` in etcd, or the phase skips with a warn
- selected node registration is readable
- `pppoe_status` table is queryable
- recovery expectations are documented in output

## Helpers

Common helpers are in `common.sh`:

- `compose ...`: run Docker Compose locally or through `ssh_controller`
- `compose_quiet ...`: run compose and suppress expected failure output
- `wait_for_service <service>`
- `is_service_up <service>`
- `stop_service <service>`
- `start_service <service>`
- `etcd_get <key>`
- `db_query <sql>`
- `config_history_count <node_id> <user_id>`
- `dlq_pending_count`
- `kafka_ensure_topic <topic>`
- `kafka_produce_base64 <topic> <base64_payload>`
- `api_get <endpoint>`
- `config_get <node_id> <user_id>`
- `pppoe_status <node_uuid> <user_id>`
- `kafka_lag`

## Troubleshooting

SSH to runner:

```bash
ssh -i ~/.ssh/id_ed25519 -o Port=2222 root@192.168.10.104 "echo OK"
```

SSH from runner to controller:

```bash
ssh -i ~/.ssh/id_ed25519 -o Port=2222 root@192.168.10.104 \
  "ssh -o StrictHostKeyChecking=no root@192.168.10.212 'echo OK'"
```

Check compose stack on controller:

```bash
ssh root@192.168.10.212 \
  "cd /root/fastrg-controller/e2e_test && docker-compose ps"
```

If the controller host uses Compose v2 only:

```bash
ssh root@192.168.10.212 \
  "cd /root/fastrg-controller/e2e_test && docker compose ps"
```

Check controller logs:

```bash
ssh root@192.168.10.212 \
  "cd /root/fastrg-controller/e2e_test && docker-compose logs -f controller"
```

Check database tables:

```bash
ssh root@192.168.10.212 \
  "cd /root/fastrg-controller/e2e_test && docker-compose exec -T postgres psql -U fastrg -d fastrg -c '\\dt'"
```

Check Kafka consumer group:

```bash
ssh root@192.168.10.212 \
  "cd /root/fastrg-controller/e2e_test && docker-compose exec -T kafka /opt/kafka/bin/kafka-consumer-groups.sh --bootstrap-server localhost:9092 --group fastrg-controller --describe"
```

## Known Limits

- The all-phases path continues after a failed phase and reports a warning.
- Phase 4 is a smoke check, not a real node network-partition test.
- Direct phase execution is useful for local debugging, but the main supported
  remote path is through `run_e2e_test.sh`.
- The controller image/container on the controller host must be rebuilt before
  testing code changes.

## Adding Phases

1. Add `phases/phase_N_description.sh`
2. Source `common.sh` like existing phases
3. Use helpers instead of direct `docker-compose` calls
4. Return non-zero on failure
5. Update this README and `QUICKSTART.md`
