package db

import (
	"context"
	"encoding/json"
	"os"
	"testing"
	"time"
)

// TestDSN covers DATABASE_URL precedence, POSTGRES_* assembly, and the empty
// case. Pure env logic, no database needed.
func TestDSN(t *testing.T) {
	t.Setenv("DATABASE_URL", "postgres://u:p@h:5432/db?sslmode=require")
	if got := DSN(); got != "postgres://u:p@h:5432/db?sslmode=require" {
		t.Fatalf("DSN with DATABASE_URL = %q", got)
	}

	t.Setenv("DATABASE_URL", "")
	t.Setenv("POSTGRES_HOST", "dbhost")
	t.Setenv("POSTGRES_PORT", "6543")
	t.Setenv("POSTGRES_USER", "fastrg")
	t.Setenv("POSTGRES_PASSWORD", "secret")
	t.Setenv("POSTGRES_DB", "fastrg")
	t.Setenv("POSTGRES_SSLMODE", "disable")
	if got, want := DSN(), "postgres://fastrg:secret@dbhost:6543/fastrg?sslmode=disable"; got != want {
		t.Fatalf("DSN assembled = %q, want %q", got, want)
	}

	t.Setenv("DATABASE_URL", "")
	t.Setenv("POSTGRES_HOST", "")
	if got := DSN(); got != "" {
		t.Fatalf("DSN with nothing = %q, want empty", got)
	}
}

// TestGetLastSuccessfulConfig: the most recent status='success' upsert wins, and
// a later 'failed' row does not shadow it. Skipped without a database.
func TestGetLastSuccessfulConfig(t *testing.T) {
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("TEST_DATABASE_URL not set; skipping PostgreSQL integration test")
	}
	ctx := context.Background()
	scopedDSN, cleanup := createIsolatedTestSchema(t, ctx, dsn, "last_successful")
	defer cleanup()
	d, err := New(ctx, scopedDSN)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer d.Close()

	const node, user = "n-succ", "7"
	if got, err := d.GetLastSuccessfulConfig(ctx, node, user); err != nil || got != nil {
		t.Fatalf("no success yet: got %v err %v, want (nil,nil)", got, err)
	}

	now := time.Now().UTC().Truncate(time.Second)
	v1 := HSIConfigRow{NodeUUID: node, UserID: user, Action: ActionUpsert,
		ConfigJSON: []byte(`{"v":1}`), DesireStatus: "connect", ModRevision: 100,
		ResourceVersion: "1", UpdatedBy: "admin", UpdatedAt: &now}
	if err := d.AppendHistoryWithStatus(ctx, v1, "success"); err != nil {
		t.Fatalf("append success v1: %v", err)
	}
	v2 := v1
	v2.ConfigJSON = []byte(`{"v":2}`)
	v2.ModRevision = 200
	v2.ResourceVersion = "2"
	if err := d.AppendHistoryWithStatus(ctx, v2, "success"); err != nil {
		t.Fatalf("append success v2: %v", err)
	}
	// A later failed row must not shadow the last success.
	fail := v1
	fail.ModRevision = 300
	if err := d.AppendHistoryWithStatus(ctx, fail, "failed"); err != nil {
		t.Fatalf("append failed: %v", err)
	}

	got, err := d.GetLastSuccessfulConfig(ctx, node, user)
	if err != nil {
		t.Fatalf("GetLastSuccessfulConfig: %v", err)
	}
	if got == nil {
		t.Fatal("want last successful config, got nil")
	}
	// config is a jsonb column (Postgres reformats whitespace), so compare the
	// decoded value, not the raw bytes.
	var decoded struct {
		V int `json:"v"`
	}
	if err := json.Unmarshal(got.ConfigJSON, &decoded); err != nil {
		t.Fatalf("unmarshal config %s: %v", got.ConfigJSON, err)
	}
	if decoded.V != 2 {
		t.Fatalf("config v = %d, want 2 (most recent success)", decoded.V)
	}
}
