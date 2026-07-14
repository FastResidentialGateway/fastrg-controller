package db

import (
	"context"
	"errors"
	"os"
	"testing"

	"github.com/jackc/pgx/v5/pgconn"
)

func TestStatementTimeout(t *testing.T) {
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("TEST_DATABASE_URL not set; skipping PostgreSQL integration test")
	}

	ctx := context.Background()
	scopedDSN, cleanup := createIsolatedTestSchema(t, ctx, dsn, "statement_timeout")
	defer cleanup()
	d, err := New(ctx, scopedDSN)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer d.Close()

	var configured string
	if err := d.pool.QueryRow(ctx, "SHOW statement_timeout").Scan(&configured); err != nil {
		t.Fatalf("SHOW statement_timeout: %v", err)
	}
	if configured != "30s" {
		t.Fatalf("statement_timeout = %q, want 30s", configured)
	}

	tx, err := d.pool.Begin(ctx)
	if err != nil {
		t.Fatalf("begin timeout transaction: %v", err)
	}
	defer tx.Rollback(ctx)
	if _, err := tx.Exec(ctx, "SET LOCAL statement_timeout = '200ms'"); err != nil {
		t.Fatalf("set local statement_timeout: %v", err)
	}
	if _, err := tx.Exec(ctx, "SELECT pg_sleep(1)"); err == nil {
		t.Fatal("pg_sleep completed, want statement timeout")
	} else {
		var pgErr *pgconn.PgError
		if !errors.As(err, &pgErr) || pgErr.Code != "57014" {
			t.Fatalf("pg_sleep error = %v, want SQLSTATE 57014", err)
		}
	}
}
