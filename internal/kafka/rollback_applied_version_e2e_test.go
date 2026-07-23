package kafka

import (
	"fmt"
	"strconv"
	"testing"
	"time"

	eventsv1 "fastrg-controller/proto/eventsv1"
)

func versionedConfigApplyFailure(
	node, user, appliedRV, correlationID string,
	timestamp int64,
) *eventsv1.NodeEvent {
	event := configApplyEvent(node, user, false, timestamp)
	event.CorrelationId = correlationID
	event.GetConfigApplyResult().AppliedResourceVersion = appliedRV
	return event
}

func etcdValueAndRevision(
	t *testing.T,
	env *rollbackE2EEnvironment,
	key string,
) ([]byte, int64) {
	t.Helper()
	resp, err := env.etcd.Client().Get(env.ctx, key)
	if err != nil {
		t.Fatalf("get %s: %v", key, err)
	}
	if len(resp.Kvs) != 1 {
		t.Fatalf("get %s: kv count = %d, want 1", key, len(resp.Kvs))
	}
	return append([]byte(nil), resp.Kvs[0].Value...), resp.Kvs[0].ModRevision
}

func TestRollbackAppliedResourceVersionEndToEnd(t *testing.T) {
	env := newRollbackE2EEnvironment(t)
	topic := "fastrg.node.events.rollback.applied-rv." + env.suffix
	stop := startRollbackConsumer(t, env, topic, "fastrg-controller-rollback-applied-rv."+env.suffix)
	defer stop()

	recordSuccess := func(node, user, key, value string, timestamp int64) {
		t.Helper()
		if _, err := env.etcd.Client().Put(env.ctx, key, value); err != nil {
			t.Fatalf("put successful config %s: %v", key, err)
		}
		produceRollbackEvents(t, env.ctx, env.brokers, topic, configApplyEvent(node, user, true, timestamp))
		waitFor(t, 25*time.Second, func() bool {
			return rollbackHistoryCount(env.ctx, env.pool, node, user, "success", "upsert") == 1
		}, "CONFIG_APPLY_OK success history for "+node)
	}

	t.Run("matching applied version and correlation rolls back", func(t *testing.T) {
		node, user := "rollback-applied-match-"+env.suffix, "201"
		key := fmt.Sprintf("configs/%s/hsi/%s", node, user)
		t.Cleanup(func() { env.etcd.Client().Delete(env.ctx, key) })
		v1 := rollbackConfigJSON(node, user, 1)
		v2 := rollbackConfigJSON(node, user, 2)
		recordSuccess(node, user, key, v1, time.Now().Unix())

		if _, err := env.etcd.Client().Put(env.ctx, key, v2); err != nil {
			t.Fatalf("put failed v2: %v", err)
		}
		_, failedRevision := etcdValueAndRevision(t, env, key)
		produceRollbackEvents(t, env.ctx, env.brokers, topic, versionedConfigApplyFailure(
			node,
			user,
			"2",
			strconv.FormatInt(failedRevision, 10),
			time.Now().Unix()+1,
		))

		waitFor(t, 25*time.Second, func() bool {
			got, _ := etcdValueAndRevision(t, env, key)
			return rollbackRestoredPayload(got, v1, "3")
		}, "matching applied version to roll back")
	})

	t.Run("stale applied version leaves newer etcd value unchanged", func(t *testing.T) {
		node, user := "rollback-applied-stale-"+env.suffix, "202"
		key := fmt.Sprintf("configs/%s/hsi/%s", node, user)
		t.Cleanup(func() { env.etcd.Client().Delete(env.ctx, key) })
		v1 := rollbackConfigJSON(node, user, 1)
		v2 := rollbackConfigJSON(node, user, 2)
		v3 := rollbackConfigJSON(node, user, 3)
		recordSuccess(node, user, key, v1, time.Now().Unix()+2)

		if _, err := env.etcd.Client().Put(env.ctx, key, v2); err != nil {
			t.Fatalf("put failed v2: %v", err)
		}
		_, failedRevision := etcdValueAndRevision(t, env, key)
		if _, err := env.etcd.Client().Put(env.ctx, key, v3); err != nil {
			t.Fatalf("put newer v3: %v", err)
		}
		produceRollbackEvents(t, env.ctx, env.brokers, topic, versionedConfigApplyFailure(
			node,
			user,
			"2",
			strconv.FormatInt(failedRevision, 10),
			time.Now().Unix()+3,
		))

		waitFor(t, 25*time.Second, func() bool {
			return rollbackHistoryCount(env.ctx, env.pool, node, user, "failed", "apply-failed") == 1
		}, "stale applied-version failure history")
		got, _ := etcdValueAndRevision(t, env, key)
		if string(got) != v3 {
			t.Fatalf("etcd value = %s, want newer value unchanged %s", got, v3)
		}
	})

	t.Run("empty applied version uses transitional fallback", func(t *testing.T) {
		node, user := "rollback-applied-fallback-"+env.suffix, "203"
		key := fmt.Sprintf("configs/%s/hsi/%s", node, user)
		t.Cleanup(func() { env.etcd.Client().Delete(env.ctx, key) })
		v1 := rollbackConfigJSON(node, user, 1)
		v2 := rollbackConfigJSON(node, user, 2)
		recordSuccess(node, user, key, v1, time.Now().Unix()+4)

		if _, err := env.etcd.Client().Put(env.ctx, key, v2); err != nil {
			t.Fatalf("put failed v2: %v", err)
		}
		produceRollbackEvents(t, env.ctx, env.brokers, topic, versionedConfigApplyFailure(
			node,
			user,
			"",
			"legacy-non-revision-correlation",
			time.Now().Unix()+5,
		))

		waitFor(t, 25*time.Second, func() bool {
			got, _ := etcdValueAndRevision(t, env, key)
			return rollbackRestoredPayload(got, v1, "3")
		}, "empty applied version to use transitional rollback")
	})

	t.Run("old correlation rejects recreated matching resourceVersion", func(t *testing.T) {
		node, user := "rollback-applied-recreate-"+env.suffix, "204"
		key := fmt.Sprintf("configs/%s/hsi/%s", node, user)
		t.Cleanup(func() { env.etcd.Client().Delete(env.ctx, key) })
		original := rollbackConfigJSON(node, user, 1)
		recreated := fmt.Sprintf(
			`{"config":{"user_id":%q,"vlan_id":"999"},"metadata":{"node":%q,"resourceVersion":"1","updatedBy":"recreate-test","updatedAt":"2026-07-23T00:00:00Z"}}`,
			user,
			node,
		)
		if _, err := env.etcd.Client().Put(env.ctx, key, original); err != nil {
			t.Fatalf("put original generation: %v", err)
		}
		_, originalRevision := etcdValueAndRevision(t, env, key)
		if _, err := env.etcd.Client().Delete(env.ctx, key); err != nil {
			t.Fatalf("delete original generation: %v", err)
		}
		if _, err := env.etcd.Client().Put(env.ctx, key, recreated); err != nil {
			t.Fatalf("put recreated generation: %v", err)
		}

		produceRollbackEvents(t, env.ctx, env.brokers, topic, versionedConfigApplyFailure(
			node,
			user,
			"1",
			strconv.FormatInt(originalRevision, 10),
			time.Now().Unix()+6,
		))
		waitFor(t, 25*time.Second, func() bool {
			return rollbackHistoryCount(env.ctx, env.pool, node, user, "failed", "apply-failed") == 1
		}, "recreated-generation failure history")

		got, recreatedRevision := etcdValueAndRevision(t, env, key)
		if string(got) != recreated {
			t.Fatalf("recreated value changed: got %s want %s", got, recreated)
		}
		if recreatedRevision == originalRevision {
			t.Fatalf("recreated ModRevision = original ModRevision = %d; test did not cross generations", originalRevision)
		}
	})
}
