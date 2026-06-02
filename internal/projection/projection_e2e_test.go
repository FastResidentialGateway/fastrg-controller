package projection

import (
	"context"
	"os"
	"testing"
	"time"

	"fastrg-controller/internal/db"
	"fastrg-controller/internal/storage"

	"github.com/jackc/pgx/v5/pgxpool"
	clientv3 "go.etcd.io/etcd/client/v3"
)

// TestProjectionEndToEnd drives a real etcd + PostgreSQL: it seeds a config,
// runs the projection, mutates and deletes the config, and asserts the current
// and history tables track every change. Skipped unless both TEST_ETCD_ENDPOINTS
// and TEST_DATABASE_URL are set.
func TestProjectionEndToEnd(t *testing.T) {
	etcdEndpoints := os.Getenv("TEST_ETCD_ENDPOINTS")
	dsn := os.Getenv("TEST_DATABASE_URL")
	if etcdEndpoints == "" || dsn == "" {
		t.Skip("TEST_ETCD_ENDPOINTS / TEST_DATABASE_URL not set; skipping projection e2e")
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// storage.NewEtcdClient reads ETCD_ENDPOINTS from the environment.
	t.Setenv("ETCD_ENDPOINTS", etcdEndpoints)
	etcd, err := storage.NewEtcdClient()
	if err != nil {
		t.Fatalf("etcd connect: %v", err)
	}
	defer etcd.Close()
	cli := etcd.Client()
	if _, err := cli.Delete(ctx, "configs/", clientv3.WithPrefix()); err != nil {
		t.Fatalf("etcd cleanup: %v", err)
	}

	database, err := db.New(ctx, dsn)
	if err != nil {
		t.Fatalf("db connect: %v", err)
	}
	defer database.Close()

	// Own pool for assertions (keeps db.DB's pool private).
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatalf("assert pool: %v", err)
	}
	defer pool.Close()
	for _, tbl := range []string{"hsi_config_current", "hsi_config_history", "etcd_watch_progress"} {
		if _, err := pool.Exec(ctx, "TRUNCATE "+tbl); err != nil {
			t.Fatalf("truncate %s: %v", tbl, err)
		}
	}

	// Seed a config before the projection starts (exercises initial reconcile).
	key := "configs/node1/hsi/2"
	if _, err := cli.Put(ctx, key, `{"config":{"user_id":"2","desire_status":"connect"},"metadata":{"resourceVersion":"1","updatedBy":"admin"}}`); err != nil {
		t.Fatalf("seed put: %v", err)
	}

	proj := New(etcd, database)
	go proj.Run(ctx)

	currentDesire := func() (string, bool) {
		var d string
		err := pool.QueryRow(ctx,
			`SELECT desire_status FROM hsi_config_current WHERE node_uuid='node1' AND user_id='2'`).Scan(&d)
		if err != nil {
			return "", false
		}
		return d, true
	}

	// After reconcile: current row present with desire_status=connect.
	waitFor(t, func() bool { d, ok := currentDesire(); return ok && d == "connect" },
		"initial reconcile to project the seeded config")

	// Live update via watch.
	if _, err := cli.Put(ctx, key, `{"config":{"user_id":"2","desire_status":"disconnect"},"metadata":{"resourceVersion":"2","updatedBy":"admin"}}`); err != nil {
		t.Fatalf("update put: %v", err)
	}
	waitFor(t, func() bool { d, ok := currentDesire(); return ok && d == "disconnect" },
		"live update to reach the current table")

	// Live delete via watch.
	if _, err := cli.Delete(ctx, key); err != nil {
		t.Fatalf("etcd delete: %v", err)
	}
	waitFor(t, func() bool { _, ok := currentDesire(); return !ok },
		"delete to remove the current row")

	// History captured the live update + delete (reconcile does not log history).
	var hist int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM hsi_config_history`).Scan(&hist); err != nil {
		t.Fatalf("count history: %v", err)
	}
	if hist < 2 {
		t.Fatalf("history rows = %d, want >= 2 (update + delete)", hist)
	}

	// Checkpoint advanced.
	if rev, ok, _ := database.GetWatchProgress(ctx, "configs"); !ok || rev == 0 {
		t.Fatalf("watch progress not advanced: rev=%d ok=%v", rev, ok)
	}
}

func waitFor(t *testing.T, cond func() bool, what string) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", what)
}
