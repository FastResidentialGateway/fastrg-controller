package kafka

import (
	"context"
	"encoding/json"
	"errors"
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
)

type offlineEditE2EEnvironment struct {
	ctx      context.Context
	brokers  string
	database *db.DB
	pool     *pgxpool.Pool
	etcd     *storage.EtcdClient
	suffix   string
	topic    string
}

func newOfflineEditE2EEnvironment(t *testing.T) *offlineEditE2EEnvironment {
	t.Helper()
	brokers := os.Getenv("TEST_KAFKA_BROKERS")
	dsn := os.Getenv("TEST_DATABASE_URL")
	etcdEndpoints := os.Getenv("TEST_ETCD_ENDPOINTS")
	if brokers == "" || dsn == "" || etcdEndpoints == "" {
		t.Skip("TEST_KAFKA_BROKERS / TEST_DATABASE_URL / TEST_ETCD_ENDPOINTS not set; skipping offline-edit e2e")
	}

	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	t.Setenv("ETCD_ENDPOINTS", etcdEndpoints)
	etcd, err := storage.NewEtcdClient()
	if err != nil {
		t.Fatalf("etcd connect: %v", err)
	}
	t.Cleanup(etcd.Close)

	suffix := strconv.FormatInt(time.Now().UnixNano(), 10)
	scopedDSN, cleanupSchema := createTask12KafkaSchema(t, ctx, dsn, "offline_edit")
	t.Cleanup(cleanupSchema)
	database, err := db.New(ctx, scopedDSN)
	if err != nil {
		t.Fatalf("db: %v", err)
	}
	t.Cleanup(database.Close)
	pool, err := pgxpool.New(ctx, scopedDSN)
	if err != nil {
		t.Fatalf("assert pool: %v", err)
	}
	t.Cleanup(pool.Close)

	topic := "fastrg.node.events.offline-edit." + suffix
	t.Setenv("KAFKA_TOPIC", topic)
	t.Setenv("KAFKA_GROUP", "fastrg-controller-offline-edit."+suffix)
	consumer := NewConsumer([]string{brokers}, database, etcd)
	done := make(chan struct{})
	go func() {
		defer close(done)
		consumer.Run(ctx)
	}()
	t.Cleanup(func() {
		cancel()
		select {
		case <-done:
		case <-time.After(10 * time.Second):
			t.Errorf("timed out stopping offline-edit consumer")
		}
	})

	return &offlineEditE2EEnvironment{
		ctx: ctx, brokers: brokers, database: database, pool: pool, etcd: etcd,
		suffix: suffix, topic: topic,
	}
}

func offlineEnvelope(payloadField, payload, node, rv, updatedBy, updatedAt string) string {
	return fmt.Sprintf(`{%q:%s,"metadata":{"node":%q,"resourceVersion":%q,"updatedBy":%q,"updatedAt":%q}}`,
		payloadField, payload, node, rv, updatedBy, updatedAt)
}

func offlineEvent(node, user string, kind eventsv1.OfflineEditKind, rv, payloadField, payload, summary string, editedAt int64) *eventsv1.NodeEvent {
	configJSON := offlineEnvelope(payloadField, payload, node, rv, "node-cli", time.Unix(editedAt, 0).UTC().Format(time.RFC3339))
	return &eventsv1.NodeEvent{
		NodeUuid:  node,
		UserId:    user,
		Type:      eventsv1.EventType_EVENT_TYPE_CONFIG_OFFLINE_EDIT,
		Timestamp: editedAt + 1,
		Payload: &eventsv1.NodeEvent_ConfigOfflineEdit{ConfigOfflineEdit: &eventsv1.ConfigOfflineEdit{
			ConfigJson: configJSON, ResourceVersion: rv, EditedAt: editedAt,
			EditSummary: summary, Kind: kind,
		}},
	}
}

func offlineTombstoneEvent(node, user, rv, summary string, editedAt int64) *eventsv1.NodeEvent {
	return &eventsv1.NodeEvent{
		NodeUuid:  node,
		UserId:    user,
		Type:      eventsv1.EventType_EVENT_TYPE_CONFIG_OFFLINE_EDIT,
		Timestamp: editedAt + 1,
		Payload: &eventsv1.NodeEvent_ConfigOfflineEdit{ConfigOfflineEdit: &eventsv1.ConfigOfflineEdit{
			ResourceVersion: rv, EditedAt: editedAt, EditSummary: summary,
			Kind: eventsv1.OfflineEditKind_OFFLINE_EDIT_KIND_HSI_CONFIG, Deleted: true,
		}},
	}
}

