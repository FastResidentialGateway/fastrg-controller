package kafka

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"reflect"
	"strconv"
	"testing"
	"time"

	"fastrg-controller/internal/db"
	"fastrg-controller/internal/storage"
	eventsv1 "fastrg-controller/proto/eventsv1"

	"github.com/jackc/pgx/v5/pgxpool"
	kafkago "github.com/segmentio/kafka-go"
	"google.golang.org/protobuf/proto"
)

type rollbackE2EEnvironment struct {
	ctx      context.Context
	brokers  string
	database *db.DB
	pool     *pgxpool.Pool
	etcd     *storage.EtcdClient
	suffix   string
}

func newRollbackE2EEnvironment(t *testing.T) *rollbackE2EEnvironment {
	t.Helper()

	brokers := os.Getenv("TEST_KAFKA_BROKERS")
	dsn := os.Getenv("TEST_DATABASE_URL")
	etcdEndpoints := os.Getenv("TEST_ETCD_ENDPOINTS")
	if brokers == "" || dsn == "" || etcdEndpoints == "" {
		t.Skip("TEST_KAFKA_BROKERS / TEST_DATABASE_URL / TEST_ETCD_ENDPOINTS not set; skipping Kafka rollback e2e")
	}

	ctx, cancel := context.WithCancel(context.Background())

	t.Setenv("ETCD_ENDPOINTS", etcdEndpoints)
	etcd, err := storage.NewEtcdClient()
	if err != nil {
		t.Fatalf("etcd connect: %v", err)
	}
	t.Cleanup(func() { etcd.Close() })

	database, err := db.New(ctx, dsn)
	if err != nil {
		t.Fatalf("db: %v", err)
	}
	t.Cleanup(database.Close)

	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatalf("assert pool: %v", err)
	}
	t.Cleanup(pool.Close)
	// Cleanup callbacks run in reverse registration order. Cancel consumers
	// before closing their etcd/DB dependencies, including on an early t.Fatal.
	t.Cleanup(cancel)

	return &rollbackE2EEnvironment{
		ctx:      ctx,
		brokers:  brokers,
		database: database,
		pool:     pool,
		etcd:     etcd,
		suffix:   strconv.FormatInt(time.Now().UnixNano(), 10),
	}
}

func rollbackConfigJSON(node, user string, rv int) string {
	return fmt.Sprintf(
		`{"config":{"user_id":%q,"vlan_id":%q},"metadata":{"node":%q,"resourceVersion":%q,"updatedBy":"e2e-test","updatedAt":"2026-07-13T00:00:00Z"}}`,
		user, strconv.Itoa(100+rv), node, strconv.Itoa(rv),
	)
}

func configApplyEvent(node, user string, success bool, timestamp int64) *eventsv1.NodeEvent {
	eventType := eventsv1.EventType_EVENT_TYPE_CONFIG_APPLY_FAIL
	errorCode := "EINVAL"
	errorMessage := "bad config"
	if success {
		eventType = eventsv1.EventType_EVENT_TYPE_CONFIG_APPLY_OK
		errorCode = ""
		errorMessage = ""
	}
	return &eventsv1.NodeEvent{
		NodeUuid:      node,
		UserId:        user,
		Type:          eventType,
		Timestamp:     timestamp,
		CorrelationId: fmt.Sprintf("%s-%d", node, timestamp),
		Payload: &eventsv1.NodeEvent_ConfigApplyResult{ConfigApplyResult: &eventsv1.ConfigApplyResult{
			Action:       "update",
			Success:      success,
			ErrorCode:    errorCode,
			ErrorMessage: errorMessage,
		}},
	}
}

func produceRollbackEvents(t *testing.T, ctx context.Context, brokers, topic string, events ...*eventsv1.NodeEvent) {
	t.Helper()

	w := &kafkago.Writer{
		Addr:                   kafkago.TCP(brokers),
		Topic:                  topic,
		AllowAutoTopicCreation: true,
		BatchTimeout:           50 * time.Millisecond,
	}
	defer w.Close()

	msgs := make([]kafkago.Message, 0, len(events))
	for _, event := range events {
		value, err := proto.Marshal(event)
		if err != nil {
			t.Fatalf("marshal event: %v", err)
		}
		msgs = append(msgs, kafkago.Message{Key: []byte(event.GetNodeUuid()), Value: value})
	}
	if err := writeWithRetry(ctx, w, msgs); err != nil {
		t.Fatalf("produce to %s: %v", topic, err)
	}
}

