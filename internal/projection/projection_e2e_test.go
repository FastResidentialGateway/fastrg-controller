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
// runs the projection, mutates and deletes the config, and asserts every change
// is recorded in hsi_config_history (the projection is history-only;
// hsi_config_current is written by the Kafka CONFIG_APPLY_OK handler). Skipped
// unless both TEST_ETCD_ENDPOINTS and TEST_DATABASE_URL are set.
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

	scopedDSN, cleanup := createTask12ProjectionSchema(t, ctx, dsn)
	defer cleanup()
	database, err := db.New(ctx, scopedDSN)
	if err != nil {
		t.Fatalf("db connect: %v", err)
	}
	defer database.Close()

	// Own pool for assertions (keeps db.DB's pool private).
	pool, err := pgxpool.New(ctx, scopedDSN)
	if err != nil {
		t.Fatalf("assert pool: %v", err)
	}
	defer pool.Close()

	// Seed a config before the projection starts (exercises initial reconcile).
	key := "configs/node1/hsi/2"
	if _, err := cli.Put(ctx, key, `{"config":{"user_id":"2","desire_status":"connect"},"metadata":{"resourceVersion":"1","updatedBy":"admin"}}`); err != nil {
		t.Fatalf("seed put: %v", err)
	}

	proj := New(etcd, database)
	go proj.Run(ctx)

	// The projection is history-only, so track hsi_config_history (not current).
	hasHistory := func(cond string) bool {
		var n int
		if err := pool.QueryRow(ctx,
			`SELECT count(*) FROM hsi_config_history WHERE node_uuid='node1' AND user_id='2' AND `+cond,
		).Scan(&n); err != nil {
			return false
		}
		return n > 0
	}

	// After reconcile: the seeded config (desire connect) is recorded.
	waitFor(t, func() bool { return hasHistory(`action='upsert' AND desire_status='connect'`) },
		"initial reconcile to record the seeded config in history")

	// Live update via watch: desire flips to disconnect.
	if _, err := cli.Put(ctx, key, `{"config":{"user_id":"2","desire_status":"disconnect"},"metadata":{"resourceVersion":"2","updatedBy":"admin"}}`); err != nil {
		t.Fatalf("update put: %v", err)
	}
	waitFor(t, func() bool { return hasHistory(`action='upsert' AND desire_status='disconnect'`) },
		"live update to be recorded in history")

	// Live delete via watch: a delete row is recorded.
	if _, err := cli.Delete(ctx, key); err != nil {
		t.Fatalf("etcd delete: %v", err)
	}
	waitFor(t, func() bool { return hasHistory(`action='delete'`) },
		"delete to be recorded in history")

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