func offlineMarkerEvent(node string, timestamp int64) *eventsv1.NodeEvent {
	return &eventsv1.NodeEvent{
		NodeUuid: node, UserId: "0", Type: eventsv1.EventType_EVENT_TYPE_RUNTIME_ERROR,
		Timestamp: timestamp,
		Payload: &eventsv1.NodeEvent_RuntimeError{RuntimeError: &eventsv1.RuntimeError{
			Module: "offline-edit-e2e", ErrorMessage: "ordering marker",
		}},
	}
}

func putOfflineValue(t *testing.T, env *offlineEditE2EEnvironment, key, value string) {
	t.Helper()
	if _, err := env.etcd.Client().Put(env.ctx, key, value); err != nil {
		t.Fatalf("put %s: %v", key, err)
	}
	t.Cleanup(func() { _, _ = env.etcd.Client().Delete(context.Background(), key) })
}

func getOfflineValue(t *testing.T, env *offlineEditE2EEnvironment, key string) ([]byte, int64, bool) {
	t.Helper()
	resp, err := env.etcd.Client().Get(env.ctx, key)
	if err != nil {
		t.Fatalf("get %s: %v", key, err)
	}
	if len(resp.Kvs) == 0 {
		return nil, 0, false
	}
	return resp.Kvs[0].Value, resp.Kvs[0].ModRevision, true
}

func offlineAppliedValue(value []byte, payloadField string, wantPayload any, wantNode, wantRV string) bool {
	var envelope map[string]json.RawMessage
	if json.Unmarshal(value, &envelope) != nil {
		return false
	}
	var payload any
	if json.Unmarshal(envelope[payloadField], &payload) != nil || !reflect.DeepEqual(payload, wantPayload) {
		return false
	}
	var metadata configMetadata
	if json.Unmarshal(envelope["metadata"], &metadata) != nil {
		return false
	}
	if metadata.Node != wantNode || metadata.ResourceVersion != wantRV || metadata.UpdatedBy != offlineEditUpdatedBy {
		return false
	}
	_, err := time.Parse(time.RFC3339, metadata.UpdatedAt)
	return err == nil
}

