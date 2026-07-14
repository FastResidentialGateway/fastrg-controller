// Package db implements the controller's optional PostgreSQL read model. The
// config-watch projection appends pending/delete history rows and stores its
// etcd watch checkpoint. The Kafka consumer writes node-confirmed current
// config, success/failed history rows, PPPoE status, node events, and its DLQ.
// REST and gRPC handlers write config only to etcd, never directly to these
// tables.
package db

import (
	"context"
	"embed"
	"fmt"
	"os"
	"sort"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/sirupsen/logrus"
)

//go:embed migrations/*.sql
var migrationFS embed.FS

// DB wraps a pgx connection pool.
type DB struct {
	pool *pgxpool.Pool
}

// DSN returns the configured PostgreSQL connection string, or "" when no
// database is configured (in which case the controller runs without the
// projection). DATABASE_URL takes precedence; otherwise it is assembled from
// POSTGRES_* parts when POSTGRES_HOST is set.
func DSN() string {
	if url := os.Getenv("DATABASE_URL"); url != "" {
		return url
	}
	host := os.Getenv("POSTGRES_HOST")
	if host == "" {
		return ""
	}
	port := envOr("POSTGRES_PORT", "5432")
	user := envOr("POSTGRES_USER", "fastrg")
	pass := envOr("POSTGRES_PASSWORD", "fastrg")
	name := envOr("POSTGRES_DB", "fastrg")
	sslmode := envOr("POSTGRES_SSLMODE", "disable")
	return fmt.Sprintf("postgres://%s:%s@%s:%s/%s?sslmode=%s", user, pass, host, port, name, sslmode)
}

func envOr(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

// New connects to PostgreSQL using dsn, verifies the connection, and applies
// any pending migrations.
func New(ctx context.Context, dsn string) (*DB, error) {
	cfg, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		return nil, fmt.Errorf("parse dsn: %w", err)
	}

	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		return nil, fmt.Errorf("connect: %w", err)
	}

	pingCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	if err := pool.Ping(pingCtx); err != nil {
		pool.Close()
		return nil, fmt.Errorf("ping: %w", err)
	}

	d := &DB{pool: pool}
	if err := d.migrate(ctx); err != nil {
		pool.Close()
		return nil, fmt.Errorf("migrate: %w", err)
	}
	return d, nil
}

// Close releases the connection pool.
func (d *DB) Close() {
	d.pool.Close()
}

// migrate applies every embedded migration not yet recorded in
// schema_migrations, in filename order, each inside its own transaction.
func (d *DB) migrate(ctx context.Context) error {
	if _, err := d.pool.Exec(ctx,
		`CREATE TABLE IF NOT EXISTS schema_migrations (version TEXT PRIMARY KEY, applied_at TIMESTAMPTZ NOT NULL DEFAULT now())`,
	); err != nil {
		return err
	}

	entries, err := migrationFS.ReadDir("migrations")
	if err != nil {
		return err
	}
	names := make([]string, 0, len(entries))
	for _, e := range entries {
		if !e.IsDir() && strings.HasSuffix(e.Name(), ".sql") {
			names = append(names, e.Name())
		}
	}
	sort.Strings(names)

	for _, name := range names {
		var exists bool
		if err := d.pool.QueryRow(ctx,
			`SELECT EXISTS(SELECT 1 FROM schema_migrations WHERE version = $1)`, name,
		).Scan(&exists); err != nil {
			return err
		}
		if exists {
			continue
		}

		sqlBytes, err := migrationFS.ReadFile("migrations/" + name)
		if err != nil {
			return err
		}

		tx, err := d.pool.Begin(ctx)
		if err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, string(sqlBytes)); err != nil {
			_ = tx.Rollback(ctx)
			return fmt.Errorf("apply %s: %w", name, err)
		}
		if _, err := tx.Exec(ctx, `INSERT INTO schema_migrations (version) VALUES ($1)`, name); err != nil {
			_ = tx.Rollback(ctx)
			return err
		}
		if err := tx.Commit(ctx); err != nil {
			return err
		}
		logrus.Infof("Applied DB migration %s", name)
	}
	return nil
}
