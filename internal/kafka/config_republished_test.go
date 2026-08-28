package kafka

import (
	"context"
	"os"
	"testing"
	"time"

	"fastrg-controller/internal/db"
	eventsv1 "fastrg-controller/proto/eventsv1"

	"github.com/jackc/pgx/v5/pgxpool"
	"google.golang.org/protobuf/proto"
)

// republishedApplyEnv is a PostgreSQL-only consumer: etcd is deliberately absent
// because these cases are about which audit rows get written, not about the
// current-config refresh that needs etcd.
type republishedApplyEnv struct {
	ctx      context.Context
	consumer *Consumer
	pool     *pgxpool.Pool
}

func newRepublishedApplyEnv(t *testing.T) *republishedApplyEnv {
	t.Helper()
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("TEST_DATABASE_URL not set; skipping republished config-apply projection test")
	}

	ctx, cancel := context.WithCancel(context.Background())
	scopedDSN, dropSchema := createTask12KafkaSchema(t, ctx, dsn, "republished_apply")

	database, err := db.New(ctx, scopedDSN)
	if err != nil {
		cancel()
		dropSchema()
		t.Fatalf("db: %v", err)
	}
	pool, err := pgxpool.New(ctx, scopedDSN)
	if err != nil {
		database.Close()
		cancel()
		dropSchema()
		t.Fatalf("pool: %v", err)
	}

	t.Cleanup(func() {
		pool.Close()
		database.Close()
		cancel()
		dropSchema()
	})

	return &republishedApplyEnv{
		ctx:      ctx,
		consumer: &Consumer{db: database},
		pool:     pool,
	}
}

// nodeEventCount counts the audit rows recorded for one (node, user).
func (e *republishedApplyEnv) nodeEventCount(t *testing.T, node, user string) int {
	t.Helper()
	var count int
	err := e.pool.QueryRow(e.ctx,
		`SELECT count(*) FROM node_events WHERE node_uuid = $1 AND user_id = $2`,
		node, user,
	).Scan(&count)
	if err != nil {
		t.Fatalf("count node_events: %v", err)
	}
	return count
}

// republishedApplyEvent builds a CONFIG_APPLY_OK/FAIL carrying the republished
// flag.
func republishedApplyEvent(node, user string, success, republished bool, timestamp int64) []byte {
	event := configApplyEvent(node, user, success, timestamp)
	event.GetConfigApplyResult().Republished = republished
	value, err := proto.Marshal(event)
	if err != nil {
		panic(err)
	}
	return value
}

// TestRepublishedSuccessWritesNoAuditRow: a node restating a config it was
// already running must not add to the audit trail — a sweep over every
// subscriber would otherwise fill node_events with transitions that never
// happened.
func TestRepublishedSuccessWritesNoAuditRow(t *testing.T) {
	env := newRepublishedApplyEnv(t)
	const node, user = "republished-restate", "301"

	value := republishedApplyEvent(node, user, true, true, time.Now().Unix())
	if err := env.consumer.handle(env.ctx, value); err != nil {
		t.Fatalf("handle republished success: %v", err)
	}
	if got := env.nodeEventCount(t, node, user); got != 0 {
		t.Fatalf("node_events rows = %d, want 0 for a restated config", got)
	}
}

// TestOrdinarySuccessStillWritesAuditRow: without the flag the behaviour is
// unchanged, so the restate path cannot silently swallow real results.
func TestOrdinarySuccessStillWritesAuditRow(t *testing.T) {
	env := newRepublishedApplyEnv(t)
	const node, user = "republished-ordinary", "302"

	value := republishedApplyEvent(node, user, true, false, time.Now().Unix())
	if err := env.consumer.handle(env.ctx, value); err != nil {
		t.Fatalf("handle ordinary success: %v", err)
	}
	if got := env.nodeEventCount(t, node, user); got != 1 {
		t.Fatalf("node_events rows = %d, want 1 for a real apply result", got)
	}
}

// TestRepublishedFailureIsStillAudited: the flag only suppresses the audit row
// for a restated success. A failure is a real apply attempt whatever the flag
// says, so it keeps its audit row and its rollback.
func TestRepublishedFailureIsStillAudited(t *testing.T) {
	env := newRepublishedApplyEnv(t)
	const node, user = "republished-failure", "303"

	value := republishedApplyEvent(node, user, false, true, time.Now().Unix())
	if err := env.consumer.handle(env.ctx, value); err != nil {
		t.Fatalf("handle republished failure: %v", err)
	}
	if got := env.nodeEventCount(t, node, user); got != 1 {
		t.Fatalf("node_events rows = %d, want 1 for a failed apply", got)
	}

	var failedHistory int
	err := env.pool.QueryRow(env.ctx,
		`SELECT count(*) FROM hsi_config_history WHERE node_uuid = $1 AND user_id = $2 AND status = 'failed'`,
		node, user,
	).Scan(&failedHistory)
	if err != nil {
		t.Fatalf("count failed history: %v", err)
	}
	if failedHistory != 1 {
		t.Fatalf("failed history rows = %d, want 1", failedHistory)
	}
}

// TestRepublishedFlagDefaultsToFalse: an older node that does not know the field
// leaves it unset, which must read as "a real apply result".
func TestRepublishedFlagDefaultsToFalse(t *testing.T) {
	event := configApplyEvent("old-node", "304", true, time.Now().Unix())
	encoded, err := proto.Marshal(event)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var decoded eventsv1.NodeEvent
	if err := proto.Unmarshal(encoded, &decoded); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if decoded.GetConfigApplyResult().GetRepublished() {
		t.Fatal("republished defaulted to true; an older node's event must read as false")
	}
}
