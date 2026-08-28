package db

import (
	"context"
	"os"
	"testing"
	"time"
)

// newPPPoEBatchDB opens a throwaway, migrated database for the batch tests.
func newPPPoEBatchDB(t *testing.T) (context.Context, *DB) {
	t.Helper()
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("TEST_DATABASE_URL not set; skipping PostgreSQL integration test")
	}

	ctx := context.Background()
	scopedDSN, cleanup := createIsolatedTestSchema(t, ctx, dsn, "pppoe_batch")
	d, err := New(ctx, scopedDSN)
	if err != nil {
		cleanup()
		t.Fatalf("New (connect + migrate): %v", err)
	}
	t.Cleanup(func() {
		d.Close()
		cleanup()
	})
	return ctx, d
}

func readPPPoEPhase(t *testing.T, ctx context.Context, d *DB, node, user string) (string, time.Time) {
	t.Helper()
	row, ok, err := d.GetPPPoEStatus(ctx, node, user)
	if err != nil {
		t.Fatalf("GetPPPoEStatus: %v", err)
	}
	if !ok {
		t.Fatalf("no pppoe_status row for %s/%s", node, user)
	}
	return row.Phase, row.EventTime
}

// TestUpsertPPPoEStatusBatchWritesEveryRow: the batch write is the multi-row
// form of the single write, so every subscriber in it lands.
func TestUpsertPPPoEStatusBatchWritesEveryRow(t *testing.T) {
	ctx, d := newPPPoEBatchDB(t)
	now := time.Now().UTC().Truncate(time.Second)

	rows := []PPPoEStatusRow{
		{NodeUUID: "batch-node", UserID: "1", Phase: "connected", HSIIPv4: "10.0.0.1", EventTime: now},
		{NodeUUID: "batch-node", UserID: "2", Phase: "disconnected", EventTime: now},
		{NodeUUID: "other-node", UserID: "1", Phase: "connecting", EventTime: now},
	}
	if err := d.UpsertPPPoEStatusBatch(ctx, rows); err != nil {
		t.Fatalf("UpsertPPPoEStatusBatch: %v", err)
	}

	for _, want := range rows {
		phase, _ := readPPPoEPhase(t, ctx, d, want.NodeUUID, want.UserID)
		if phase != want.Phase {
			t.Fatalf("%s/%s phase = %q, want %q", want.NodeUUID, want.UserID, phase, want.Phase)
		}
	}
}

// TestUpsertPPPoEStatusBatchKeepsTheEventTimeGuard: an out-of-order redelivery
// must not drag a row back to an older state, exactly as with the single-row
// write.
func TestUpsertPPPoEStatusBatchKeepsTheEventTimeGuard(t *testing.T) {
	ctx, d := newPPPoEBatchDB(t)
	now := time.Now().UTC().Truncate(time.Second)

	newer := PPPoEStatusRow{NodeUUID: "guard-node", UserID: "1", Phase: "connected", EventTime: now}
	if err := d.UpsertPPPoEStatusBatch(ctx, []PPPoEStatusRow{newer}); err != nil {
		t.Fatalf("UpsertPPPoEStatusBatch newer: %v", err)
	}

	older := PPPoEStatusRow{NodeUUID: "guard-node", UserID: "1", Phase: "disconnected", EventTime: now.Add(-time.Minute)}
	if err := d.UpsertPPPoEStatusBatch(ctx, []PPPoEStatusRow{older}); err != nil {
		t.Fatalf("UpsertPPPoEStatusBatch older: %v", err)
	}

	phase, eventTime := readPPPoEPhase(t, ctx, d, "guard-node", "1")
	if phase != "connected" {
		t.Fatalf("phase = %q, want %q — an older event must not overwrite", phase, "connected")
	}
	if !eventTime.Equal(now) {
		t.Fatalf("event_time = %s, want %s", eventTime, now)
	}
}