func startRollbackConsumer(t *testing.T, env *rollbackE2EEnvironment, topic, group string) func() {
	t.Helper()

	t.Setenv("KAFKA_TOPIC", topic)
	t.Setenv("KAFKA_GROUP", group)
	ctx, cancel := context.WithCancel(env.ctx)
	done := make(chan struct{})
	consumer := NewConsumer([]string{env.brokers}, env.database, env.etcd)
	go func() {
		defer close(done)
		consumer.Run(ctx)
	}()

	return func() {
		cancel()
		select {
		case <-done:
		case <-time.After(10 * time.Second):
			t.Errorf("timed out stopping Kafka consumer for topic %s", topic)
		}
	}
}

func rollbackHistoryCount(ctx context.Context, pool *pgxpool.Pool, node, user, status, action string) int {
	var count int
	if err := pool.QueryRow(ctx, `
		SELECT count(*) FROM hsi_config_history
		WHERE node_uuid = $1 AND user_id = $2 AND status = $3 AND action = $4`,
		node, user, status, action,
	).Scan(&count); err != nil {
		return -1
	}
	return count
}

func rollbackNodeEventCount(ctx context.Context, pool *pgxpool.Pool, node, user, eventType string) int {
	var count int
	if err := pool.QueryRow(ctx, `
		SELECT count(*) FROM node_events
		WHERE node_uuid = $1 AND user_id = $2 AND event_type = $3`,
		node, user, eventType,
	).Scan(&count); err != nil {
		return -1
	}
	return count
}

func rollbackJSONEqual(got []byte, want string) bool {
	var gotValue, wantValue any
	if err := json.Unmarshal(got, &gotValue); err != nil {
		return false
	}
	if err := json.Unmarshal([]byte(want), &wantValue); err != nil {
		return false
	}
	return reflect.DeepEqual(gotValue, wantValue)
}

// rollbackRestoredPayload reports whether got carries want's config payload
// with freshly re-stamped rollback metadata: the payload is restored while
// resourceVersion advances to wantRV (current+1) and updatedBy identifies the
// rollback writer. The old snapshot's metadata must never be written back.
func rollbackRestoredPayload(got []byte, want string, wantRV string) bool {
	type envelope struct {
		Config   any `json:"config"`
		Metadata struct {
			ResourceVersion string `json:"resourceVersion"`
			UpdatedBy       string `json:"updatedBy"`
			UpdatedAt       string `json:"updatedAt"`
		} `json:"metadata"`
	}
	var gotEnv, wantEnv envelope
	if err := json.Unmarshal(got, &gotEnv); err != nil {
		return false
	}
	if err := json.Unmarshal([]byte(want), &wantEnv); err != nil {
		return false
	}
	if !reflect.DeepEqual(gotEnv.Config, wantEnv.Config) {
		return false
	}
	if gotEnv.Metadata.ResourceVersion != wantRV || gotEnv.Metadata.UpdatedBy != rollbackUpdatedBy {
		return false
	}
	_, err := time.Parse(time.RFC3339, gotEnv.Metadata.UpdatedAt)
	return err == nil
}

func TestRollbackRestoresPreviousConfig(t *testing.T) {
	env := newRollbackE2EEnvironment(t)
	node, user := "rollback-restore-"+env.suffix, "101"
	key := fmt.Sprintf("configs/%s/hsi/%s", node, user)
	v1 := rollbackConfigJSON(node, user, 1)
	v2 := rollbackConfigJSON(node, user, 2)
	if _, err := env.etcd.Client().Put(env.ctx, key, v1); err != nil {
		t.Fatalf("put v1: %v", err)
	}

	topic := "fastrg.node.events.rollback.restore." + env.suffix
	produceRollbackEvents(t, env.ctx, env.brokers, topic, configApplyEvent(node, user, true, time.Now().Unix()))
	stop := startRollbackConsumer(t, env, topic, "fastrg-controller-rollback-restore."+env.suffix)
	defer stop()

	waitFor(t, 25*time.Second, func() bool {
		return rollbackHistoryCount(env.ctx, env.pool, node, user, "success", db.ActionUpsert) == 1
	}, "CONFIG_APPLY_OK success history")

	if _, err := env.etcd.Client().Put(env.ctx, key, v2); err != nil {
		t.Fatalf("put v2: %v", err)
	}
	produceRollbackEvents(t, env.ctx, env.brokers, topic, configApplyEvent(node, user, false, time.Now().Unix()+1))

	waitFor(t, 25*time.Second, func() bool {
		resp, err := env.etcd.Client().Get(env.ctx, key)
		// The rollback restores v1's payload but re-stamps metadata
		// (rv = current 2 + 1 = 3, fresh updatedAt/updatedBy).
		return err == nil && len(resp.Kvs) == 1 && rollbackRestoredPayload(resp.Kvs[0].Value, v1, "3")
	}, "failed v2 to be rolled back to v1's payload with re-stamped metadata")
	waitFor(t, 25*time.Second, func() bool {
		return rollbackHistoryCount(env.ctx, env.pool, node, user, "failed", "apply-failed") == 1
	}, "apply-failed history")
}

