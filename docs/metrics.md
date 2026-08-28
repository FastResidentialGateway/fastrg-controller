# FastRG Controller — Metrics and Alerts

The controller exposes Prometheus metrics on port `55688`
(`PROMETHEUS_LISTEN_IP` selects the interface, default `127.0.0.1`):

```bash
curl http://<host>:55688/metrics | grep fastrg_
```

Besides the standard Go/process collectors, the controller exports the
metrics below. Everything here is observation only — the controller never
resets an offset or rewrites a table because a metric looks bad. The repair
is always the same operator action, described in the runbook at the end.

## Kafka consumer

| Metric | Type | Meaning |
|---|---|---|
| `fastrg_kafka_consumer_stall_seconds` | gauge | How long the consumer has been retrying the same message because PostgreSQL or etcd is unavailable. 0 when it is making progress. |
| `fastrg_kafka_consumer_infra_retries_total{source}` | counter | Message retries caused by unavailable infrastructure. `source` is `database` or `etcd`. |
| `fastrg_kafka_consumer_offset_beyond_log_end{partition}` | gauge | How many messages the consumer group's committed offset sits past the end of the partition's log. Above 0 means the broker dropped data the group had already acknowledged (a disk-full truncation, or a re-created topic). |
| `fastrg_kafka_consumer_offset_check_errors_total` | counter | Failed attempts to read the group's offsets from the brokers. Rising means the check above is blind, not that the consumer is healthy. |
| `fastrg_kafka_consumer_last_fetch_age_seconds` | gauge | Seconds since the consumer last fetched a message. |

The offset and fetch-age values are refreshed every 30 seconds while the
consumer runs.

## Database

| Metric | Type | Meaning |
|---|---|---|
| `fastrg_db_up` | gauge | 1 when the last PostgreSQL ping succeeded, 0 when it failed. Sampled every 30 seconds. |
| `fastrg_db_errors_total{op}` | counter | Failed database operations, labelled by the repository call that failed (`upsert_pppoe_status`, `insert_node_event`, `upsert_current_with_history`, …). |
| `fastrg_pppoe_status_rows{node_uuid}` | gauge | Rows in `pppoe_status` per node, sampled every 30 seconds. Compare it with the node's subscriber count. A node whose rows are all gone stops being reported at all, so alert on a drop *or* a disappearing series. |

## Config confirmation

| Metric | Type | Meaning |
|---|---|---|
| `fastrg_config_unconfirmed_nodes` | gauge | Active nodes holding at least one HSI config they have not confirmed applying — the controller pushed a config to etcd and never received the node's apply result. Sampled every 30 seconds by the confirmation sweep, and only on the leader replica. |

A short-lived non-zero value is normal: it covers the seconds between pushing a
config and the node reporting back. What matters is a value that stays up.
After a minute the sweep starts asking those nodes to restate what they are
running, at a backoff that doubles up to 15 minutes, so the number should fall
back to 0 on its own. One that does not is a node that cannot apply the config
or cannot reach Kafka.

## Suggested alerts

| Condition | What it means |
|---|---|
| `fastrg_kafka_consumer_offset_beyond_log_end > 0` | Kafka lost acknowledged events. The consumer is wedged and `pppoe_status` is drifting out of date. This is the primary alert. |
| `fastrg_db_up == 0` for 2 minutes | PostgreSQL is unreachable; nothing is being projected. |
| `rate(fastrg_db_errors_total[5m]) > 0` | Writes or queries are failing. The `op` label says which. |
| `fastrg_pppoe_status_rows` dropped sharply, or a node's series vanished | The table was emptied or rebuilt. |
| `fastrg_kafka_consumer_stall_seconds > 60` | The consumer has been stuck on one message for over a minute. |
| `fastrg_config_unconfirmed_nodes > 0` for 20 minutes | A node has stopped confirming the config it was pushed and the sweep's requests are not fixing it. Check that node's Kafka connectivity and its apply errors in `node_events`. |

`fastrg_kafka_consumer_last_fetch_age_seconds` is **not** an alert on its
own: it also grows on a quiet topic, where no events simply means nothing is
happening. Use it as a second opinion — a large fetch age *together with*
`fastrg_kafka_consumer_offset_check_errors_total` rising (so the offset
check cannot see the brokers either) points at a wedged consumer.

## Runbook: an alert fired

1. **Check Kafka.** Look at broker disk usage and the topic. A full disk
   truncates the log, which is what puts the committed offset past the log
   end. Free the disk before restarting anything, otherwise the same loss
   happens again.
2. **Check PostgreSQL.** `fastrg_db_up` and `fastrg_db_errors_total` say
   whether the database is reachable and which operation fails. Fix the
   database first; the controller retries writes on its own once it is back.
3. **Restart the controller.** This is the repair for both a truncated log
   and a rebuilt read model. On startup the controller:
   - snaps the consumer group's committed offsets back into the surviving
     log, so fetching resumes instead of waiting forever, and
   - asks every registered node to re-send the current PPPoE state and the
     config it is running for every subscriber as Kafka events, which refills
     DB tables `pppoe_status` and `hsi_config_current` through the normal 
     consumer path. Nodes are asked 16 at a time; an unresponsive 
     node costs only its own 10s timeout instead of stalling the rest of the sweep.
4. **Give it time to catch up.** The events all arrive at once and the
   consumer works through them in batches. Measured on the e2e-sized stack
   (one broker, one PostgreSQL, 8 cores), 100 nodes × 1000 subscribers —
   100,000 events — take about 15 seconds end to end, around 7,000 rows a
   second. If the rows are still climbing, it is working; if they have
   stopped short, `fastrg_kafka_consumer_stall_seconds` says whether it is
   stuck on one message.
5. **Confirm recovery.** `fastrg_kafka_consumer_offset_beyond_log_end` back
   to 0, `fastrg_pppoe_status_rows` back to the subscriber count per node,
   and `event_time` in `pppoe_status` advanced past the restart:

   ```bash
   psql "$DATABASE_URL" -c "SELECT node_uuid,user_id,phase,event_time FROM pppoe_status;"
   ```

A node too old to know the republish request answers "unimplemented"; the
controller logs a warning and carries on, so that node's rows only recover
when it next reports a state change on its own.
