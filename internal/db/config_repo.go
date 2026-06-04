package db

import (
	"context"

	"github.com/jackc/pgx/v5"
)

// UpsertCurrent writes the latest state for (node, user). The mod_revision guard
// makes it safe against out-of-order delivery: an older revision (e.g. replayed
// during reconcile) never overwrites a newer one already stored.
func (d *DB) UpsertCurrent(ctx context.Context, row HSIConfigRow) error {
	_, err := d.pool.Exec(ctx, `
		INSERT INTO hsi_config_current
			(node_uuid, user_id, config, desire_status, mod_revision, resource_version, updated_by, updated_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8)
		ON CONFLICT (node_uuid, user_id) DO UPDATE SET
			config           = EXCLUDED.config,
			desire_status    = EXCLUDED.desire_status,
			mod_revision     = EXCLUDED.mod_revision,
			resource_version = EXCLUDED.resource_version,
			updated_by       = EXCLUDED.updated_by,
			updated_at       = EXCLUDED.updated_at
		WHERE hsi_config_current.mod_revision < EXCLUDED.mod_revision`,
		row.NodeUUID, row.UserID, row.ConfigJSON, row.DesireStatus,
		row.ModRevision, row.ResourceVersion, row.UpdatedBy, row.UpdatedAt,
	)
	return err
}

// DeleteCurrent removes the current-state row for (node, user).
func (d *DB) DeleteCurrent(ctx context.Context, nodeUUID, userID string) error {
	_, err := d.pool.Exec(ctx,
		`DELETE FROM hsi_config_current WHERE node_uuid = $1 AND user_id = $2`,
		nodeUUID, userID,
	)
	return err
}

// ListCurrentKeys returns the keys of every current-state row, used by the
// projection to detect rows that no longer exist in etcd during reconcile.
func (d *DB) ListCurrentKeys(ctx context.Context) ([]ConfigKey, error) {
	rows, err := d.pool.Query(ctx, `SELECT node_uuid, user_id FROM hsi_config_current`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var keys []ConfigKey
	for rows.Next() {
		var k ConfigKey
		if err := rows.Scan(&k.NodeUUID, &k.UserID); err != nil {
			return nil, err
		}
		keys = append(keys, k)
	}
	return keys, rows.Err()
}

// AppendHistory appends one audit row.
func (d *DB) AppendHistory(ctx context.Context, row HSIConfigRow) error {
	_, err := d.pool.Exec(ctx, `
		INSERT INTO hsi_config_history
			(node_uuid, user_id, action, config, desire_status, mod_revision, resource_version, updated_by, updated_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9)`,
		row.NodeUUID, row.UserID, row.Action, row.ConfigJSON, row.DesireStatus,
		row.ModRevision, row.ResourceVersion, row.UpdatedBy, row.UpdatedAt,
	)
	return err
}

// GetWatchProgress returns the last applied revision for a watcher. ok is false
// when no checkpoint exists yet (fresh start).
func (d *DB) GetWatchProgress(ctx context.Context, watcher string) (rev int64, ok bool, err error) {
	err = d.pool.QueryRow(ctx,
		`SELECT last_revision FROM etcd_watch_progress WHERE watcher_name = $1`, watcher,
	).Scan(&rev)
	if err == pgx.ErrNoRows {
		return 0, false, nil
	}
	if err != nil {
		return 0, false, err
	}
	return rev, true, nil
}

// SetWatchProgress records the last revision a watcher has durably applied.
func (d *DB) SetWatchProgress(ctx context.Context, watcher string, rev int64) error {
	_, err := d.pool.Exec(ctx, `
		INSERT INTO etcd_watch_progress (watcher_name, last_revision)
		VALUES ($1, $2)
		ON CONFLICT (watcher_name) DO UPDATE SET last_revision = EXCLUDED.last_revision`,
		watcher, rev,
	)
	return err
}

// GetLastSuccessfulConfig finds the most recent history row with status='success'
// for the given (node, user). Returns nil if no successful version exists.
func (d *DB) GetLastSuccessfulConfig(ctx context.Context, nodeUUID, userID string) (*HSIConfigRow, error) {
	var row HSIConfigRow
	err := d.pool.QueryRow(ctx, `
		SELECT node_uuid, user_id, action, config, desire_status, mod_revision,
		       resource_version, updated_by, updated_at
		FROM hsi_config_history
		WHERE node_uuid = $1 AND user_id = $2 AND status = 'success'
		  AND action = 'upsert' AND config IS NOT NULL
		ORDER BY id DESC
		LIMIT 1
	`, nodeUUID, userID).Scan(
		&row.NodeUUID, &row.UserID, &row.Action, &row.ConfigJSON, &row.DesireStatus,
		&row.ModRevision, &row.ResourceVersion, &row.UpdatedBy, &row.UpdatedAt,
	)
	if err == pgx.ErrNoRows {
		return nil, nil // No successful version found
	}
	if err != nil {
		return nil, err
	}
	return &row, nil
}

// RollbackToLastSuccessful records a failed config apply attempt in history.
// hsi_config_current is now managed exclusively by CONFIG_APPLY_OK handlers,
// so this function only records the failure event, not restores to current.
func (d *DB) RollbackToLastSuccessful(ctx context.Context, nodeUUID, userID string, failureReason string) error {
	// Record the failed attempt in history
	_, err := d.pool.Exec(ctx, `
		INSERT INTO hsi_config_history
			(node_uuid, user_id, action, config, desire_status, mod_revision,
			 resource_version, updated_by, updated_at, status)
		VALUES ($1, $2, 'apply-failed', NULL, 'disconnect', 0, '', 'system', now(), 'failed')
	`, nodeUUID, userID)
	return err
}
