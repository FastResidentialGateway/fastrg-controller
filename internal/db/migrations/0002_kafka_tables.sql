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
