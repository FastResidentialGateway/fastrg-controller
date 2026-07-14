package kafka

import (
	"context"
	"net"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	"fastrg-controller/internal/db"
	"fastrg-controller/internal/storage"
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
	etcdEndpoints := os.Getenv("TEST_ETCD_ENDPOINTS")
	if brokers == "" || dsn == "" || etcdEndpoints == "" {
		t.Skip("TEST_KAFKA_BROKERS / TEST_DATABASE_URL / TEST_ETCD_ENDPOINTS not set; skipping Kafka e2e")
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Real etcd client so the CONFIG_APPLY_FAIL rollback path actually runs
	// (passing nil here previously short-circuited the whole rollback/CAS logic).
	t.Setenv("ETCD_ENDPOINTS", etcdEndpoints)
	etcd, err := storage.NewEtcdClient()
	if err != nil {
		t.Fatalf("etcd connect: %v", err)
	}
	defer etcd.Close()

	// Unique topic + group per run so repeated runs against the same broker are
	// fully isolated (no stale committed offsets or group-member rebalance waits).
	suffix := strconv.FormatInt(time.Now().UnixNano(), 10)
	topic := "fastrg.node.events.test." + suffix
	t.Setenv("KAFKA_TOPIC", topic)
	t.Setenv("KAFKA_GROUP", "fastrg-controller-test."+suffix)

	scopedDSN, cleanup := createTask12KafkaSchema(t, ctx, dsn, "consumer_e2e")
	defer cleanup()
	database, err := db.New(ctx, scopedDSN)
	if err != nil {
		t.Fatalf("db: %v", err)
	}
	defer database.Close()

	// Own pool for assertions (db.DB's pool is private).
	pool, err := pgxpool.New(ctx, scopedDSN)
	if err != nil {
		t.Fatalf("assert pool: %v", err)
	}
	defer pool.Close()

	// Seed a config for (n1, user 2) in etcd. The CONFIG_APPLY_FAIL event below has
	// no prior successful version in history, so the consumer's rollback must
	// DELETE it — this exercises the etcd CAS rollback path end to end.
	if _, err := etcd.Client().Put(ctx, "configs/n1/hsi/2",
		`{"config":{"user_id":"2","vlan_id":"999"},"metadata":{"resourceVersion":"1"}}`); err != nil {
		t.Fatalf("seed etcd config: %v", err)
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

	// Run the consumer with the real etcd client.
	go NewConsumer([]string{brokers}, database, etcd).Run(ctx)

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

	// The CONFIG_APPLY_FAIL for (n1,2) had no prior successful version, so the
	// rollback must have DELETED the seeded config from etcd.
	waitFor(t, 25*time.Second, func() bool {
		resp, err := etcd.Client().Get(ctx, "configs/n1/hsi/2")
		return err == nil && len(resp.Kvs) == 0
	}, "etcd config to be rolled back (deleted) after CONFIG_APPLY_FAIL")
}

// TestConsumerSkipsUnavailableFirstBroker verifies topic discovery and
// consumption continue through the configured broker list when the first
// address refuses connections.
func TestConsumerSkipsUnavailableFirstBroker(t *testing.T) {
	brokers := os.Getenv("TEST_KAFKA_BROKERS")
	dsn := os.Getenv("TEST_DATABASE_URL")
	etcdEndpoints := os.Getenv("TEST_ETCD_ENDPOINTS")
	if brokers == "" || dsn == "" || etcdEndpoints == "" {
		t.Skip("TEST_KAFKA_BROKERS / TEST_DATABASE_URL / TEST_ETCD_ENDPOINTS not set; skipping Kafka e2e")
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	t.Setenv("ETCD_ENDPOINTS", etcdEndpoints)
	etcd, err := storage.NewEtcdClient()
	if err != nil {
		t.Fatalf("etcd connect: %v", err)
	}
	defer etcd.Close()

	suffix := strconv.FormatInt(time.Now().UnixNano(), 10)
	topic := "fastrg.node.events.broker-fallback." + suffix
	t.Setenv("KAFKA_TOPIC", topic)
	t.Setenv("KAFKA_GROUP", "fastrg-controller-broker-fallback."+suffix)

	scopedDSN, cleanup := createTask12KafkaSchema(t, ctx, dsn, "broker_fallback")
	defer cleanup()
	database, err := db.New(ctx, scopedDSN)
	if err != nil {
		t.Fatalf("db: %v", err)
	}
	defer database.Close()

	healthyBrokers := splitTestBrokers(brokers)
	w := &kafkago.Writer{
		Addr:                   kafkago.TCP(healthyBrokers...),
		Topic:                  topic,
		AllowAutoTopicCreation: true,
		BatchTimeout:           50 * time.Millisecond,
	}
	defer w.Close()
	event := &eventsv1.NodeEvent{
		NodeUuid: "broker-fallback", UserId: "0", Type: eventsv1.EventType_EVENT_TYPE_RUNTIME_ERROR,
		Timestamp: time.Now().Unix(),
		Payload: &eventsv1.NodeEvent_RuntimeError{RuntimeError: &eventsv1.RuntimeError{
			Module: "broker-test", ErrorMessage: "fallback consumed",
		}},
	}
	value, err := proto.Marshal(event)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if err := writeWithRetry(ctx, w, []kafkago.Message{{Key: []byte(event.NodeUuid), Value: value}}); err != nil {
		t.Fatalf("produce: %v", err)
	}

	consumerBrokers := append([]string{reserveRefusedAddress(t)}, healthyBrokers...)
	go NewConsumer(consumerBrokers, database, etcd).Run(ctx)
	waitFor(t, 25*time.Second, func() bool {
		rows, _ := database.ListNodeEvents(ctx, "broker-fallback", "RUNTIME_ERROR", 0)
		return len(rows) == 1 && rows[0].Module == "broker-test"
	}, "event consumption through the second broker")
}

func splitTestBrokers(brokers string) []string {
	var result []string
	for _, broker := range strings.Split(brokers, ",") {
		if broker = strings.TrimSpace(broker); broker != "" {
			result = append(result, broker)
		}
	}
	return result
}

func reserveRefusedAddress(t *testing.T) string {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("reserve refused broker address: %v", err)
	}
	address := listener.Addr().String()
	if err := listener.Close(); err != nil {
		t.Fatalf("release refused broker address: %v", err)
	}
	return address
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
