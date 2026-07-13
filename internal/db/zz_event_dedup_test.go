package db

import (
	"context"
	"fmt"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// TestNodeEventCorrelationDedup verifies the node_events idempotency key
// against a real PostgreSQL. It is skipped unless TEST_DATABASE_URL is set.
func TestNodeEventCorrelationDedup(t *testing.T) {
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("TEST_DATABASE_URL not set; skipping PostgreSQL integration test")
	}

	ctx := context.Background()
	scopedDSN, cleanup := createTestSchema(t, ctx, dsn, "event_dedup")
	defer cleanup()

	d, err := New(ctx, scopedDSN)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer d.Close()

	t0 := time.Now().UTC().Truncate(time.Second)

	t.Run("different correlation IDs are distinct", func(t *testing.T) {
		first := NodeEventRow{
			NodeUUID: "dedup-node", UserID: "1", EventType: "RUNTIME_ERROR",
			CorrelationID: "correlation-1", EventTime: t0,
		}
		second := first
		second.CorrelationID = "correlation-2"

		assertNodeEventInserted(t, ctx, d, first, true)
		assertNodeEventInserted(t, ctx, d, second, true)
		assertNodeEventCount(t, ctx, d, first, 2)
	})

	t.Run("same correlation ID is idempotent", func(t *testing.T) {
		row := NodeEventRow{
			NodeUUID: "dedup-node", UserID: "2", EventType: "CONFIG_APPLY_FAIL",
			CorrelationID: "correlation-3", EventTime: t0,
		}

		assertNodeEventInserted(t, ctx, d, row, true)
		assertNodeEventInserted(t, ctx, d, row, false)
		assertNodeEventCount(t, ctx, d, row, 1)
	})

	t.Run("empty correlation ID is idempotent", func(t *testing.T) {
		row := NodeEventRow{
			NodeUUID: "dedup-node", UserID: "3", EventType: "RUNTIME_ERROR",
			EventTime: t0,
		}

		assertNodeEventInserted(t, ctx, d, row, true)
		assertNodeEventInserted(t, ctx, d, row, false)
		assertNodeEventCount(t, ctx, d, row, 1)

		var correlationID string
		if err := d.pool.QueryRow(ctx, `
			SELECT correlation_id FROM node_events
			WHERE node_uuid = $1 AND user_id = $2 AND event_type = $3 AND event_time = $4`,
			row.NodeUUID, row.UserID, row.EventType, row.EventTime,
		).Scan(&correlationID); err != nil {
			t.Fatalf("select empty correlation_id: %v", err)
		}
		if correlationID != "" {
			t.Fatalf("correlation_id = %q, want empty string", correlationID)
		}
	})
}

