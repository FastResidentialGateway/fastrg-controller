package db

import (
	"context"
	"os"
	"testing"
	"time"
)

// TestConfigRepo exercises migrations and the config repository against a real
// PostgreSQL. It is skipped unless TEST_DATABASE_URL points at a throwaway DB.
func TestConfigRepo(t *testing.T) {
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("TEST_DATABASE_URL not set; skipping PostgreSQL integration test")
	}

	ctx := context.Background()
	d, err := New(ctx, dsn)
	if err != nil {
		t.Fatalf("New (connect + migrate): %v", err)
	}
	defer d.Close()

	// Clean slate.
	for _, tbl := range []string{"hsi_config_current", "hsi_config_history", "etcd_watch_progress"} {
		if _, err := d.pool.Exec(ctx, "TRUNCATE "+tbl); err != nil {
			t.Fatalf("truncate %s: %v", tbl, err)
		}
	}

	now := time.Now().UTC().Truncate(time.Second)
	row := HSIConfigRow{
		NodeUUID:        "node1",
		UserID:          "2",
		ConfigJSON:      []byte(`{"config":{"user_id":"2","desire_status":"connect"},"metadata":{"resourceVersion":"3"}}`),
		DesireStatus:    "connect",
		ModRevision:     10,
		ResourceVersion: "3",
		UpdatedBy:       "admin",
		UpdatedAt:       &now,
	}

	if err := d.UpsertCurrent(ctx, row); err != nil {
		t.Fatalf("UpsertCurrent: %v", err)
	}

	// mod_revision guard: a stale (lower) revision must not overwrite.
	stale := row
	stale.ModRevision = 5
	stale.DesireStatus = "disconnect"
	if err := d.UpsertCurrent(ctx, stale); err != nil {
		t.Fatalf("UpsertCurrent stale: %v", err)
	}
	var gotDesire string
	var gotRev int64
	if err := d.pool.QueryRow(ctx,
		`SELECT desire_status, mod_revision FROM hsi_config_current WHERE node_uuid=$1 AND user_id=$2`,
		"node1", "2").Scan(&gotDesire, &gotRev); err != nil {
		t.Fatalf("select current: %v", err)
	}
	if gotDesire != "connect" || gotRev != 10 {
		t.Fatalf("stale write leaked: desire=%q rev=%d (want connect/10)", gotDesire, gotRev)
	}

	// A newer revision wins.
	newer := row
	newer.ModRevision = 20
	newer.DesireStatus = "disconnect"
	if err := d.UpsertCurrent(ctx, newer); err != nil {
		t.Fatalf("UpsertCurrent newer: %v", err)
	}
	if err := d.pool.QueryRow(ctx,
		`SELECT desire_status, mod_revision FROM hsi_config_current WHERE node_uuid=$1 AND user_id=$2`,
		"node1", "2").Scan(&gotDesire, &gotRev); err != nil {
		t.Fatalf("select current 2: %v", err)
	}
	if gotDesire != "disconnect" || gotRev != 20 {
		t.Fatalf("newer write lost: desire=%q rev=%d (want disconnect/20)", gotDesire, gotRev)
	}

	// History append.
	row.Action = ActionUpsert
	if err := d.AppendHistory(ctx, row); err != nil {
		t.Fatalf("AppendHistory: %v", err)
	}
	var histCount int
	if err := d.pool.QueryRow(ctx, `SELECT count(*) FROM hsi_config_history`).Scan(&histCount); err != nil {
		t.Fatalf("count history: %v", err)
	}
	if histCount != 1 {
		t.Fatalf("history count = %d, want 1", histCount)
	}

	// ListCurrentKeys.
	keys, err := d.ListCurrentKeys(ctx)
	if err != nil {
		t.Fatalf("ListCurrentKeys: %v", err)
	}
	if len(keys) != 1 || keys[0] != (ConfigKey{NodeUUID: "node1", UserID: "2"}) {
		t.Fatalf("ListCurrentKeys = %+v, want [{node1 2}]", keys)
	}

	// Watch progress round-trip.
	if _, ok, _ := d.GetWatchProgress(ctx, "configs"); ok {
		t.Fatal("expected no checkpoint initially")
	}
	if err := d.SetWatchProgress(ctx, "configs", 42); err != nil {
		t.Fatalf("SetWatchProgress: %v", err)
	}
	rev, ok, err := d.GetWatchProgress(ctx, "configs")
	if err != nil || !ok || rev != 42 {
		t.Fatalf("GetWatchProgress = (%d,%v,%v), want (42,true,nil)", rev, ok, err)
	}

	// DeleteCurrent.
	if err := d.DeleteCurrent(ctx, "node1", "2"); err != nil {
		t.Fatalf("DeleteCurrent: %v", err)
	}
	keys, _ = d.ListCurrentKeys(ctx)
	if len(keys) != 0 {
		t.Fatalf("after delete ListCurrentKeys = %+v, want empty", keys)
	}
}
