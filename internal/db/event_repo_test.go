package db

import (
	"context"
	"encoding/json"
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

func TestPPPoEStatusIPv6Fields(t *testing.T) {
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("TEST_DATABASE_URL not set; skipping PostgreSQL integration test")
	}

	ctx := context.Background()
	scopedDSN, cleanup := createIsolatedTestSchema(t, ctx, dsn, "pppoe_ipv6")
	defer cleanup()
	database, err := New(ctx, scopedDSN)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer database.Close()

	t0 := time.Now().UTC().Truncate(time.Second)
	if err := database.UpsertPPPoEStatus(ctx, PPPoEStatusRow{
		NodeUUID:        "ipv6-node",
		UserID:          "2",
		Phase:           "connected",
		HSIIPv4:         "10.0.0.5",
		HSIIPv6:         "2001:db8::5",
		HSIIPv6PDPrefix: "2001:db8:ab00::/56",
		HSIIPv6DNS:      "2001:4860:4860::8888,2606:4700:4700::1111",
		EventTime:       t0,
	}); err != nil {
		t.Fatalf("UpsertPPPoEStatus IPv6: %v", err)
	}

	status, ok, err := database.GetPPPoEStatus(ctx, "ipv6-node", "2")
	if err != nil || !ok {
		t.Fatalf("GetPPPoEStatus IPv6 = (%+v,%v,%v)", status, ok, err)
	}
	if status.HSIIPv6 != "2001:db8::5" ||
		status.HSIIPv6PDPrefix != "2001:db8:ab00::/56" ||
		status.HSIIPv6DNS != "2001:4860:4860::8888,2606:4700:4700::1111" {
		t.Fatalf("IPv6 status = %+v", status)
	}

	encoded, err := json.Marshal(status)
	if err != nil {
		t.Fatalf("marshal PPPoEStatusRow: %v", err)
	}
	var response map[string]any
	if err := json.Unmarshal(encoded, &response); err != nil {
		t.Fatalf("unmarshal PPPoEStatusRow JSON: %v", err)
	}
	for key, want := range map[string]string{
		"hsi_ipv6":           "2001:db8::5",
		"hsi_ipv6_pd_prefix": "2001:db8:ab00::/56",
		"hsi_ipv6_dns":       "2001:4860:4860::8888,2606:4700:4700::1111",
	} {
		if got, exists := response[key]; !exists || got != want {
			t.Errorf("JSON field %q = (%v,%v), want (%q,true)", key, got, exists, want)
		}
	}

	clearedAt := t0.Add(2 * time.Second)
	if err := database.UpsertPPPoEStatus(ctx, PPPoEStatusRow{
		NodeUUID: "ipv6-node", UserID: "2", Phase: "disconnected", EventTime: clearedAt,
	}); err != nil {
		t.Fatalf("UpsertPPPoEStatus clear IPv6: %v", err)
	}
	status, ok, err = database.GetPPPoEStatus(ctx, "ipv6-node", "2")
	if err != nil || !ok {
		t.Fatalf("GetPPPoEStatus cleared = (%+v,%v,%v)", status, ok, err)
	}
	if status.HSIIPv6 != "" || status.HSIIPv6PDPrefix != "" || status.HSIIPv6DNS != "" {
		t.Fatalf("IPv6 fields were not cleared: %+v", status)
	}

	if err := database.UpsertPPPoEStatus(ctx, PPPoEStatusRow{
		NodeUUID:        "ipv6-node",
		UserID:          "2",
		Phase:           "connected",
		HSIIPv6:         "2001:db8::stale",
		HSIIPv6PDPrefix: "2001:db8:ff00::/56",
		HSIIPv6DNS:      "2001:db8::53",
		EventTime:       t0.Add(time.Second),
	}); err != nil {
		t.Fatalf("UpsertPPPoEStatus stale IPv6: %v", err)
	}
	status, ok, err = database.GetPPPoEStatus(ctx, "ipv6-node", "2")
	if err != nil || !ok {
		t.Fatalf("GetPPPoEStatus after stale = (%+v,%v,%v)", status, ok, err)
	}
	if status.Phase != "disconnected" || !status.EventTime.Equal(clearedAt) ||
		status.HSIIPv6 != "" || status.HSIIPv6PDPrefix != "" || status.HSIIPv6DNS != "" {
		t.Fatalf("stale event changed status: %+v", status)
	}
}