// TestNodeEventCorrelationMigrationUpgrade starts from a schema where only
// 0001 is recorded and a legacy node_events row has a NULL correlation_id.
func TestNodeEventCorrelationMigrationUpgrade(t *testing.T) {
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("TEST_DATABASE_URL not set; skipping PostgreSQL integration test")
	}

	ctx := context.Background()
	scopedDSN, cleanup := createTestSchema(t, ctx, dsn, "event_migration")
	defer cleanup()

	setupPool, err := pgxpool.New(ctx, scopedDSN)
	if err != nil {
		t.Fatalf("connect setup pool: %v", err)
	}
	if _, err := setupPool.Exec(ctx,
		`CREATE TABLE schema_migrations (version TEXT PRIMARY KEY, applied_at TIMESTAMPTZ NOT NULL DEFAULT now())`,
	); err != nil {
		setupPool.Close()
		t.Fatalf("create schema_migrations: %v", err)
	}
	migration0001, err := migrationFS.ReadFile("migrations/0001_init_complete.sql")
	if err != nil {
		setupPool.Close()
		t.Fatalf("read 0001 migration: %v", err)
	}
	if _, err := setupPool.Exec(ctx, string(migration0001)); err != nil {
		setupPool.Close()
		t.Fatalf("apply 0001 migration: %v", err)
	}
	if _, err := setupPool.Exec(ctx,
		`INSERT INTO schema_migrations (version) VALUES ('0001_init_complete.sql')`,
	); err != nil {
		setupPool.Close()
		t.Fatalf("record 0001 migration: %v", err)
	}
	eventTime := time.Now().UTC().Truncate(time.Second)
	if _, err := setupPool.Exec(ctx, `
		INSERT INTO node_events (node_uuid, user_id, event_type, correlation_id, event_time)
		VALUES ('legacy-node', '1', 'RUNTIME_ERROR', NULL, $1)`, eventTime,
	); err != nil {
		setupPool.Close()
		t.Fatalf("insert legacy event: %v", err)
	}
	setupPool.Close()

	d, err := New(ctx, scopedDSN)
	if err != nil {
		t.Fatalf("New applying 0002: %v", err)
	}
	defer d.Close()

	var correlationID string
	if err := d.pool.QueryRow(ctx, `
		SELECT correlation_id FROM node_events
		WHERE node_uuid = 'legacy-node' AND user_id = '1'`,
	).Scan(&correlationID); err != nil {
		t.Fatalf("select migrated legacy event: %v", err)
	}
	if correlationID != "" {
		t.Fatalf("legacy correlation_id = %q, want empty string", correlationID)
	}

	var nullable, defaultValue string
	if err := d.pool.QueryRow(ctx, `
		SELECT is_nullable, column_default
		FROM information_schema.columns
		WHERE table_schema = current_schema()
		  AND table_name = 'node_events'
		  AND column_name = 'correlation_id'`,
	).Scan(&nullable, &defaultValue); err != nil {
		t.Fatalf("inspect correlation_id column: %v", err)
	}
	if nullable != "NO" || defaultValue != "''::text" {
		t.Fatalf("correlation_id schema = nullable %q default %q, want NO and empty text", nullable, defaultValue)
	}

	legacyEvents, err := d.ListNodeEvents(ctx, "legacy-node", "", 10)
	if err != nil || len(legacyEvents) != 1 || legacyEvents[0].CorrelationID != "" {
		t.Fatalf("ListNodeEvents legacy = %+v, %v; want one migrated event", legacyEvents, err)
	}
	newEvent := NodeEventRow{
		NodeUUID: "legacy-node", UserID: "1", EventType: "RUNTIME_ERROR",
		CorrelationID: "new-correlation", EventTime: eventTime,
	}
	assertNodeEventInserted(t, ctx, d, newEvent, true)
	assertNodeEventCount(t, ctx, d, newEvent, 2)
}

func assertNodeEventInserted(t *testing.T, ctx context.Context, d *DB, row NodeEventRow, want bool) {
	t.Helper()
	inserted, err := d.InsertNodeEvent(ctx, row)
	if err != nil || inserted != want {
		t.Fatalf("InsertNodeEvent = (%v, %v), want (%v, nil)", inserted, err, want)
	}
}

func assertNodeEventCount(t *testing.T, ctx context.Context, d *DB, row NodeEventRow, want int) {
	t.Helper()
	var count int
	if err := d.pool.QueryRow(ctx, `
		SELECT count(*) FROM node_events
		WHERE node_uuid = $1 AND user_id = $2 AND event_type = $3 AND event_time = $4`,
		row.NodeUUID, row.UserID, row.EventType, row.EventTime,
	).Scan(&count); err != nil {
		t.Fatalf("count node events: %v", err)
	}
	if count != want {
		t.Fatalf("node event count = %d, want %d", count, want)
	}
}

func createTestSchema(t *testing.T, ctx context.Context, dsn, prefix string) (string, func()) {
	t.Helper()
	parsed, err := url.Parse(dsn)
	if err != nil || parsed.Scheme == "" {
		t.Fatalf("TEST_DATABASE_URL must be a PostgreSQL URL: %v", err)
	}
	schema := fmt.Sprintf("task6_%s_%d", prefix, time.Now().UnixNano())

	adminPool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatalf("connect admin pool: %v", err)
	}
	quotedSchema := `"` + strings.ReplaceAll(schema, `"`, `""`) + `"`
	if _, err := adminPool.Exec(ctx, "CREATE SCHEMA "+quotedSchema); err != nil {
		adminPool.Close()
		t.Fatalf("create test schema: %v", err)
	}

	query := parsed.Query()
	query.Set("search_path", schema)
	parsed.RawQuery = query.Encode()

	cleanup := func() {
		defer adminPool.Close()
		if _, err := adminPool.Exec(context.Background(), "DROP SCHEMA "+quotedSchema+" CASCADE"); err != nil {
			t.Errorf("drop test schema: %v", err)
		}
	}
	return parsed.String(), cleanup
}
