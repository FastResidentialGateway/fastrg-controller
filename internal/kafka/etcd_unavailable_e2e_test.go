package kafka

import (
	"context"
	"os"
	"strconv"
	"testing"
	"time"

	"fastrg-controller/internal/db"
	"fastrg-controller/internal/storage"

	"github.com/jackc/pgx/v5/pgxpool"
)

// TestConsumerRetriesEtcdFailureWithoutDLQ verifies that an infrastructure
// failure during rollback stalls the Kafka message instead of committing it to
// the DLQ. localhost:1 is a privileged port that is expected to refuse
// connections; use another confirmed-closed port if a service owns it.
func TestConsumerRetriesEtcdFailureWithoutDLQ(t *testing.T) {
	brokers := os.Getenv("TEST_KAFKA_BROKERS")
	dsn := os.Getenv("TEST_DATABASE_URL")
	if brokers == "" || dsn == "" {
		t.Skip("TEST_KAFKA_BROKERS / TEST_DATABASE_URL not set; skipping etcd-unavailable Kafka e2e")
	}

	ctx, cancel := context.WithCancel(context.Background())

	database, err := db.New(ctx, dsn)
	if err != nil {
		cancel()
		t.Fatalf("db: %v", err)
	}

	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		database.Close()
		cancel()
		t.Fatalf("assert pool: %v", err)
	}

	t.Setenv("ETCD_ENDPOINTS", "localhost:1")
	etcd, err := storage.NewEtcdClient()
	if err != nil {
		pool.Close()
		database.Close()
		cancel()
		t.Fatalf("etcd client: %v", err)
	}

	// Cancel the consumer before closing its dependencies, including when an
	// assertion terminates the test early.
	t.Cleanup(etcd.Close)
	t.Cleanup(pool.Close)
	t.Cleanup(database.Close)
	t.Cleanup(cancel)

	suffix := strconv.FormatInt(time.Now().UnixNano(), 10)
	topic := "fastrg.node.events.etcd-unavailable." + suffix
	group := "fastrg-controller-etcd-unavailable." + suffix
	node := "etcd-unavailable-" + suffix
	user := "701"
	t.Setenv("KAFKA_TOPIC", topic)
	t.Setenv("KAFKA_GROUP", group)

	event := configApplyEvent(node, user, false, time.Now().Unix())
	event.CorrelationId = "etcd-unavailable-" + suffix
	produceRollbackEvents(t, ctx, brokers, topic, event)

	done := make(chan struct{})
	consumer := NewConsumer([]string{brokers}, database, etcd)
	go func() {
		defer close(done)
		consumer.Run(ctx)
	}()

	waitFor(t, 25*time.Second, func() bool {
		return rollbackNodeEventCount(ctx, pool, node, user, "CONFIG_APPLY_FAIL") == 1
	}, "CONFIG_APPLY_FAIL node event before etcd rollback")

	// This observation interval exceeds the old five-attempt backoff budget.
	// The corrected path remains on the same message and does not commit it.
	timer := time.NewTimer(5 * time.Second)
	select {
	case <-timer.C:
	case <-ctx.Done():
		t.Fatal("consumer context ended before retry observation completed")
	}

	var dlqRows int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM kafka_dlq WHERE topic = $1`, topic).Scan(&dlqRows); err != nil {
		t.Fatalf("count DLQ rows for %s: %v", topic, err)
	}
	if dlqRows != 0 {
		t.Fatalf("kafka_dlq rows for %s = %d, want 0", topic, dlqRows)
	}

	var eventRows int
	if err := pool.QueryRow(ctx, `
		SELECT count(*) FROM node_events
		WHERE node_uuid = $1 AND user_id = $2 AND correlation_id = $3`,
		node, user, event.GetCorrelationId(),
	).Scan(&eventRows); err != nil {
		t.Fatalf("count node events: %v", err)
	}
	if eventRows != 1 {
		t.Fatalf("node_events rows for %s/%s = %d, want 1", node, user, eventRows)
	}

	cancel()
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("timed out stopping Kafka consumer")
	}
}