// TestUpsertPPPoEStatusBatchMatchesSingleWrite: batching must not change what a
// row ends up holding, including the empty fields stored as NULL.
func TestUpsertPPPoEStatusBatchMatchesSingleWrite(t *testing.T) {
	ctx, d := newPPPoEBatchDB(t)
	now := time.Now().UTC().Truncate(time.Second)

	single := PPPoEStatusRow{
		NodeUUID: "compare-node", UserID: "1", Phase: "connected",
		HSIIPv4: "10.0.0.1", HSIIPv4GW: "10.0.0.254",
		HSIIPv6: "2001:db8::1", HSIIPv6PDPrefix: "2001:db8:ab00::/56",
		HSIIPv6DNS: "2001:db8::53", EventTime: now,
	}
	batched := single
	batched.UserID = "2"

	if err := d.UpsertPPPoEStatus(ctx, single); err != nil {
		t.Fatalf("UpsertPPPoEStatus: %v", err)
	}
	if err := d.UpsertPPPoEStatusBatch(ctx, []PPPoEStatusRow{batched}); err != nil {
		t.Fatalf("UpsertPPPoEStatusBatch: %v", err)
	}

	singleRow, _, err := d.GetPPPoEStatus(ctx, "compare-node", "1")
	if err != nil {
		t.Fatalf("GetPPPoEStatus single: %v", err)
	}
	batchedRow, _, err := d.GetPPPoEStatus(ctx, "compare-node", "2")
	if err != nil {
		t.Fatalf("GetPPPoEStatus batched: %v", err)
	}

	singleRow.UserID = batchedRow.UserID
	singleRow.UpdatedAt = batchedRow.UpdatedAt
	if singleRow != batchedRow {
		t.Fatalf("batched row = %+v, want the same content as the single write %+v", batchedRow, singleRow)
	}
}

// TestUpsertPPPoEStatusBatchStoresEmptyFieldsAsNull: an unreported address is
// stored as NULL, the same as the single-row write does.
func TestUpsertPPPoEStatusBatchStoresEmptyFieldsAsNull(t *testing.T) {
	ctx, d := newPPPoEBatchDB(t)
	now := time.Now().UTC().Truncate(time.Second)

	err := d.UpsertPPPoEStatusBatch(ctx, []PPPoEStatusRow{
		{NodeUUID: "null-node", UserID: "1", Phase: "disconnected", EventTime: now},
	})
	if err != nil {
		t.Fatalf("UpsertPPPoEStatusBatch: %v", err)
	}

	var nullCount int
	err = d.pool.QueryRow(ctx, `
		SELECT count(*) FROM pppoe_status
		WHERE node_uuid = 'null-node' AND user_id = '1'
		  AND hsi_ipv4 IS NULL AND hsi_ipv4_gw IS NULL AND hsi_ipv6 IS NULL
		  AND hsi_ipv6_pd_prefix IS NULL AND hsi_ipv6_dns IS NULL
		  AND error_message IS NULL`).Scan(&nullCount)
	if err != nil {
		t.Fatalf("count null columns: %v", err)
	}
	if nullCount != 1 {
		t.Fatal("empty fields were not stored as NULL")
	}
}

// TestUpsertPPPoEStatusBatchRejectsRepeatedKeys pins down why callers have to
// collapse duplicates first: PostgreSQL refuses a statement whose conflict
// target repeats, so a batch carrying the same subscriber twice fails outright
// rather than silently keeping one of them.
func TestUpsertPPPoEStatusBatchRejectsRepeatedKeys(t *testing.T) {
	ctx, d := newPPPoEBatchDB(t)
	now := time.Now().UTC().Truncate(time.Second)

	err := d.UpsertPPPoEStatusBatch(ctx, []PPPoEStatusRow{
		{NodeUUID: "repeat-node", UserID: "1", Phase: "connecting", EventTime: now},
		{NodeUUID: "repeat-node", UserID: "1", Phase: "connected", EventTime: now},
	})
	if err == nil {
		t.Fatal("a batch repeating one subscriber was accepted; callers rely on this being rejected")
	}
}

// TestUpsertPPPoEStatusBatchIgnoresEmptyInput: an empty batch is a no-op, not a
// malformed statement.
func TestUpsertPPPoEStatusBatchIgnoresEmptyInput(t *testing.T) {
	ctx, d := newPPPoEBatchDB(t)
	if err := d.UpsertPPPoEStatusBatch(ctx, nil); err != nil {
		t.Fatalf("UpsertPPPoEStatusBatch(nil): %v", err)
	}
}
