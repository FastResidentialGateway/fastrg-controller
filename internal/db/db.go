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
	"net"
	"net/url"
	"os"
	"sort"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/sirupsen/logrus"
)

//go:embed migrations/*.sql
var migrationFS embed.FS

// migrationAdvisoryLockKey is the fixed PostgreSQL advisory-lock identifier
// used by every FastRG Controller instance while applying migrations. The
// hexadecimal value is the ASCII encoding of "FastRG".
const migrationAdvisoryLockKey int64 = 0x466173745247

const (
	connectRetryInitialDelay = 2 * time.Second
	connectRetryMaxDelay     = 30 * time.Second
)

// DB wraps a pgx connection pool.
type DB struct {
	pool *pgxpool.Pool
}

// DSN returns the configured PostgreSQL connection string, or "" when no
// database is configured (in which case the controller runs without the
// projection). DATABASE_URL takes precedence; otherwise it is assembled from
// POSTGRES_* parts when POSTGRES_HOST is set. DATABASE_URL is returned as-is;
// callers providing it are responsible for supplying an already escaped URL.
func DSN() string {
	if databaseURL := os.Getenv("DATABASE_URL"); databaseURL != "" {
		return databaseURL
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
	u := url.URL{
		Scheme:   "postgres",
		User:     url.UserPassword(user, pass),
		Host:     net.JoinHostPort(host, port),
		Path:     "/" + name,
		RawQuery: "sslmode=" + url.QueryEscape(sslmode),
	}
	return u.String()
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

// ConnectLoop keeps trying to establish the controller's optional PostgreSQL
// read model until it succeeds or ctx is cancelled. Once connected, onReady is
// called exactly once. The connection remains available until ctx is cancelled,
// at which point its pool is closed and ConnectLoop returns.
func ConnectLoop(ctx context.Context, dsn string, onReady func(*DB)) {
	delay := connectRetryInitialDelay
	for attempt := 1; ; attempt++ {
		database, err := New(ctx, dsn)
		if err == nil {
			if ctx.Err() != nil {
				database.Close()
				return
			}

			logrus.Info("Connected to PostgreSQL; enabling database-backed components")
			onReady(database)
			<-ctx.Done()
			database.Close()
			return
		}
		if ctx.Err() != nil {
			return
		}

		logrus.WithError(err).Warnf(
			"PostgreSQL not ready, retrying in %s (attempt %d)", delay, attempt,
		)
		timer := time.NewTimer(delay)
		select {
		case <-ctx.Done():
			timer.Stop()
			return
		case <-timer.C:
		}
		delay = min(delay*2, connectRetryMaxDelay)
	}
}

// Close releases the connection pool.
func (d *DB) Close() {
	d.pool.Close()
}

// migrate applies every embedded migration not yet recorded in
// schema_migrations, in filename order, each inside its own transaction.
func (d *DB) migrate(ctx context.Context) error {
	// PostgreSQL advisory locks belong to a session. Keep a dedicated pooled
	// connection for the lock, every migration operation, and the unlock;
	// separate pool calls could use different sessions or expose the locked
	// session to unrelated work before migration completes.
	conn, err := d.pool.Acquire(ctx)
	if err != nil {
		return err
	}
	defer conn.Release()

	if _, err := conn.Exec(ctx, `SELECT pg_advisory_lock($1)`, migrationAdvisoryLockKey); err != nil {
		return fmt.Errorf("acquire migration advisory lock: %w", err)
	}
	defer func() {
		// Unlock must still run when the caller's context is canceled after the
		// lock was acquired. PostgreSQL also releases the lock if this session
		// has already disconnected.
		unlockCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		var unlocked bool
		if err := conn.QueryRow(unlockCtx,
			`SELECT pg_advisory_unlock($1)`, migrationAdvisoryLockKey,
		).Scan(&unlocked); err != nil {
			logrus.Warnf("Failed to release DB migration advisory lock: %v", err)
		} else if !unlocked {
			logrus.Warn("DB migration advisory lock was not held during release")
		}
	}()

	if _, err := conn.Exec(ctx,
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
		if err := conn.QueryRow(ctx,
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

		tx, err := conn.Begin(ctx)
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
