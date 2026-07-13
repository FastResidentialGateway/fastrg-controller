UPDATE node_events SET correlation_id = '' WHERE correlation_id IS NULL;

ALTER TABLE node_events ALTER COLUMN correlation_id SET DEFAULT '';
ALTER TABLE node_events ALTER COLUMN correlation_id SET NOT NULL;

ALTER TABLE node_events
    DROP CONSTRAINT IF EXISTS node_events_node_uuid_user_id_event_type_event_time_key;
ALTER TABLE node_events
    ADD CONSTRAINT node_events_dedup_key
    UNIQUE (node_uuid, user_id, event_type, event_time, correlation_id);
