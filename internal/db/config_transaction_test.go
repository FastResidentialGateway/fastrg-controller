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

// TestUpsertCurrentWithHistory verifies the CONFIG_APPLY_OK transaction against
// a real PostgreSQL. It is skipped unless TEST_DATABASE_URL is set.
func TestUpsertCurrentWithHistory(t *testing.T) {
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("TEST_DATABASE_URL not set; skipping PostgreSQL integration test")
	}

	ctx := context.Background()
	scopedDSN, cleanup := createTask8TestSchema(t, ctx, dsn)
	defer cleanup()

	d, err := New(ctx, scopedDSN)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer d.Close()

	now := time.Now().UTC().Truncate(time.Second)
	row := HSIConfigRow{
		NodeUUID:        "txn-history-node",
		UserID:          "42",
		Action:          ActionUpsert,
		ConfigJSON:      []byte(`{"version":2}`),
		DesireStatus:    "connect",
		ModRevision:     20,
		ResourceVersion: "2",
		UpdatedBy:       "node",
		UpdatedAt:       &now,
	}

	if err := d.UpsertCurrentWithHistory(ctx, row, "success"); err != nil {
		t.Fatalf("UpsertCurrentWithHistory: %v", err)
	}
	assertTask8Current(t, ctx, d, row.NodeUUID, row.UserID, 20, "connect")
	assertTask8HistoryCount(t, ctx, d, row.NodeUUID, row.UserID, 20, "success", 1)

	// Replaying the same Kafka event must not change current or duplicate history.
	if err := d.UpsertCurrentWithHistory(ctx, row, "success"); err != nil {
		t.Fatalf("UpsertCurrentWithHistory replay: %v", err)
	}
	assertTask8Current(t, ctx, d, row.NodeUUID, row.UserID, 20, "connect")
	assertTask8HistoryCount(t, ctx, d, row.NodeUUID, row.UserID, 20, "success", 1)

	// An older revision is still audited once, but the current row must not regress.
	stale := row
	stale.ConfigJSON = []byte(`{"version":1}`)
	stale.DesireStatus = "disconnect"
	stale.ModRevision = 10
	stale.ResourceVersion = "1"
	if err := d.UpsertCurrentWithHistory(ctx, stale, "success"); err != nil {
		t.Fatalf("UpsertCurrentWithHistory stale: %v", err)
	}
	assertTask8Current(t, ctx, d, row.NodeUUID, row.UserID, 20, "connect")
	assertTask8HistoryCount(t, ctx, d, row.NodeUUID, row.UserID, 10, "success", 1)

	if err := d.UpsertCurrentWithHistory(ctx, stale, "success"); err != nil {
		t.Fatalf("UpsertCurrentWithHistory stale replay: %v", err)
	}
	assertTask8HistoryCount(t, ctx, d, row.NodeUUID, row.UserID, 10, "success", 1)
}

func assertTask8Current(t *testing.T, ctx context.Context, d *DB, node, user string, wantRevision int64, wantDesire string) {
	t.Helper()
	var revision int64
	var desire string
	if err := d.pool.QueryRow(ctx, `
		SELECT mod_revision, desire_status
		FROM hsi_config_current
		WHERE node_uuid = $1 AND user_id = $2`, node, user,
	).Scan(&revision, &desire); err != nil {
		t.Fatalf("select current: %v", err)
	}
	if revision != wantRevision || desire != wantDesire {
		t.Fatalf("current = revision %d desire %q, want revision %d desire %q", revision, desire, wantRevision, wantDesire)
	}
}

func assertTask8HistoryCount(t *testing.T, ctx context.Context, d *DB, node, user string, revision int64, status string, want int) {
	t.Helper()
	var count int
	if err := d.pool.QueryRow(ctx, `
		SELECT count(*)
		FROM hsi_config_history
		WHERE node_uuid = $1 AND user_id = $2 AND mod_revision = $3 AND status = $4`,
		node, user, revision, status,
	).Scan(&count); err != nil {
		t.Fatalf("count history: %v", err)
	}
	if count != want {
		t.Fatalf("history count for revision %d status %q = %d, want %d", revision, status, count, want)
	}
}

func createTask8TestSchema(t *testing.T, ctx context.Context, dsn string) (string, func()) {
	t.Helper()
	parsed, err := url.Parse(dsn)
	if err != nil || parsed.Scheme == "" {
		t.Fatalf("TEST_DATABASE_URL must be a PostgreSQL URL: %v", err)
	}
	schema := fmt.Sprintf("txn_current_history_%d", time.Now().UnixNano())

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
