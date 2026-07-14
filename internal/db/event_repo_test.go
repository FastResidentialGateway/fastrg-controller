package db

import (
	"context"
	"os"
	"testing"
	"time"
)

// TestEventRepo exercises the Kafka-fed tables against a real PostgreSQL.
// Skipped unless TEST_DATABASE_URL is set.
func TestEventRepo(t *testing.T) {
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("TEST_DATABASE_URL not set; skipping PostgreSQL integration test")
	}

	ctx := context.Background()
	scopedDSN, cleanup := createIsolatedTestSchema(t, ctx, dsn, "event_repo")
	defer cleanup()
	d, err := New(ctx, scopedDSN)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer d.Close()

	t0 := time.Now().UTC().Truncate(time.Second)

	// --- pppoe_status: newest event_time wins, older is ignored. ---
	if err := d.UpsertPPPoEStatus(ctx, PPPoEStatusRow{
		NodeUUID: "n1", UserID: "2", Phase: "connecting", EventTime: t0,
	}); err != nil {
		t.Fatalf("UpsertPPPoEStatus connecting: %v", err)
	}
	if err := d.UpsertPPPoEStatus(ctx, PPPoEStatusRow{
		NodeUUID: "n1", UserID: "2", Phase: "connected", HSIIPv4: "10.0.0.5", EventTime: t0.Add(time.Second),
	}); err != nil {
		t.Fatalf("UpsertPPPoEStatus connected: %v", err)
	}
	// Stale (older) transition must not overwrite.
	if err := d.UpsertPPPoEStatus(ctx, PPPoEStatusRow{
		NodeUUID: "n1", UserID: "2", Phase: "disconnected", EventTime: t0.Add(-time.Hour),
	}); err != nil {
		t.Fatalf("UpsertPPPoEStatus stale: %v", err)
	}

	st, ok, err := d.GetPPPoEStatus(ctx, "n1", "2")
	if err != nil || !ok {
		t.Fatalf("GetPPPoEStatus = (%v,%v,%v)", st, ok, err)
	}
	if st.Phase != "connected" || st.HSIIPv4 != "10.0.0.5" {
		t.Fatalf("pppoe status = %+v, want phase=connected ip=10.0.0.5", st)
	}

	if _, ok, _ := d.GetPPPoEStatus(ctx, "n1", "999"); ok {
		t.Fatal("expected no status for unknown user")
	}

	// --- node_events: insert + idempotent dedup. ---
	success := false
	ev := NodeEventRow{
		NodeUUID: "n1", UserID: "2", EventType: "CONFIG_APPLY_FAIL", Action: "update",
		Success: &success, ErrorCode: "EINVAL", ErrorMessage: "bad vlan", EventTime: t0,
	}
	ins, err := d.InsertNodeEvent(ctx, ev)
	if err != nil || !ins {
		t.Fatalf("InsertNodeEvent first = (%v,%v), want (true,nil)", ins, err)
	}
	ins, err = d.InsertNodeEvent(ctx, ev) // same dedup key
	if err != nil || ins {
		t.Fatalf("InsertNodeEvent dup = (%v,%v), want (false,nil)", ins, err)
	}

	// A runtime error on another node.
	if _, err := d.InsertNodeEvent(ctx, NodeEventRow{
		NodeUUID: "n2", UserID: "0", EventType: "RUNTIME_ERROR", Module: "pppd",
		ErrorMessage: "link down", EventTime: t0.Add(time.Second),
	}); err != nil {
		t.Fatalf("InsertNodeEvent runtime: %v", err)
	}

	all, err := d.ListNodeEvents(ctx, "", "", 0)
	if err != nil || len(all) != 2 {
		t.Fatalf("ListNodeEvents all = %d rows (%v), want 2", len(all), err)
	}
	// Newest first.
	if all[0].NodeUUID != "n2" {
		t.Fatalf("expected newest (n2) first, got %s", all[0].NodeUUID)
	}

	byNode, _ := d.ListNodeEvents(ctx, "n1", "", 0)
	if len(byNode) != 1 || byNode[0].EventType != "CONFIG_APPLY_FAIL" {
		t.Fatalf("ListNodeEvents node filter = %+v", byNode)
	}
	byType, _ := d.ListNodeEvents(ctx, "", "RUNTIME_ERROR", 0)
	if len(byType) != 1 || byType[0].Module != "pppd" {
		t.Fatalf("ListNodeEvents type filter = %+v", byType)
	}

	// Delete by id.
	del, err := d.DeleteNodeEvents(ctx, []int64{all[0].ID, all[1].ID})
	if err != nil || del != 2 {
		t.Fatalf("DeleteNodeEvents = (%d,%v), want (2,nil)", del, err)
	}
	remaining, _ := d.ListNodeEvents(ctx, "", "", 0)
	if len(remaining) != 0 {
		t.Fatalf("after delete = %d rows, want 0", len(remaining))
	}
}
