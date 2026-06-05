-- Slice 5 (plan B-1): controller's PostgreSQL projection of etcd config state.
-- These tables are written ONLY by the config-watch projection (CQRS): REST and
-- gRPC handlers write etcd, never these tables directly.

-- Current-state table: one row per (node, user), upserted on every config change.
CREATE TABLE IF NOT EXISTS hsi_config_current (
    node_uuid        TEXT        NOT NULL,
    user_id          TEXT        NOT NULL,
    config           JSONB       NOT NULL,            -- full etcd value (config + metadata)
    desire_status    TEXT        NOT NULL DEFAULT 'disconnect',
    mod_revision     BIGINT      NOT NULL,            -- etcd ModRevision of this value
    resource_version TEXT,                            -- display-only audit counter
    updated_by       TEXT,
    updated_at       TIMESTAMPTZ,
    PRIMARY KEY (node_uuid, user_id)
);

-- History table: append-only audit log of every observed config change.
CREATE TABLE IF NOT EXISTS hsi_config_history (
    id               BIGSERIAL   PRIMARY KEY,
    node_uuid        TEXT        NOT NULL,
    user_id          TEXT        NOT NULL,
    action           TEXT        NOT NULL,            -- 'upsert' | 'delete'
    config           JSONB,                           -- null for delete
    desire_status    TEXT,
    mod_revision     BIGINT      NOT NULL,
    resource_version TEXT,
    updated_by       TEXT,
    updated_at       TIMESTAMPTZ,
    recorded_at      TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX IF NOT EXISTS idx_hsi_config_history_key
    ON hsi_config_history (node_uuid, user_id);

-- Watch checkpoint: last etcd revision the projection has durably applied, so a
-- restart resumes the watch from there instead of re-listing everything.
CREATE TABLE IF NOT EXISTS etcd_watch_progress (
    watcher_name  TEXT   PRIMARY KEY,
    last_revision BIGINT NOT NULL
);
-- Slice 6 (plan B-6). Tables fed by the Kafka consumer from node events,
-- replacing the etcd failed_events mechanism. Written only by internal/kafka.

-- Latest observed PPPoE state per (node, user), fed by PPPoE transition events.
CREATE TABLE IF NOT EXISTS pppoe_status (
    node_uuid     TEXT        NOT NULL,
    user_id       TEXT        NOT NULL,
    phase         TEXT        NOT NULL,        -- connecting|connected|disconnecting|disconnected
    hsi_ipv4      TEXT,
    hsi_ipv4_gw   TEXT,
    error_message TEXT,
    event_time    TIMESTAMPTZ NOT NULL,        -- producer event timestamp
    updated_at    TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (node_uuid, user_id)
);

-- Append-only node event log (config-apply results + runtime errors), replacing
-- etcd failed_events / failed_events_history. The UNIQUE key makes the
-- at-least-once Kafka delivery idempotent.
CREATE TABLE IF NOT EXISTS node_events (
    id             BIGSERIAL   PRIMARY KEY,
    node_uuid      TEXT        NOT NULL,
    user_id        TEXT        NOT NULL,
    event_type     TEXT        NOT NULL,       -- CONFIG_APPLY_OK|CONFIG_APPLY_FAIL|RUNTIME_ERROR
    action         TEXT,                        -- config apply: create|update|delete
    success        BOOLEAN,
    module         TEXT,                        -- runtime error source module
    error_code     TEXT,
    error_message  TEXT,
    context        TEXT,
    correlation_id TEXT,                        -- triggering etcd mod_revision
    event_time     TIMESTAMPTZ NOT NULL,
    recorded_at    TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (node_uuid, user_id, event_type, event_time)
);

CREATE INDEX IF NOT EXISTS idx_node_events_node ON node_events (node_uuid);
CREATE INDEX IF NOT EXISTS idx_node_events_time ON node_events (event_time DESC);
-- Slice 7B: add status field to config history for rollback support.
-- When CONFIG_APPLY_FAIL is reported from node, controller can restore
-- the last successful (status='success') config version.

ALTER TABLE IF EXISTS hsi_config_history
ADD COLUMN IF NOT EXISTS status TEXT DEFAULT 'success';

-- Ensure all existing rows are marked as success (they passed when first written).
UPDATE hsi_config_history SET status = 'success' WHERE status IS NULL;

-- Index for finding last successful version quickly.
CREATE INDEX IF NOT EXISTS idx_hsi_config_history_status_node_user
    ON hsi_config_history (node_uuid, user_id, status DESC, id DESC)
    WHERE status = 'success';
-- Slice 7B: add idempotency constraint to hsi_config_history.
-- When Kafka consumer retries after database restart, we must not duplicate
-- history entries. Use (node_uuid, user_id, mod_revision, status) as UNIQUE key.
--
-- This migration handles existing duplicate data by keeping only the latest
-- record for each (node, user, mod_revision, status) combination.

-- Step 1: Remove duplicate records (keep the most recent by ID)
-- This is safe because history is append-only; newer IDs = newer events
DELETE FROM hsi_config_history h1
WHERE id NOT IN (
  SELECT MAX(id) FROM hsi_config_history h2
  WHERE h1.node_uuid = h2.node_uuid
    AND h1.user_id = h2.user_id
    AND h1.mod_revision = h2.mod_revision
    AND h1.status = h2.status
  GROUP BY node_uuid, user_id, mod_revision, status
);

-- Step 2: Create unique index for idempotency
-- IF NOT EXISTS ensures this migration is idempotent (safe to re-run)
CREATE UNIQUE INDEX IF NOT EXISTS idx_hsi_config_history_idempotency
  ON hsi_config_history (node_uuid, user_id, mod_revision, status);
-- Slice 7B: persistent DLQ (Dead Letter Queue) for failed Kafka messages.
-- When a message fails to be processed after max retries, it's recorded here
-- for human investigation and potential replay.

CREATE TABLE IF NOT EXISTS kafka_dlq (
    id              BIGSERIAL   PRIMARY KEY,
    topic           TEXT        NOT NULL,
    partition       INT         NOT NULL,
    offset          BIGINT      NOT NULL,
    message_value   BYTEA       NOT NULL,  -- raw protobuf message
    error_message   TEXT        NOT NULL,
    retry_count     INT         NOT NULL DEFAULT 0,
    status          TEXT        NOT NULL DEFAULT 'pending',  -- pending | processing | resolved
    created_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at      TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX IF NOT EXISTS idx_kafka_dlq_status
    ON kafka_dlq (status, created_at DESC);

CREATE INDEX IF NOT EXISTS idx_kafka_dlq_topic_partition
    ON kafka_dlq (topic, partition, offset);
