package kafka

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"testing"
	"time"

	"fastrg-controller/internal/db"
	"fastrg-controller/internal/storage"
)

// TestRollbackRestampThroughEtcdCAS drives guardedRollbackMutation through the
// exact wiring consumer.handle uses (storage.CAS with the guard closure) against
// a real etcd, so the re-stamp is validated on the actual concurrency path, not
// just the pure mutate. Gated on TEST_ETCD_ENDPOINTS; skipped otherwise.
func TestRollbackRestampThroughEtcdCAS(t *testing.T) {
	endpoints := os.Getenv("TEST_ETCD_ENDPOINTS")
	if endpoints == "" {
		t.Skip("TEST_ETCD_ENDPOINTS not set; skipping etcd rollback CAS integration")
	}
	t.Setenv("ETCD_ENDPOINTS", endpoints)
	etcd, err := storage.NewEtcdClient()
	if err != nil {
		t.Fatalf("etcd connect: %v", err)
	}
	t.Cleanup(etcd.Close)

	ctx := context.Background()
	key := fmt.Sprintf("configs/rollback-cas-%d/hsi/2", time.Now().UnixNano())
	t.Cleanup(func() { _, _ = etcd.Client().Delete(ctx, key) })

	prev := &db.HSIConfigRow{ConfigJSON: []byte(
		`{"config":{"user_id":"2","vlan_id":"3"},"metadata":{"node":"n1","resourceVersion":"1","updatedBy":"admin","updatedAt":"2020-01-01T00:00:00Z"}}`,
	)}
	// current is the failed successor (rv=2): the guard authorizes the rollback.
	current := `{"config":{"user_id":"2","vlan_id":"9"},"metadata":{"node":"n1","resourceVersion":"2","updatedBy":"admin","updatedAt":"2020-02-02T00:00:00Z"}}`
	if _, err := etcd.Client().Put(ctx, key, current); err != nil {
		t.Fatalf("seed current: %v", err)
	}

	before := time.Now().UTC()
	casErr := etcd.CAS(ctx, key, func(cur []byte) (storage.CASResult, error) {
		return guardedRollbackMutation(prev, cur)
	})
	if casErr != nil {
		t.Fatalf("rollback CAS: %v", casErr)
	}

	resp, err := etcd.Client().Get(ctx, key)
	if err != nil || len(resp.Kvs) != 1 {
		t.Fatalf("get after rollback: err=%v kvs=%d", err, len(resp.Kvs))
	}
	var got struct {
		Config   json.RawMessage `json:"config"`
		Metadata configMetadata  `json:"metadata"`
	}
	if err := json.Unmarshal(resp.Kvs[0].Value, &got); err != nil {
		t.Fatalf("decode rolled-back value: %v", err)
	}

	if got.Metadata.ResourceVersion != "3" {
		t.Fatalf("resourceVersion = %q, want %q (no chain regression)", got.Metadata.ResourceVersion, "3")
	}
	if got.Metadata.UpdatedBy != rollbackUpdatedBy {
		t.Fatalf("updatedBy = %q, want %q", got.Metadata.UpdatedBy, rollbackUpdatedBy)
	}
	updatedAt, err := time.Parse(time.RFC3339, got.Metadata.UpdatedAt)
	if err != nil {
		t.Fatalf("updatedAt %q not RFC3339: %v", got.Metadata.UpdatedAt, err)
	}
	if updatedAt.Before(before.Truncate(time.Second)) {
		t.Fatalf("updatedAt %v regressed below rollback moment %v", updatedAt, before)
	}
	// Payload is the last successful config's, not the failed current's.
	var payload struct {
		VlanID string `json:"vlan_id"`
	}
	if err := json.Unmarshal(got.Config, &payload); err != nil {
		t.Fatalf("decode payload: %v", err)
	}
	if payload.VlanID != "3" {
		t.Fatalf("vlan_id = %q, want restored last-successful %q", payload.VlanID, "3")
	}

	// Sanity: current-based guard skips when the value has moved past the failed
	// successor (rv already 3 now), proving the guard reads current, not prev.
	skipErr := etcd.CAS(ctx, key, func(cur []byte) (storage.CASResult, error) {
		return guardedRollbackMutation(prev, cur)
	})
	if skipErr == nil {
		t.Fatalf("expected the guard to skip a superseded rollback, got a write")
	}
}
