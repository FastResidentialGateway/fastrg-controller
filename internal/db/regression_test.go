package db

import (
	"context"
	"os"
	"testing"
)

// TestSendToDLQReservedOffset is a regression guard for the kafka_dlq INSERT.
// The column "offset" is a PostgreSQL reserved word and MUST be quoted in the
// INSERT column list. An unquoted offset makes SendToDLQ fail with
// "syntax error at or near offset" (SQLSTATE 42601), which silently breaks the
// dead-letter path so failed Kafka messages can never be parked. That path is
// only exercised when a message fails repeatedly, so the bug stays dormant on
// the happy path — exactly why it slipped through until a rollback storm hit it.
//
// Skipped unless TEST_DATABASE_URL points at a throwaway PostgreSQL.
func TestSendToDLQReservedOffset(t *testing.T) {
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("TEST_DATABASE_URL not set; skipping PostgreSQL integration test")
	}
	ctx := context.Background()
	d, err := New(ctx, dsn)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer d.Close()
	if _, err := d.pool.Exec(ctx, "TRUNCATE kafka_dlq"); err != nil {
		t.Fatalf("truncate kafka_dlq: %v", err)
	}

	// First park must succeed. This is the call that fails outright when the
	// reserved word is unquoted in the INSERT column list.
	id1, err := d.SendToDLQ(ctx, "fastrg.node.events", 0, 4006, []byte("payload"), "boom")
	if err != nil {
		t.Fatalf("SendToDLQ first: %v", err)
	}
	if id1 == 0 {
		t.Fatalf("SendToDLQ first returned id 0")
	}

	// Re-park the SAME (topic, partition, offset): the ON CONFLICT branch must
	// update in place and bump retry_count rather than erroring or inserting anew.
	id2, err := d.SendToDLQ(ctx, "fastrg.node.events", 0, 4006, []byte("payload"), "boom again")
	if err != nil {
		t.Fatalf("SendToDLQ duplicate: %v", err)
	}
	if id2 != id1 {
		t.Fatalf("SendToDLQ duplicate returned id %d, want same row %d", id2, id1)
	}

	var retry, rows int
	if err := d.pool.QueryRow(ctx,
		`SELECT retry_count FROM kafka_dlq WHERE topic=$1 AND partition=$2 AND "offset"=$3`,
		"fastrg.node.events", 0, 4006).Scan(&retry); err != nil {
		t.Fatalf("select retry_count: %v", err)
	}
	if retry != 1 {
		t.Fatalf("retry_count = %d, want 1 after one re-park", retry)
	}
	if err := d.pool.QueryRow(ctx, "SELECT count(*) FROM kafka_dlq").Scan(&rows); err != nil {
		t.Fatalf("count kafka_dlq: %v", err)
	}
	if rows != 1 {
		t.Fatalf("kafka_dlq rows = %d, want 1 (re-park must not insert a new row)", rows)
	}
}

// TestRollbackToLastSuccessfulIdempotent is a regression guard for the rollback
// history insert. RollbackToLastSuccessful records a failed attempt with a
// hard-coded mod_revision=0 and status='failed'. The unique index
// idx_hsi_config_history_idempotency is (node_uuid, user_id, mod_revision,
// status), so the SECOND apply-failure for the same user collides on that key.
// Without ON CONFLICT DO NOTHING the second call returns SQLSTATE 23505, which
// the Kafka consumer treats as a handler error and retries forever, wedging the
// whole consumer. A single failure works fine, so this only bites when a user
// fails to apply more than once — a real but easily-missed edge case.
//
// Skipped unless TEST_DATABASE_URL points at a throwaway PostgreSQL.
func TestRollbackToLastSuccessfulIdempotent(t *testing.T) {
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("TEST_DATABASE_URL not set; skipping PostgreSQL integration test")
	}
	ctx := context.Background()
	d, err := New(ctx, dsn)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer d.Close()
	if _, err := d.pool.Exec(ctx, "TRUNCATE hsi_config_history"); err != nil {
		t.Fatalf("truncate hsi_config_history: %v", err)
	}

	// First apply-failure for (node1, user 2): records one failed history row.
	if err := d.RollbackToLastSuccessful(ctx, "node1", "2", "bad vlan"); err != nil {
		t.Fatalf("RollbackToLastSuccessful first: %v", err)
	}
	// Second apply-failure for the SAME user: same (node, user, mod_revision=0,
	// status=failed) key. Must be a no-op, NOT a duplicate-key error.
	if err := d.RollbackToLastSuccessful(ctx, "node1", "2", "still bad"); err != nil {
		t.Fatalf("RollbackToLastSuccessful second (duplicate-key regression): %v", err)
	}

	var failed int
	if err := d.pool.QueryRow(ctx,
		"SELECT count(*) FROM hsi_config_history WHERE node_uuid=$1 AND user_id=$2 AND status='failed'",
		"node1", "2").Scan(&failed); err != nil {
		t.Fatalf("count failed history: %v", err)
	}
	if failed != 1 {
		t.Fatalf("failed history rows = %d, want 1 (second rollback must be deduped)", failed)
	}
}
