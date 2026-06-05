package kafka

import (
	"context"
	"os"
	"strconv"
	"testing"
	"time"

	"fastrg-controller/internal/db"
	eventsv1 "fastrg-controller/proto/eventsv1"

	"github.com/jackc/pgx/v5/pgxpool"
	kafkago "github.com/segmentio/kafka-go"
	"google.golang.org/protobuf/proto"
)

// TestConsumerEndToEnd produces protobuf NodeEvents to a real Kafka topic, runs
// the consumer, and asserts they land in pppoe_status / node_events. Skipped
// unless TEST_KAFKA_BROKERS and TEST_DATABASE_URL are set.
func TestConsumerEndToEnd(t *testing.T) {
	brokers := os.Getenv("TEST_KAFKA_BROKERS")
	dsn := os.Getenv("TEST_DATABASE_URL")
	if brokers == "" || dsn == "" {
		t.Skip("TEST_KAFKA_BROKERS / TEST_DATABASE_URL not set; skipping Kafka e2e")
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Unique topic + group per run so repeated runs against the same broker are
	// fully isolated (no stale committed offsets or group-member rebalance waits).
	suffix := strconv.FormatInt(time.Now().UnixNano(), 10)
	topic := "fastrg.node.events.test." + suffix
	t.Setenv("KAFKA_TOPIC", topic)
	t.Setenv("KAFKA_GROUP", "fastrg-controller-test."+suffix)

	database, err := db.New(ctx, dsn)
	if err != nil {
		t.Fatalf("db: %v", err)
	}
	defer database.Close()

	// Own pool for test setup (db.DB's pool is private).
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatalf("assert pool: %v", err)
	}
	defer pool.Close()
	for _, tbl := range []string{"pppoe_status", "node_events"} {
		if _, err := pool.Exec(ctx, "TRUNCATE "+tbl); err != nil {
			t.Fatalf("truncate %s: %v", tbl, err)
		}
	}

	// Produce three events (auto-creating the topic).
	w := &kafkago.Writer{
		Addr:                   kafkago.TCP(brokers),
		Topic:                  topic,
		AllowAutoTopicCreation: true,
		BatchTimeout:           50 * time.Millisecond,
	}
	defer w.Close()

	now := time.Now().Unix()
	events := []*eventsv1.NodeEvent{
		{
			NodeUuid: "n1", UserId: "2", Type: eventsv1.EventType_EVENT_TYPE_PPPOE_CONNECTED,
			Timestamp: now,
			Payload: &eventsv1.NodeEvent_PppoeStateChange{PppoeStateChange: &eventsv1.PPPoEStateChange{
				Phase: eventsv1.PPPoEPhase_PPPOE_PHASE_CONNECTED, HsiIpv4: "10.0.0.9",
			}},
		},
		{
			NodeUuid: "n1", UserId: "2", Type: eventsv1.EventType_EVENT_TYPE_CONFIG_APPLY_FAIL,
			Timestamp: now, CorrelationId: "1234",
			Payload: &eventsv1.NodeEvent_ConfigApplyResult{ConfigApplyResult: &eventsv1.ConfigApplyResult{
				Action: "update", Success: false, ErrorCode: "EINVAL", ErrorMessage: "bad vlan",
			}},
		},
		{
			NodeUuid: "n2", UserId: "0", Type: eventsv1.EventType_EVENT_TYPE_RUNTIME_ERROR,
			Timestamp: now,
			Payload: &eventsv1.NodeEvent_RuntimeError{RuntimeError: &eventsv1.RuntimeError{
				Module: "pppd", ErrorMessage: "link down",
			}},
		},
	}

	var msgs []kafkago.Message
	for _, e := range events {
		b, err := proto.Marshal(e)
		if err != nil {
			t.Fatalf("marshal: %v", err)
		}
		msgs = append(msgs, kafkago.Message{Key: []byte(e.NodeUuid), Value: b})
	}
	// Writing may briefly fail until the auto-created topic's metadata propagates.
	if err := writeWithRetry(ctx, w, msgs); err != nil {
		t.Fatalf("produce: %v", err)
	}

	// Run the consumer.
	go NewConsumer([]string{brokers}, database, nil).Run(ctx)

	// PPPoE status projected.
	waitFor(t, 25*time.Second, func() bool {
		st, ok, _ := database.GetPPPoEStatus(ctx, "n1", "2")
		return ok && st.Phase == "connected" && st.HSIIPv4 == "10.0.0.9"
	}, "pppoe_status to be projected")

	// Both node events projected.
	waitFor(t, 25*time.Second, func() bool {
		rows, _ := database.ListNodeEvents(ctx, "", "", 0)
		return len(rows) == 2
	}, "node_events to be projected")

	fail, _ := database.ListNodeEvents(ctx, "n1", "CONFIG_APPLY_FAIL", 0)
	if len(fail) != 1 || fail[0].ErrorCode != "EINVAL" || fail[0].CorrelationID != "1234" {
		t.Fatalf("config-apply-fail event wrong: %+v", fail)
	}
	rt, _ := database.ListNodeEvents(ctx, "n2", "RUNTIME_ERROR", 0)
	if len(rt) != 1 || rt[0].Module != "pppd" {
		t.Fatalf("runtime-error event wrong: %+v", rt)
	}
}

func writeWithRetry(ctx context.Context, w *kafkago.Writer, msgs []kafkago.Message) error {
	deadline := time.Now().Add(20 * time.Second)
	var err error
	for time.Now().Before(deadline) {
		if err = w.WriteMessages(ctx, msgs...); err == nil {
			return nil
		}
		time.Sleep(500 * time.Millisecond)
	}
	return err
}

func waitFor(t *testing.T, timeout time.Duration, cond func() bool, what string) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(200 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", what)
}
