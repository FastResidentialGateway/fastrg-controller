package storage

import (
	"context"
	"fmt"
	"testing"
	"time"
)

func TestCASWithRevisionPassesCurrentRevisionAndRefreshesAfterConflict(t *testing.T) {
	c := testEtcd(t)
	ctx := context.Background()
	key := fmt.Sprintf("test/cas-with-revision/%d", time.Now().UnixNano())
	if _, err := c.Client().Put(ctx, key, "seed"); err != nil {
		t.Fatalf("seed: %v", err)
	}
	t.Cleanup(func() { c.Client().Delete(ctx, key) })

	seedResp, err := c.Client().Get(ctx, key)
	if err != nil || len(seedResp.Kvs) != 1 {
		t.Fatalf("get seed: kvs=%d err=%v", len(seedResp.Kvs), err)
	}
	seedRevision := seedResp.Kvs[0].ModRevision

	var calls int
	var retryRevision int64
	err = c.CASWithRevision(ctx, key, func(current []byte, modRevision int64) (CASResult, error) {
		calls++
		switch calls {
		case 1:
			if string(current) != "seed" {
				return CASResult{}, fmt.Errorf("first current = %q, want seed", current)
			}
			if modRevision != seedRevision {
				return CASResult{}, fmt.Errorf("first ModRevision = %d, want %d", modRevision, seedRevision)
			}
			if _, err := c.Client().Put(ctx, key, "concurrent"); err != nil {
				return CASResult{}, fmt.Errorf("inject concurrent write: %w", err)
			}
		case 2:
			if string(current) != "concurrent" {
				return CASResult{}, fmt.Errorf("retry current = %q, want concurrent", current)
			}
			retryRevision = modRevision
		default:
			return CASResult{}, fmt.Errorf("mutate called %d times, want exactly 2", calls)
		}
		return CASResult{Value: []byte("committed")}, nil
	})
	if err != nil {
		t.Fatalf("CASWithRevision: %v", err)
	}
	if calls != 2 {
		t.Fatalf("mutate calls = %d, want 2", calls)
	}
	if retryRevision <= seedRevision {
		t.Fatalf("retry ModRevision = %d, want newer than seed revision %d", retryRevision, seedRevision)
	}
	if got := getVal(t, c, key); got != "committed" {
		t.Fatalf("committed value = %q, want committed", got)
	}
}

func TestCASWithRevisionPassesZeroRevisionForMissingKey(t *testing.T) {
	c := testEtcd(t)
	ctx := context.Background()
	key := fmt.Sprintf("test/cas-with-revision-missing/%d", time.Now().UnixNano())
	if _, err := c.Client().Delete(ctx, key); err != nil {
		t.Fatalf("ensure key absent: %v", err)
	}
	t.Cleanup(func() { c.Client().Delete(ctx, key) })

	err := c.CASWithRevision(ctx, key, func(current []byte, modRevision int64) (CASResult, error) {
		if current != nil {
			return CASResult{}, fmt.Errorf("current = %q, want nil", current)
		}
		if modRevision != 0 {
			return CASResult{}, fmt.Errorf("ModRevision = %d, want 0", modRevision)
		}
		return CASResult{Value: []byte("created")}, nil
	})
	if err != nil {
		t.Fatalf("CASWithRevision create: %v", err)
	}
	if got := getVal(t, c, key); got != "created" {
		t.Fatalf("created value = %q, want created", got)
	}
}
