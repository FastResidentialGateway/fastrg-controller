package storage

import (
	"context"
	"errors"
	"os"
	"testing"
)

func testEtcd(t *testing.T) *EtcdClient {
	t.Helper()
	eps := os.Getenv("TEST_ETCD_ENDPOINTS")
	if eps == "" {
		t.Skip("TEST_ETCD_ENDPOINTS not set; skipping etcd CAS test")
	}
	t.Setenv("ETCD_ENDPOINTS", eps)
	c, err := NewEtcdClient()
	if err != nil {
		t.Fatalf("etcd connect: %v", err)
	}
	t.Cleanup(func() { c.Close() })
	return c
}

func getVal(t *testing.T, c *EtcdClient, key string) string {
	t.Helper()
	resp, err := c.Client().Get(context.Background(), key)
	if err != nil {
		t.Fatalf("get %s: %v", key, err)
	}
	if len(resp.Kvs) == 0 {
		return ""
	}
	return string(resp.Kvs[0].Value)
}

// TestCASCreateUpdateDelete exercises the three CAS outcomes: create (nil
// current), update (matching current), and delete.
func TestCASCreateUpdateDelete(t *testing.T) {
	c := testEtcd(t)
	ctx := context.Background()
	key := "test/cas/roundtrip"
	c.Client().Delete(ctx, key)
	t.Cleanup(func() { c.Client().Delete(ctx, key) })

	// Create: current must be nil.
	if err := c.CAS(ctx, key, func(current []byte) (CASResult, error) {
		if current != nil {
			t.Errorf("create: expected nil current, got %q", current)
		}
		return CASResult{Value: []byte("v1")}, nil
	}); err != nil {
		t.Fatalf("CAS create: %v", err)
	}
	if got := getVal(t, c, key); got != "v1" {
		t.Fatalf("after create = %q, want v1", got)
	}

	// Update: current must be v1.
	if err := c.CAS(ctx, key, func(current []byte) (CASResult, error) {
		if string(current) != "v1" {
			t.Errorf("update: expected v1 current, got %q", current)
		}
		return CASResult{Value: []byte("v2")}, nil
	}); err != nil {
		t.Fatalf("CAS update: %v", err)
	}
	if got := getVal(t, c, key); got != "v2" {
		t.Fatalf("after update = %q, want v2", got)
	}

	// Delete.
	if err := c.CAS(ctx, key, func([]byte) (CASResult, error) {
		return CASResult{Delete: true}, nil
	}); err != nil {
		t.Fatalf("CAS delete: %v", err)
	}
	if got := getVal(t, c, key); got != "" {
		t.Fatalf("after delete = %q, want empty", got)
	}
}

// TestCASMutateErrorAborts: a mutate error aborts the CAS with no write.
func TestCASMutateErrorAborts(t *testing.T) {
	c := testEtcd(t)
	ctx := context.Background()
	key := "test/cas/abort"
	c.Client().Delete(ctx, key)
	t.Cleanup(func() { c.Client().Delete(ctx, key) })

	sentinel := errors.New("mutate says no")
	err := c.CAS(ctx, key, func([]byte) (CASResult, error) {
		return CASResult{}, sentinel
	})
	if !errors.Is(err, sentinel) {
		t.Fatalf("CAS = %v, want the mutate error", err)
	}
	if got := getVal(t, c, key); got != "" {
		t.Fatalf("no write should happen on mutate error, got %q", got)
	}
}

// TestCASConflictRetriesThenFails: the mutate closure rewrites the key on every
// attempt, so the guard revision is always stale and CAS exhausts its retries.
func TestCASConflictRetriesThenFails(t *testing.T) {
	c := testEtcd(t)
	ctx := context.Background()
	key := "test/cas/conflict"
	if _, err := c.Client().Put(ctx, key, "seed"); err != nil {
		t.Fatalf("seed: %v", err)
	}
	t.Cleanup(func() { c.Client().Delete(ctx, key) })

	err := c.CAS(ctx, key, func([]byte) (CASResult, error) {
		c.Client().Put(ctx, key, "moved") // invalidate the read revision
		return CASResult{Value: []byte("v")}, nil
	})
	if !errors.Is(err, ErrCASConflict) {
		t.Fatalf("CAS = %v, want ErrCASConflict", err)
	}
}
