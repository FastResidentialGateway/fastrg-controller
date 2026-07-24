package kafka

import (
	"encoding/json"
	"testing"
	"time"

	"fastrg-controller/internal/db"
)

// TestRollbackRestampsMetadata verifies that a rollback restores only the last
// successful payload while re-stamping metadata so the resourceVersion chain
// and updatedAt advance instead of regressing to the old snapshot's values.
func TestRollbackRestampsMetadata(t *testing.T) {
	prev := &db.HSIConfigRow{ConfigJSON: []byte(
		`{"config":{"user_id":"2","vlan_id":"3"},"metadata":{"node":"n1","resourceVersion":"1","updatedBy":"admin","updatedAt":"2020-01-01T00:00:00Z"}}`,
	)}
	current := []byte(
		`{"config":{"user_id":"2","vlan_id":"9"},"metadata":{"node":"n1","resourceVersion":"2","updatedBy":"admin","updatedAt":"2020-02-02T00:00:00Z"}}`,
	)

	before := time.Now().UTC()
	result, err := guardedRollbackMutation(prev, current)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Delete {
		t.Fatalf("Delete = true, want a re-stamped put")
	}

	var got struct {
		Config   json.RawMessage `json:"config"`
		Metadata configMetadata  `json:"metadata"`
	}
	if err := json.Unmarshal(result.Value, &got); err != nil {
		t.Fatalf("rollback value is not valid JSON: %v", err)
	}

	// Payload is restored from the last successful config, not from current.
	var prevEnv struct {
		Config json.RawMessage `json:"config"`
	}
	if err := json.Unmarshal(prev.ConfigJSON, &prevEnv); err != nil {
		t.Fatalf("decode prev: %v", err)
	}
	if !jsonEqual(t, got.Config, prevEnv.Config) {
		t.Fatalf("payload = %s, want restored last-successful payload %s", got.Config, prevEnv.Config)
	}

	// Metadata is re-stamped, never copied from the old snapshot.
	if got.Metadata.ResourceVersion != "3" {
		t.Fatalf("resourceVersion = %q, want %q (current 2 + 1)", got.Metadata.ResourceVersion, "3")
	}
	if got.Metadata.UpdatedBy != rollbackUpdatedBy {
		t.Fatalf("updatedBy = %q, want %q", got.Metadata.UpdatedBy, rollbackUpdatedBy)
	}
	if got.Metadata.Node != "n1" {
		t.Fatalf("node = %q, want carried-over %q", got.Metadata.Node, "n1")
	}
	updatedAt, err := time.Parse(time.RFC3339, got.Metadata.UpdatedAt)
	if err != nil {
		t.Fatalf("updatedAt %q not RFC3339: %v", got.Metadata.UpdatedAt, err)
	}
	if updatedAt.Before(before.Truncate(time.Second)) {
		t.Fatalf("updatedAt %v regressed; must be at or after the rollback moment %v", updatedAt, before)
	}
}

// TestRollbackConfigValueRejectsEmptyPayload fails closed when the last
// successful config carries no payload to restore.
func TestRollbackConfigValueRejectsEmptyPayload(t *testing.T) {
	_, err := rollbackConfigValue([]byte(`{"metadata":{"resourceVersion":"1"}}`), []byte(`{"metadata":{"resourceVersion":"2"}}`), 2)
	if err == nil {
		t.Fatalf("expected error for payload-less previous config, got nil")
	}
}

func jsonEqual(t *testing.T, a, b []byte) bool {
	t.Helper()
	var av, bv any
	if err := json.Unmarshal(a, &av); err != nil {
		t.Fatalf("unmarshal a: %v", err)
	}
	if err := json.Unmarshal(b, &bv); err != nil {
		t.Fatalf("unmarshal b: %v", err)
	}
	ab, _ := json.Marshal(av)
	bb, _ := json.Marshal(bv)
	return string(ab) == string(bb)
}
