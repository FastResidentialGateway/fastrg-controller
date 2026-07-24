package db

import (
	"context"
	"fmt"
	"net/url"
	"os"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

func TestDSNSpecialCharactersRoundTrip(t *testing.T) {
	tests := []struct {
		name     string
		host     string
		port     string
		user     string
		password string
		database string
		sslmode  string
		wantDSN  string
	}{
		{
			name: "password with reserved characters and space", host: "dbhost", port: "6543",
			user: "fastrg", password: "p@:/#% word", database: "fastrg", sslmode: "disable",
		},
		{
			name: "user and database with reserved characters", host: "dbhost", port: "6543",
			user: "user@name", password: "secret", database: "db/#% name", sslmode: "disable",
		},
		{
			name: "IPv6 host", host: "::1", port: "5432",
			user: "fastrg", password: "secret", database: "fastrg", sslmode: "disable",
		},
		{
			name: "legacy simple values", host: "dbhost", port: "6543",
			user: "fastrg", password: "secret", database: "fastrg", sslmode: "disable",
			wantDSN: "postgres://fastrg:secret@dbhost:6543/fastrg?sslmode=disable",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Setenv("DATABASE_URL", "")
			t.Setenv("POSTGRES_HOST", tt.host)
			t.Setenv("POSTGRES_PORT", tt.port)
			t.Setenv("POSTGRES_USER", tt.user)
			t.Setenv("POSTGRES_PASSWORD", tt.password)
			t.Setenv("POSTGRES_DB", tt.database)
			t.Setenv("POSTGRES_SSLMODE", tt.sslmode)

			got := DSN()
			if tt.wantDSN != "" && got != tt.wantDSN {
				t.Fatalf("DSN() = %q, want %q", got, tt.wantDSN)
			}
			if tt.host == "::1" && !strings.Contains(got, "@[::1]:5432/") {
				t.Fatalf("IPv6 DSN = %q, want bracketed host", got)
			}

			cfg, err := pgxpool.ParseConfig(got)
			if err != nil {
				t.Fatalf("ParseConfig(%q): %v", got, err)
			}
			wantPort, err := strconv.ParseUint(tt.port, 10, 16)
			if err != nil {
				t.Fatalf("invalid test port %q: %v", tt.port, err)
			}
			if cfg.ConnConfig.User != tt.user || cfg.ConnConfig.Password != tt.password ||
				cfg.ConnConfig.Host != tt.host || cfg.ConnConfig.Port != uint16(wantPort) ||
				cfg.ConnConfig.Database != tt.database {
				t.Fatalf("round trip = user %q password %q host %q port %d database %q; want user %q password %q host %q port %s database %q",
					cfg.ConnConfig.User, cfg.ConnConfig.Password, cfg.ConnConfig.Host, cfg.ConnConfig.Port, cfg.ConnConfig.Database,
					tt.user, tt.password, tt.host, tt.port, tt.database)
			}
		})
	}
}

func TestDSNSpecialPasswordConnects(t *testing.T) {
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("TEST_DATABASE_URL not set; skipping PostgreSQL integration test")
	}

	ctx := context.Background()
	adminPool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatalf("connect admin pool: %v", err)
	}
	defer adminPool.Close()
	if err := adminPool.Ping(ctx); err != nil {
		t.Fatalf("ping admin pool: %v", err)
	}

	baseConfig, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		t.Fatalf("parse TEST_DATABASE_URL: %v", err)
	}
	role := fmt.Sprintf("dsn_role_%d", time.Now().UnixNano())
	password := "dsn@:/#% password"
	quotedRole := quoteTask9Identifier(role)
	quotedPassword := quoteTask9Literal(password)
	quotedDatabase := quoteTask9Identifier(baseConfig.ConnConfig.Database)

	if _, err := adminPool.Exec(ctx, "CREATE ROLE "+quotedRole+" WITH LOGIN PASSWORD "+quotedPassword); err != nil {
		t.Fatalf("create test role: %v", err)
	}
	defer func() {
		cleanupCtx := context.Background()
		if _, err := adminPool.Exec(cleanupCtx, "REVOKE CONNECT ON DATABASE "+quotedDatabase+" FROM "+quotedRole); err != nil {
			t.Errorf("revoke test role: %v", err)
		}
		if _, err := adminPool.Exec(cleanupCtx, "DROP ROLE "+quotedRole); err != nil {
			t.Errorf("drop test role: %v", err)
		}
	}()
	if _, err := adminPool.Exec(ctx, "GRANT CONNECT ON DATABASE "+quotedDatabase+" TO "+quotedRole); err != nil {
		t.Fatalf("grant test role: %v", err)
	}

	sslmode := "disable"
	if parsed, err := url.Parse(dsn); err == nil && parsed.Query().Get("sslmode") != "" {
		sslmode = parsed.Query().Get("sslmode")
	}
	t.Setenv("DATABASE_URL", "")
	t.Setenv("POSTGRES_HOST", baseConfig.ConnConfig.Host)
	t.Setenv("POSTGRES_PORT", strconv.Itoa(int(baseConfig.ConnConfig.Port)))
	t.Setenv("POSTGRES_USER", role)
	t.Setenv("POSTGRES_PASSWORD", password)
	t.Setenv("POSTGRES_DB", baseConfig.ConnConfig.Database)
	t.Setenv("POSTGRES_SSLMODE", sslmode)

	rolePool, err := pgxpool.New(ctx, DSN())
	if err != nil {
		t.Fatalf("connect with escaped DSN: %v", err)
	}
	if err := rolePool.Ping(ctx); err != nil {
		rolePool.Close()
		t.Fatalf("ping with escaped DSN: %v", err)
	}
	rolePool.Close()
}