func offlineNodeEventCount(ctx context.Context, pool *pgxpool.Pool, node string) int {
	var count int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM node_events WHERE node_uuid=$1 AND event_type='CONFIG_OFFLINE_EDIT'`, node).Scan(&count); err != nil {
		return -1
	}
	return count
}

func waitForOfflineMarker(t *testing.T, env *offlineEditE2EEnvironment, node string) {
	t.Helper()
	waitFor(t, 25*time.Second, func() bool {
		rows, err := env.database.ListNodeEvents(env.ctx, node, "RUNTIME_ERROR", 10)
		return err == nil && len(rows) == 1
	}, "offline-edit ordering marker "+node)
}

func TestOfflineEditConsumerEndToEnd(t *testing.T) {
	env := newOfflineEditE2EEnvironment(t)
	oldTime := "2026-07-22T08:00:00Z"
	editTime := time.Date(2026, 7, 22, 9, 0, 0, 0, time.UTC).Unix()

	type winnerCase struct {
		node, user, key, payloadField, currentPayload, proposedPayload string
		kind                                                           eventsv1.OfflineEditKind
		want                                                           any
	}
	winners := []winnerCase{
		{"offline-hsi-" + env.suffix, "11", "", "config", `{"user_id":"11","vlan_id":"100"}`, `{"user_id":"11","vlan_id":"200"}`, eventsv1.OfflineEditKind_OFFLINE_EDIT_KIND_HSI_CONFIG, map[string]any{"user_id": "11", "vlan_id": "200"}},
		{"offline-dns-" + env.suffix, "12", "", "records", `[{"domain":"old.test","ip":"192.0.2.1","ttl":30}]`, `[{"domain":"new.test","ip":"192.0.2.2","ttl":60}]`, eventsv1.OfflineEditKind_OFFLINE_EDIT_KIND_DNS_RECORDS, []any{map[string]any{"domain": "new.test", "ip": "192.0.2.2", "ttl": float64(60)}}},
		{"offline-count-" + env.suffix, "0", "", "subscriber_count", `"10"`, `"20"`, eventsv1.OfflineEditKind_OFFLINE_EDIT_KIND_SUBSCRIBER_COUNT, "20"},
	}
	for i := range winners {
		w := &winners[i]
		switch w.kind {
		case eventsv1.OfflineEditKind_OFFLINE_EDIT_KIND_HSI_CONFIG:
			w.key = fmt.Sprintf("configs/%s/hsi/%s", w.node, w.user)
		case eventsv1.OfflineEditKind_OFFLINE_EDIT_KIND_DNS_RECORDS:
			w.key = fmt.Sprintf("configs/%s/dns/%s", w.node, w.user)
		case eventsv1.OfflineEditKind_OFFLINE_EDIT_KIND_SUBSCRIBER_COUNT:
			w.key = fmt.Sprintf("user_counts/%s/", w.node)
		}
		putOfflineValue(t, env, w.key, offlineEnvelope(w.payloadField, w.currentPayload, w.node, "1", "controller", oldTime))
		produceRollbackEvents(t, env.ctx, env.brokers, env.topic,
			offlineEvent(w.node, w.user, w.kind, "2", w.payloadField, w.proposedPayload, "winner", editTime))
	}
	for _, w := range winners {
		w := w
		waitFor(t, 25*time.Second, func() bool {
			value, _, exists := getOfflineValue(t, env, w.key)
			return exists && offlineAppliedValue(value, w.payloadField, w.want, w.node, "2")
		}, "accepted "+w.kind.String())
	}

	// Replaying the HSI winner must stop at payload equality and must not stamp
	// another resourceVersion or etcd ModRevision.
	_, replayRevision, _ := getOfflineValue(t, env, winners[0].key)
	replayMarker := "offline-replay-marker-" + env.suffix
	produceRollbackEvents(t, env.ctx, env.brokers, env.topic,
		offlineEvent(winners[0].node, winners[0].user, winners[0].kind, "2", winners[0].payloadField, winners[0].proposedPayload, "winner", editTime),
		offlineMarkerEvent(replayMarker, editTime+10))
	waitForOfflineMarker(t, env, replayMarker)
	value, gotReplayRevision, _ := getOfflineValue(t, env, winners[0].key)
	if gotReplayRevision != replayRevision || !offlineAppliedValue(value, "config", winners[0].want, winners[0].node, "2") {
		t.Fatalf("content replay changed accepted value/revision: revision %d -> %d value=%s", replayRevision, gotReplayRevision, value)
	}

	// Each losing kind leaves one operator-visible node_events row and leaves
	// the controller value untouched.
	for i, w := range winners {
		node := "offline-discard-" + strconv.Itoa(i) + "-" + env.suffix
		key := w.key
		switch w.kind {
		case eventsv1.OfflineEditKind_OFFLINE_EDIT_KIND_HSI_CONFIG:
			key = fmt.Sprintf("configs/%s/hsi/%s", node, w.user)
		case eventsv1.OfflineEditKind_OFFLINE_EDIT_KIND_DNS_RECORDS:
			key = fmt.Sprintf("configs/%s/dns/%s", node, w.user)
		case eventsv1.OfflineEditKind_OFFLINE_EDIT_KIND_SUBSCRIBER_COUNT:
			key = fmt.Sprintf("user_counts/%s/", node)
		}
		stored := offlineEnvelope(w.payloadField, w.currentPayload, node, "3", "controller", oldTime)
		putOfflineValue(t, env, key, stored)
		produceRollbackEvents(t, env.ctx, env.brokers, env.topic,
			offlineEvent(node, w.user, w.kind, "2", w.payloadField, w.proposedPayload, "discard summary", editTime+int64(i)))
		waitFor(t, 25*time.Second, func() bool { return offlineNodeEventCount(env.ctx, env.pool, node) == 1 }, "discard audit for "+w.kind.String())
		got, _, _ := getOfflineValue(t, env, key)
		if !rollbackJSONEqual(got, stored) {
			t.Fatalf("discarded %s changed etcd: got=%s want=%s", w.kind, got, stored)
		}
		rows, err := env.database.ListNodeEvents(env.ctx, node, "CONFIG_OFFLINE_EDIT", 10)
		if err != nil || len(rows) != 1 || rows[0].Action != "discard" || rows[0].CorrelationID != "" ||
			rows[0].Module != offlineEditKindString(w.kind) || rows[0].Context == "" {
			t.Fatalf("discard audit row invalid: rows=%+v err=%v", rows, err)
		}
	}

	// Tombstone replay reaches the nil-current no-op and still cascades DNS,
	// repairing an orphan inserted into the first delete's crash window.
	tombNode, tombUser := "offline-tomb-"+env.suffix, "21"
	tombHSI := fmt.Sprintf("configs/%s/hsi/%s", tombNode, tombUser)
	tombDNS := fmt.Sprintf("configs/%s/dns/%s", tombNode, tombUser)
	putOfflineValue(t, env, tombHSI, offlineEnvelope("config", `{"user_id":"21"}`, tombNode, "5", "controller", oldTime))
	putOfflineValue(t, env, tombDNS, offlineEnvelope("records", `[]`, tombNode, "2", "controller", oldTime))
	tombstone := offlineTombstoneEvent(tombNode, tombUser, "5", "delete subscriber", editTime)
	produceRollbackEvents(t, env.ctx, env.brokers, env.topic, tombstone)
	waitFor(t, 25*time.Second, func() bool {
		_, _, hsiExists := getOfflineValue(t, env, tombHSI)
		_, _, dnsExists := getOfflineValue(t, env, tombDNS)
		return !hsiExists && !dnsExists
	}, "accepted tombstone cascade")
	putOfflineValue(t, env, tombDNS, offlineEnvelope("records", `[]`, tombNode, "1", "orphan", oldTime))
	tombMarker := "offline-tomb-marker-" + env.suffix
	produceRollbackEvents(t, env.ctx, env.brokers, env.topic, tombstone, offlineMarkerEvent(tombMarker, editTime+20))
	waitForOfflineMarker(t, env, tombMarker)
	if _, _, exists := getOfflineValue(t, env, tombDNS); exists {
		t.Fatal("replayed tombstone did not clean the recreated DNS orphan")
	}

	// A rollback is a freshly stamped controller write. Offline proposals made
	// before that stamp must lose even when a content edit carries a larger rv.
	rollbackEditNode, rollbackTombNode := "offline-rb-edit-"+env.suffix, "offline-rb-tomb-"+env.suffix
	rollbackUser := "31"
	rollbackKeys := []string{
		fmt.Sprintf("configs/%s/hsi/%s", rollbackEditNode, rollbackUser),
		fmt.Sprintf("configs/%s/hsi/%s", rollbackTombNode, rollbackUser),
	}
	for i, key := range rollbackKeys {
		node := []string{rollbackEditNode, rollbackTombNode}[i]
		v1 := offlineEnvelope("config", `{"user_id":"31","vlan_id":"100"}`, node, "1", "controller", oldTime)
		v2 := offlineEnvelope("config", `{"user_id":"31","vlan_id":"200"}`, node, "2", "controller", oldTime)
		putOfflineValue(t, env, key, v2)
		prev := &db.HSIConfigRow{ConfigJSON: []byte(v1)}
		if err := env.etcd.CAS(env.ctx, key, func(current []byte) (storage.CASResult, error) {
			return guardedRollbackMutation(prev, current)
		}); err != nil {
			t.Fatalf("re-stamp rollback key %s: %v", key, err)
		}
	}
	beforeRollbackEdit, _, _ := getOfflineValue(t, env, rollbackKeys[0])
	beforeRollbackTomb, _, _ := getOfflineValue(t, env, rollbackKeys[1])
	earlierThanRollback := time.Now().Add(-time.Hour).Unix()
	rollbackMarker := "offline-rb-marker-" + env.suffix
	produceRollbackEvents(t, env.ctx, env.brokers, env.topic,
		offlineEvent(rollbackEditNode, rollbackUser, eventsv1.OfflineEditKind_OFFLINE_EDIT_KIND_HSI_CONFIG,
			"4", "config", `{"user_id":"31","vlan_id":"300"}`, "pre-rollback edit", earlierThanRollback),
		offlineTombstoneEvent(rollbackTombNode, rollbackUser, "3", "pre-rollback delete", earlierThanRollback),
		offlineMarkerEvent(rollbackMarker, time.Now().Unix()))
	waitForOfflineMarker(t, env, rollbackMarker)
	afterRollbackEdit, _, _ := getOfflineValue(t, env, rollbackKeys[0])
	afterRollbackTomb, _, _ := getOfflineValue(t, env, rollbackKeys[1])
	if !reflect.DeepEqual(beforeRollbackEdit, afterRollbackEdit) || !reflect.DeepEqual(beforeRollbackTomb, afterRollbackTomb) {
		t.Fatalf("proposal predating rollback changed state: edit=%s tomb=%s", afterRollbackEdit, afterRollbackTomb)
	}
	if offlineNodeEventCount(env.ctx, env.pool, rollbackEditNode) != 1 || offlineNodeEventCount(env.ctx, env.pool, rollbackTombNode) != 1 {
		t.Fatal("proposals rejected by rollback stamp did not both leave node_events rows")
	}
}

func TestOfflineEditCASRetriesAgainstConcurrentWriter(t *testing.T) {
	etcdEndpoints := os.Getenv("TEST_ETCD_ENDPOINTS")
	if etcdEndpoints == "" {
		t.Skip("TEST_ETCD_ENDPOINTS not set; skipping offline-edit CAS concurrency integration")
	}
	t.Setenv("ETCD_ENDPOINTS", etcdEndpoints)
	etcd, err := storage.NewEtcdClient()
	if err != nil {
		t.Fatalf("etcd connect: %v", err)
	}
	defer etcd.Close()

	ctx := context.Background()
	node, user := "offline-cas-"+strconv.FormatInt(time.Now().UnixNano(), 10), "51"
	key := fmt.Sprintf("configs/%s/hsi/%s", node, user)
	defer etcd.Client().Delete(ctx, key)
	initial := offlineEnvelope("config", `{"user_id":"51","vlan_id":"100"}`, node, "1", "controller", "2026-07-22T08:00:00Z")
	controllerWrite := offlineEnvelope("config", `{"user_id":"51","vlan_id":"200"}`, node, "2", "controller", "2026-07-22T09:30:00Z")
	if _, err := etcd.Client().Put(ctx, key, initial); err != nil {
		t.Fatalf("seed: %v", err)
	}
	event := offlineEvent(node, user, eventsv1.OfflineEditKind_OFFLINE_EDIT_KIND_HSI_CONFIG,
		"2", "config", `{"user_id":"51","vlan_id":"300"}`, "concurrent edit",
		time.Date(2026, 7, 22, 10, 0, 0, 0, time.UTC).Unix())
	proposal, err := prepareOfflineEdit(event, event.GetConfigOfflineEdit())
	if err != nil {
		t.Fatalf("prepare proposal: %v", err)
	}

	firstMutation := make(chan struct{})
	writerDone := make(chan error, 1)
	go func() {
		<-firstMutation
		_, putErr := etcd.Client().Put(ctx, key, controllerWrite)
		writerDone <- putErr
	}()
	attempts := 0
	err = etcd.CAS(ctx, key, func(current []byte) (storage.CASResult, error) {
		attempts++
		if attempts == 1 {
			close(firstMutation)
			if putErr := <-writerDone; putErr != nil {
				return storage.CASResult{}, putErr
			}
		}
		return offlineEditMutation(proposal, current, time.Now())
	})
	if !errors.Is(err, errOfflineEditDiscard) {
		t.Fatalf("CAS error = %v, want discard after retry", err)
	}
	if attempts < 2 {
		t.Fatalf("CAS mutation attempts = %d, want at least 2", attempts)
	}
	resp, err := etcd.Client().Get(ctx, key)
	if err != nil || len(resp.Kvs) != 1 || !rollbackJSONEqual(resp.Kvs[0].Value, controllerWrite) {
		t.Fatalf("concurrent controller write was lost: response=%v err=%v", resp, err)
	}
}