func TestRollbackSkipsSupersededConfig(t *testing.T) {
	env := newRollbackE2EEnvironment(t)
	node, user := "rollback-superseded-"+env.suffix, "102"
	key := fmt.Sprintf("configs/%s/hsi/%s", node, user)
	v1 := rollbackConfigJSON(node, user, 1)
	v2 := rollbackConfigJSON(node, user, 2)
	v3 := rollbackConfigJSON(node, user, 3)
	if _, err := env.etcd.Client().Put(env.ctx, key, v1); err != nil {
		t.Fatalf("put v1: %v", err)
	}

	okTopic := "fastrg.node.events.rollback.superseded.ok." + env.suffix
	produceRollbackEvents(t, env.ctx, env.brokers, okTopic, configApplyEvent(node, user, true, time.Now().Unix()))
	stopOK := startRollbackConsumer(t, env, okTopic, "fastrg-controller-rollback-superseded-ok."+env.suffix)
	waitFor(t, 25*time.Second, func() bool {
		return rollbackHistoryCount(env.ctx, env.pool, node, user, "success", db.ActionUpsert) == 1
	}, "CONFIG_APPLY_OK success history")
	stopOK()

	if _, err := env.etcd.Client().Put(env.ctx, key, v2); err != nil {
		t.Fatalf("put v2: %v", err)
	}
	failTopic := "fastrg.node.events.rollback.superseded.fail." + env.suffix
	produceRollbackEvents(t, env.ctx, env.brokers, failTopic, configApplyEvent(node, user, false, time.Now().Unix()+1))
	if _, err := env.etcd.Client().Put(env.ctx, key, v3); err != nil {
		t.Fatalf("put v3: %v", err)
	}

	// The FAIL is already in Kafka and v3 is already in etcd before this
	// consumer starts, making the superseding race deterministic.
	stopFail := startRollbackConsumer(t, env, failTopic, "fastrg-controller-rollback-superseded-fail."+env.suffix)
	defer stopFail()
	waitFor(t, 25*time.Second, func() bool {
		return rollbackHistoryCount(env.ctx, env.pool, node, user, "failed", "apply-failed") == 1 &&
			rollbackNodeEventCount(env.ctx, env.pool, node, user, "CONFIG_APPLY_FAIL") == 1
	}, "superseded failure history and node event")

	resp, err := env.etcd.Client().Get(env.ctx, key)
	if err != nil {
		t.Fatalf("get v3: %v", err)
	}
	var got string
	if len(resp.Kvs) == 1 {
		got = string(resp.Kvs[0].Value)
	}
	if len(resp.Kvs) != 1 || got != v3 {
		t.Fatalf("etcd config = %q, want superseding v3 %q", got, v3)
	}
}

func TestRollbackSkipsWhenKeyAbsent(t *testing.T) {
	env := newRollbackE2EEnvironment(t)
	node, user := "rollback-absent-"+env.suffix, "103"
	key := fmt.Sprintf("configs/%s/hsi/%s", node, user)
	v1 := rollbackConfigJSON(node, user, 1)
	if _, err := env.etcd.Client().Put(env.ctx, key, v1); err != nil {
		t.Fatalf("put v1: %v", err)
	}

	okTopic := "fastrg.node.events.rollback.absent.ok." + env.suffix
	produceRollbackEvents(t, env.ctx, env.brokers, okTopic, configApplyEvent(node, user, true, time.Now().Unix()))
	stopOK := startRollbackConsumer(t, env, okTopic, "fastrg-controller-rollback-absent-ok."+env.suffix)
	waitFor(t, 25*time.Second, func() bool {
		return rollbackHistoryCount(env.ctx, env.pool, node, user, "success", db.ActionUpsert) == 1
	}, "CONFIG_APPLY_OK success history")
	stopOK()

	if _, err := env.etcd.Client().Delete(env.ctx, key); err != nil {
		t.Fatalf("delete config: %v", err)
	}
	failTopic := "fastrg.node.events.rollback.absent.fail." + env.suffix
	produceRollbackEvents(t, env.ctx, env.brokers, failTopic, configApplyEvent(node, user, false, time.Now().Unix()+1))
	stopFail := startRollbackConsumer(t, env, failTopic, "fastrg-controller-rollback-absent-fail."+env.suffix)
	defer stopFail()

	waitFor(t, 25*time.Second, func() bool {
		return rollbackHistoryCount(env.ctx, env.pool, node, user, "failed", "apply-failed") == 1
	}, "missing-key apply-failed history")
	resp, err := env.etcd.Client().Get(env.ctx, key)
	if err != nil {
		t.Fatalf("get absent config: %v", err)
	}
	if len(resp.Kvs) != 0 {
		t.Fatalf("config was recreated after rollback skip: %q", resp.Kvs[0].Value)
	}
}
