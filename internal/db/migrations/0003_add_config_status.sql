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