func TestConcurrentMigrations(t *testing.T) {
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("TEST_DATABASE_URL not set; skipping PostgreSQL integration test")
	}

	const (
		iterations = 3
		workers    = 5
	)
	ctx := context.Background()
	for iteration := 0; iteration < iterations; iteration++ {
		scopedDSN, cleanup := createTask9TestSchema(t, ctx, dsn, iteration)

		start := make(chan struct{})
		errs := make(chan error, workers)
		var wg sync.WaitGroup
		wg.Add(workers)
		for worker := 0; worker < workers; worker++ {
			go func() {
				defer wg.Done()
				<-start
				d, err := New(ctx, scopedDSN)
				if err == nil {
					d.Close()
				}
				errs <- err
			}()
		}
		close(start)
		wg.Wait()
		close(errs)

		for err := range errs {
			if err != nil {
				cleanup()
				t.Fatalf("iteration %d concurrent New: %v", iteration, err)
			}
		}

		pool, err := pgxpool.New(ctx, scopedDSN)
		if err != nil {
			cleanup()
			t.Fatalf("iteration %d inspect migrations: %v", iteration, err)
		}
		entries, err := migrationFS.ReadDir("migrations")
		if err != nil {
			pool.Close()
			cleanup()
			t.Fatalf("read embedded migrations: %v", err)
		}
		wantVersions := 0
		for _, entry := range entries {
			if !entry.IsDir() && strings.HasSuffix(entry.Name(), ".sql") {
				wantVersions++
			}
		}
		var versions, duplicateVersions int
		if err := pool.QueryRow(ctx, `SELECT count(*), count(*) - count(DISTINCT version) FROM schema_migrations`).Scan(&versions, &duplicateVersions); err != nil {
			pool.Close()
			cleanup()
			t.Fatalf("iteration %d count migrations: %v", iteration, err)
		}
		pool.Close()
		cleanup()
		if versions != wantVersions || duplicateVersions != 0 {
			t.Fatalf("iteration %d schema_migrations = %d rows, %d duplicates; want %d rows, no duplicates",
				iteration, versions, duplicateVersions, wantVersions)
		}
	}
}

func createTask9TestSchema(t *testing.T, ctx context.Context, dsn string, iteration int) (string, func()) {
	t.Helper()
	parsed, err := url.Parse(dsn)
	if err != nil || parsed.Scheme == "" {
		t.Fatalf("TEST_DATABASE_URL must be a PostgreSQL URL: %v", err)
	}
	schema := fmt.Sprintf("migration_retry_%d_%d", iteration, time.Now().UnixNano())
	quotedSchema := quoteTask9Identifier(schema)

	adminPool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatalf("connect admin pool: %v", err)
	}
	if _, err := adminPool.Exec(ctx, "CREATE SCHEMA "+quotedSchema); err != nil {
		adminPool.Close()
		t.Fatalf("create test schema: %v", err)
	}

	query := parsed.Query()
	query.Set("search_path", schema)
	parsed.RawQuery = query.Encode()
	cleanup := func() {
		defer adminPool.Close()
		if _, err := adminPool.Exec(context.Background(), "DROP SCHEMA "+quotedSchema+" CASCADE"); err != nil {
			t.Errorf("drop test schema: %v", err)
		}
	}
	return parsed.String(), cleanup
}

func quoteTask9Identifier(value string) string {
	return `"` + strings.ReplaceAll(value, `"`, `""`) + `"`
}

func quoteTask9Literal(value string) string {
	return `'` + strings.ReplaceAll(value, `'`, `''`) + `'`
}
