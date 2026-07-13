package db

import (
	"context"
	"time"

	"github.com/jackc/pgx/v5"
)

// DLQRow represents a failed message in the dead letter queue
type DLQRow struct {
	ID           int64
	Topic        string
	Partition    int
	Offset       int64
	MessageValue []byte
	ErrorMessage string
	RetryCount   int
	Status       string // pending, processing, resolved
	CreatedAt    time.Time
	UpdatedAt    time.Time
}

// PPPoEStatusRow is the latest observed PPPoE state for a (node, user).
type PPPoEStatusRow struct {
	NodeUUID     string    `json:"node_uuid"`
	UserID       string    `json:"user_id"`
	Phase        string    `json:"phase"`
	HSIIPv4      string    `json:"hsi_ipv4,omitempty"`
	HSIIPv4GW    string    `json:"hsi_ipv4_gw,omitempty"`
	ErrorMessage string    `json:"error_message,omitempty"`
	EventTime    time.Time `json:"event_time"`
	UpdatedAt    time.Time `json:"updated_at"`
}

// NodeEventRow is one node event (config-apply result or runtime error).
type NodeEventRow struct {
	ID            int64     `json:"id"`
	NodeUUID      string    `json:"node_uuid"`
	UserID        string    `json:"user_id"`
	EventType     string    `json:"event_type"`
	Action        string    `json:"action,omitempty"`
	Success       *bool     `json:"success,omitempty"`
	Module        string    `json:"module,omitempty"`
	ErrorCode     string    `json:"error_code,omitempty"`
	ErrorMessage  string    `json:"error_message,omitempty"`
	Context       string    `json:"context,omitempty"`
	CorrelationID string    `json:"correlation_id,omitempty"`
	EventTime     time.Time `json:"event_time"`
}

// UpsertPPPoEStatus stores the latest PPPoE phase for a (node, user). The
// event_time guard keeps an older, out-of-order transition from clobbering a
// newer one (Kafka is at-least-once and only per-partition ordered).
func (d *DB) UpsertPPPoEStatus(ctx context.Context, row PPPoEStatusRow) error {
	_, err := d.pool.Exec(ctx, `
		INSERT INTO pppoe_status
			(node_uuid, user_id, phase, hsi_ipv4, hsi_ipv4_gw, error_message, event_time, updated_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, now())
		ON CONFLICT (node_uuid, user_id) DO UPDATE SET
			phase         = EXCLUDED.phase,
			hsi_ipv4      = EXCLUDED.hsi_ipv4,
			hsi_ipv4_gw   = EXCLUDED.hsi_ipv4_gw,
			error_message = EXCLUDED.error_message,
			event_time    = EXCLUDED.event_time,
			updated_at    = now()
		WHERE pppoe_status.event_time <= EXCLUDED.event_time`,
		row.NodeUUID, row.UserID, row.Phase, nullStr(row.HSIIPv4),
		nullStr(row.HSIIPv4GW), nullStr(row.ErrorMessage), row.EventTime,
	)
	return err
}

// GetPPPoEStatus returns the latest PPPoE state for a (node, user). ok is false
// when no event has been recorded yet.
func (d *DB) GetPPPoEStatus(ctx context.Context, nodeUUID, userID string) (row PPPoEStatusRow, ok bool, err error) {
	err = d.pool.QueryRow(ctx, `
		SELECT node_uuid, user_id, phase, COALESCE(hsi_ipv4,''), COALESCE(hsi_ipv4_gw,''),
		       COALESCE(error_message,''), event_time, updated_at
		FROM pppoe_status WHERE node_uuid = $1 AND user_id = $2`,
		nodeUUID, userID,
	).Scan(&row.NodeUUID, &row.UserID, &row.Phase, &row.HSIIPv4, &row.HSIIPv4GW,
		&row.ErrorMessage, &row.EventTime, &row.UpdatedAt)
	if err == pgx.ErrNoRows {
		return PPPoEStatusRow{}, false, nil
	}
	if err != nil {
		return PPPoEStatusRow{}, false, err
	}
	return row, true, nil
}

// SendToDLQ records a failed Kafka message for human investigation.
// Returns the DLQ row ID for logging.
func (d *DB) SendToDLQ(ctx context.Context, topic string, partition int, offset int64,
	messageValue []byte, errorMessage string) (int64, error) {
	var id int64
	err := d.pool.QueryRow(ctx, `
		INSERT INTO kafka_dlq
			(topic, partition, "offset", message_value, error_message, retry_count, status)
		VALUES ($1, $2, $3, $4, $5, 0, 'pending')
		ON CONFLICT (topic, partition, "offset") DO UPDATE SET
			error_message = EXCLUDED.error_message,
			retry_count = kafka_dlq.retry_count + 1,
			updated_at = now()
		RETURNING id`,
		topic, partition, offset, messageValue, errorMessage,
	).Scan(&id)
	return id, err
}

// InsertNodeEvent appends a node event, ignoring duplicates (same node, user,
// type, event_time and correlation_id) so at-least-once redelivery is
// idempotent. inserted is false when the event was a duplicate.
func (d *DB) InsertNodeEvent(ctx context.Context, row NodeEventRow) (inserted bool, err error) {
	tag, err := d.pool.Exec(ctx, `
		INSERT INTO node_events
			(node_uuid, user_id, event_type, action, success, module, error_code, error_message, context, correlation_id, event_time)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11)
		ON CONFLICT (node_uuid, user_id, event_type, event_time, correlation_id) DO NOTHING`,
		row.NodeUUID, row.UserID, row.EventType, nullStr(row.Action), row.Success,
		nullStr(row.Module), nullStr(row.ErrorCode), nullStr(row.ErrorMessage),
		nullStr(row.Context), row.CorrelationID, row.EventTime,
	)
	if err != nil {
		return false, err
	}
	return tag.RowsAffected() > 0, nil
}

// ListNodeEvents returns recent node events, optionally filtered by node and/or
// event_type, newest first, capped at limit.
func (d *DB) ListNodeEvents(ctx context.Context, nodeUUID, eventType string, limit int) ([]NodeEventRow, error) {
	if limit <= 0 || limit > 1000 {
		limit = 500
	}
	rows, err := d.pool.Query(ctx, `
		SELECT id, node_uuid, user_id, event_type, COALESCE(action,''), success,
		       COALESCE(module,''), COALESCE(error_code,''), COALESCE(error_message,''),
		       COALESCE(context,''), COALESCE(correlation_id,''), event_time
		FROM node_events
		WHERE ($1 = '' OR node_uuid = $1)
		  AND ($2 = '' OR event_type = $2)
		ORDER BY event_time DESC, id DESC
		LIMIT $3`,
		nodeUUID, eventType, limit,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []NodeEventRow
	for rows.Next() {
		var e NodeEventRow
		if err := rows.Scan(&e.ID, &e.NodeUUID, &e.UserID, &e.EventType, &e.Action,
			&e.Success, &e.Module, &e.ErrorCode, &e.ErrorMessage, &e.Context,
			&e.CorrelationID, &e.EventTime); err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

// DeleteNodeEvents deletes node events by id. Returns the number removed.
func (d *DB) DeleteNodeEvents(ctx context.Context, ids []int64) (int64, error) {
	if len(ids) == 0 {
		return 0, nil
	}
	tag, err := d.pool.Exec(ctx, `DELETE FROM node_events WHERE id = ANY($1)`, ids)
	if err != nil {
		return 0, err
	}
	return tag.RowsAffected(), nil
}

func nullStr(s string) interface{} {
	if s == "" {
		return nil
	}
	return s
}
