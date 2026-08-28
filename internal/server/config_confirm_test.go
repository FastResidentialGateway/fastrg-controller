package server

import (
	"testing"
	"time"

	"fastrg-controller/internal/db"
)

// TestParseHSIConfigKey: only per-subscriber HSI config keys carry a
// config-apply confirmation; every other key under configs/ is ignored.
func TestParseHSIConfigKey(t *testing.T) {
	tests := []struct {
		name string
		key  string
		want db.ConfigKey
		ok   bool
	}{
		{
			name: "hsi config",
			key:  "configs/node-a/hsi/2",
			want: db.ConfigKey{NodeUUID: "node-a", UserID: "2"},
			ok:   true,
		},
		{name: "dns records", key: "configs/node-a/dns/2"},
		{name: "prefix itself", key: "configs/"},
		{name: "missing user", key: "configs/node-a/hsi/"},
		{name: "missing node", key: "configs//hsi/2"},
		{name: "too deep", key: "configs/node-a/hsi/2/extra"},
		{name: "different prefix", key: "user_counts/node-a/"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, ok := parseHSIConfigKey([]byte(tt.key))
			if ok != tt.ok {
				t.Fatalf("parseHSIConfigKey(%q) ok = %v, want %v", tt.key, ok, tt.ok)
			}
			if ok && got != tt.want {
				t.Fatalf("parseHSIConfigKey(%q) = %+v, want %+v", tt.key, got, tt.want)
			}
		})
	}
}

// TestUnconfirmedFromRevisions: a node is pending only when it is active and
// holds a config newer than the one it confirmed applying.
func TestUnconfirmedFromRevisions(t *testing.T) {
	active := map[string]republishTarget{
		"node-a": {nodeUUID: "node-a", nodeIP: "10.0.0.1"},
		"node-b": {nodeUUID: "node-b", nodeIP: "10.0.0.2"},
	}
	confirmed := map[db.ConfigKey]int64{
		{NodeUUID: "node-a", UserID: "1"}: 10, // up to date
		{NodeUUID: "node-a", UserID: "2"}: 7,  // behind
		{NodeUUID: "node-b", UserID: "1"}: 20, // ahead of what was pushed
		{NodeUUID: "gone", UserID: "1"}:   1,
	}
	pushed := map[db.ConfigKey]int64{
		{NodeUUID: "node-a", UserID: "1"}: 10,
		{NodeUUID: "node-a", UserID: "2"}: 9,
		{NodeUUID: "node-b", UserID: "1"}: 20,
		{NodeUUID: "node-c", UserID: "1"}: 30, // never confirmed, but inactive
		{NodeUUID: "gone", UserID: "1"}:   40, // never confirmed, not registered
	}

	pending := unconfirmedFromRevisions(active, confirmed, pushed)
	if len(pending) != 1 {
		t.Fatalf("pending = %+v, want only node-a", pending)
	}
	if _, ok := pending["node-a"]; !ok {
		t.Fatalf("pending = %+v, want node-a", pending)
	}
}

// TestUnconfirmedFromRevisionsCoversMissingRows: a config the node has never
// confirmed at all counts as pending.
func TestUnconfirmedFromRevisionsCoversMissingRows(t *testing.T) {
	active := map[string]republishTarget{"node-a": {nodeUUID: "node-a"}}
	pushed := map[db.ConfigKey]int64{{NodeUUID: "node-a", UserID: "5"}: 3}

	pending := unconfirmedFromRevisions(active, map[db.ConfigKey]int64{}, pushed)
	if _, ok := pending["node-a"]; !ok {
		t.Fatalf("pending = %+v, want node-a", pending)
	}
}

// TestDueForNudgeWaitsOutTheTimeout: a node that has just fallen behind is left
// alone until configConfirmTimeout has passed, then asked.
func TestDueForNudgeWaitsOutTheTimeout(t *testing.T) {
	manager := NewNodeMonitorManager(nil)
	pending := map[string]republishTarget{"node-a": {nodeUUID: "node-a"}}
	start := time.Now()

	if due := manager.dueForNudge(pending, start); len(due) != 0 {
		t.Fatalf("first sweep asked %d node(s), want 0 — the timeout has not passed", len(due))
	}
	if due := manager.dueForNudge(pending, start.Add(configConfirmTimeout-time.Second)); len(due) != 0 {
		t.Fatalf("sweep inside the timeout asked %d node(s), want 0", len(due))
	}
	due := manager.dueForNudge(pending, start.Add(configConfirmTimeout+time.Second))
	if len(due) != 1 || due[0].nodeUUID != "node-a" {
		t.Fatalf("sweep past the timeout asked %+v, want node-a", due)
	}
}

// TestDueForNudgeBacksOff: a node that never converges is asked at a doubling
// interval, capped at configConfirmBackoffMax.
func TestDueForNudgeBacksOff(t *testing.T) {
	manager := NewNodeMonitorManager(nil)
	pending := map[string]republishTarget{"node-a": {nodeUUID: "node-a"}}
	now := time.Now()

	manager.dueForNudge(pending, now) // starts tracking
	now = now.Add(configConfirmTimeout)
	if due := manager.dueForNudge(pending, now); len(due) != 1 {
		t.Fatalf("first request asked %d node(s), want 1", len(due))
	}

	// The wait after the first request is twice the timeout, so a sweep one
	// timeout later is still too early.
	if due := manager.dueForNudge(pending, now.Add(configConfirmTimeout)); len(due) != 0 {
		t.Fatalf("sweep inside the backoff asked %d node(s), want 0", len(due))
	}
	now = now.Add(2 * configConfirmTimeout)
	if due := manager.dueForNudge(pending, now); len(due) != 1 {
		t.Fatalf("sweep past the backoff asked %d node(s), want 1", len(due))
	}

	// Keep asking far into the future: the interval must stop growing at the cap.
	for range 20 {
		now = now.Add(configConfirmBackoffMax)
		if due := manager.dueForNudge(pending, now); len(due) != 1 {
			t.Fatalf("sweep a full backoff cap later asked %d node(s), want 1", len(due))
		}
	}
}

// TestDueForNudgeForgetsConvergedNodes: once a node has caught up its backoff is
// dropped, so a later lag waits out the full timeout again instead of being
// asked immediately.
func TestDueForNudgeForgetsConvergedNodes(t *testing.T) {
	manager := NewNodeMonitorManager(nil)
	pending := map[string]republishTarget{"node-a": {nodeUUID: "node-a"}}
	now := time.Now()

	manager.dueForNudge(pending, now)
	now = now.Add(configConfirmTimeout + time.Second)
	if due := manager.dueForNudge(pending, now); len(due) != 1 {
		t.Fatalf("first request asked %d node(s), want 1", len(due))
	}

	// Node caught up.
	if due := manager.dueForNudge(map[string]republishTarget{}, now); len(due) != 0 {
		t.Fatalf("sweep with nothing pending asked %d node(s), want 0", len(due))
	}
	manager.confirmMu.Lock()
	tracked := len(manager.confirmState)
	manager.confirmMu.Unlock()
	if tracked != 0 {
		t.Fatalf("%d node(s) still tracked after converging, want 0", tracked)
	}

	// It falls behind again: the timeout starts over.
	now = now.Add(time.Hour)
	if due := manager.dueForNudge(pending, now); len(due) != 0 {
		t.Fatalf("a freshly lagging node was asked %d time(s), want 0", len(due))
	}
}