func TestUpsertPPPoEStatusPreservingIPv6(t *testing.T) {
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("TEST_DATABASE_URL not set; skipping PostgreSQL integration test")
	}

	ctx := context.Background()
	scopedDSN, cleanup := createIsolatedTestSchema(t, ctx, dsn, "pppoe_preserve_ipv6")
	defer cleanup()
	database, err := New(ctx, scopedDSN)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer database.Close()

	t0 := time.Now().UTC().Truncate(time.Second)
	if err := database.UpsertPPPoEStatus(ctx, PPPoEStatusRow{
		NodeUUID:        "poll-node",
		UserID:          "7",
		Phase:           "connected",
		HSIIPv4:         "10.0.0.7",
		HSIIPv6:         "2001:db8::7",
		HSIIPv6PDPrefix: "2001:db8:700::/56",
		HSIIPv6DNS:      "2001:db8::53,2001:db8::54",
		EventTime:       t0,
	}); err != nil {
		t.Fatalf("seed PPPoE status: %v", err)
	}

	pollTime := t0.Add(2 * time.Second)
	if err := database.UpsertPPPoEStatusPreservingIPv6(ctx, PPPoEStatusRow{
		NodeUUID: "poll-node", UserID: "7", Phase: "connecting",
		HSIIPv4: "10.0.0.8", EventTime: pollTime,
	}); err != nil {
		t.Fatalf("poll update: %v", err)
	}
	status, ok, err := database.GetPPPoEStatus(ctx, "poll-node", "7")
	if err != nil || !ok {
		t.Fatalf("GetPPPoEStatus after poll = (%+v,%v,%v)", status, ok, err)
	}
	if status.Phase != "connecting" || status.HSIIPv4 != "10.0.0.8" ||
		!status.EventTime.Equal(pollTime) || status.HSIIPv6 != "2001:db8::7" ||
		status.HSIIPv6PDPrefix != "2001:db8:700::/56" ||
		status.HSIIPv6DNS != "2001:db8::53,2001:db8::54" {
		t.Fatalf("poll update did not preserve IPv6: %+v", status)
	}

	if err := database.UpsertPPPoEStatusPreservingIPv6(ctx, PPPoEStatusRow{
		NodeUUID:        "new-poll-node",
		UserID:          "8",
		Phase:           "connected",
		HSIIPv6:         "must-not-be-inserted",
		HSIIPv6PDPrefix: "must-not-be-inserted",
		HSIIPv6DNS:      "must-not-be-inserted",
		EventTime:       pollTime,
	}); err != nil {
		t.Fatalf("first poll insert: %v", err)
	}
	inserted, ok, err := database.GetPPPoEStatus(ctx, "new-poll-node", "8")
	if err != nil || !ok {
		t.Fatalf("GetPPPoEStatus first poll = (%+v,%v,%v)", inserted, ok, err)
	}
	if inserted.HSIIPv6 != "" || inserted.HSIIPv6PDPrefix != "" || inserted.HSIIPv6DNS != "" {
		t.Fatalf("first poll insert populated IPv6 fields: %+v", inserted)
	}

	if err := database.UpsertPPPoEStatusPreservingIPv6(ctx, PPPoEStatusRow{
		NodeUUID: "poll-node", UserID: "7", Phase: "disconnected",
		HSIIPv4: "10.0.0.99", EventTime: t0.Add(time.Second),
	}); err != nil {
		t.Fatalf("stale poll update: %v", err)
	}
	status, ok, err = database.GetPPPoEStatus(ctx, "poll-node", "7")
	if err != nil || !ok {
		t.Fatalf("GetPPPoEStatus after stale poll = (%+v,%v,%v)", status, ok, err)
	}
	if status.Phase != "connecting" || status.HSIIPv4 != "10.0.0.8" ||
		!status.EventTime.Equal(pollTime) || status.HSIIPv6 != "2001:db8::7" ||
		status.HSIIPv6PDPrefix != "2001:db8:700::/56" ||
		status.HSIIPv6DNS != "2001:db8::53,2001:db8::54" {
		t.Fatalf("stale poll update changed status: %+v", status)
	}
}
