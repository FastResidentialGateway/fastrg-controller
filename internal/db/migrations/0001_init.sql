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
