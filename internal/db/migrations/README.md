# Database Migrations

## Current Status: Consolidated

**As of June 5, 2026**: All migrations have been consolidated into a single file `0001_init_complete.sql` since no production environments are currently using the database.

## File Structure

```
migrations/
└── 0001_init_complete.sql     ← Single consolidated migration (all tables + indices)
```

## How Migrations Work

1. **Automatic Application**: Each migration file in `migrations/` is automatically applied on first `DB.New()` connection
2. **Idempotency**: All operations use `IF NOT EXISTS` to ensure migrations can be re-run safely
3. **Tracking**: Applied migrations are recorded in the `schema_migrations` table to prevent re-application

## When Consolidating Multiple Migrations

The controller uses Go's `//go:embed migrations/*.sql` to bundle all SQL files into the binary:
- All `.sql` files in this directory are automatically embedded
- On `DB.New()`, they are applied in filename order
- Already-applied migrations (tracked in `schema_migrations`) are skipped

## If Production Deployment is Needed

When deploying to production:

1. **If schema_migrations is empty**: All migrations apply fresh (normal deployment)
2. **If schema_migrations has old entries**: Controller checks each migration name; only unapplied ones run
3. **If schema_migrations has old split-migration entries**: confirm whether that
   environment was created from a pre-consolidation build before deploying this
   migration layout. The current repository intentionally keeps only the
   consolidated migration because the database feature has not been used outside
   test environments.

## Adding New Features

**Future migrations** (after production deployment becomes relevant):

1. Create new file: `0002_your_feature.sql`
2. Implement your schema changes with `IF NOT EXISTS`
3. Place in this `migrations/` directory
4. On next deployment, it will be automatically picked up and applied

Example:
```sql
-- 0002_add_audit_log.sql
CREATE TABLE IF NOT EXISTS audit_log (
    id BIGSERIAL PRIMARY KEY,
    event_type TEXT NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX IF NOT EXISTS idx_audit_log_event_type
    ON audit_log (event_type);
```

## Best Practices

✓ **Never delete or modify** an already-applied migration file after a real environment has used it
✓ **Always use** `IF NOT EXISTS` for idempotency
✓ **One change per file** for clarity
✓ **Test locally** before deploying (run `docker-compose down && docker-compose up`)
✓ **Consolidate only before real environments have applied the migration history**

## Schema Overview

The consolidated migration creates:

| Table | Purpose |
|-------|---------|
| `hsi_config_current` | Latest config state per (node, user) |
| `hsi_config_history` | Append-only audit log of config changes |
| `etcd_watch_progress` | Watch checkpoint for config projection |
| `pppoe_status` | Latest PPPoE state per (node, user) |
| `node_events` | Node telemetry events (config apply, PPPoE state, errors) |
| `kafka_dlq` | Dead-letter queue for failed Kafka messages |
| `schema_migrations` | Migration version tracking (created automatically) |
