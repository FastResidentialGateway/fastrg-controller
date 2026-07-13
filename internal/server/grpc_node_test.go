package server

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	controllerpb "fastrg-controller/proto"
)

// seedNode writes a node registration doc directly into etcd (bypassing
// RegisterNode so no monitor/NIC-fetch goroutine is started for tests that only
// need the etcd state).
func seedNode(t *testing.T, gs *GrpcServer, uuid string, data map[string]interface{}) {
	t.Helper()
	b, err := json.Marshal(data)
	if err != nil {
		t.Fatalf("marshal node: %v", err)
	}
	if _, err := gs.etcd.Client().Put(context.Background(), "nodes/"+uuid, string(b)); err != nil {
		t.Fatalf("seed node: %v", err)
	}
}

func readNode(t *testing.T, gs *GrpcServer, uuid string) (map[string]interface{}, bool) {
	t.Helper()
	resp, err := gs.etcd.Client().Get(context.Background(), "nodes/"+uuid)
	if err != nil {
		t.Fatalf("get node: %v", err)
	}
	if len(resp.Kvs) == 0 {
		return nil, false
	}
	var m map[string]interface{}
	if err := json.Unmarshal(resp.Kvs[0].Value, &m); err != nil {
		t.Fatalf("unmarshal node: %v", err)
	}
	return m, true
}

func newTestGrpcServer(t *testing.T) *GrpcServer {
	t.Helper()
	etcd := serverTestEtcd(t)
	nmm := NewNodeMonitorManager(nil)
	gs := NewGrpcServer(etcd, nmm)
	t.Cleanup(gs.Stop)
	return gs
}

// TestRegisterNode: a valid registration writes an active node doc to etcd.
func TestRegisterNode(t *testing.T) {
	gs := newTestGrpcServer(t)
	ctx := context.Background()
	const uuid = "test-reg-node"
	gs.etcd.Client().Delete(ctx, "nodes/"+uuid)
	t.Cleanup(func() {
		gs.nodeMonitorMgr.StopMonitoring(uuid)
		gs.etcd.Client().Delete(ctx, "nodes/"+uuid)
	})

	reply, err := gs.RegisterNode(ctx, &controllerpb.NodeRegisterRequest{
		NodeUuid: uuid, Ip: "127.0.0.1", Version: "v-test",
	})
	if err != nil {
		t.Fatalf("RegisterNode: %v", err)
	}
	if !reply.Success {
		t.Fatalf("RegisterNode reply not success: %s", reply.Message)
	}
	node, ok := readNode(t, gs, uuid)
	if !ok {
		t.Fatal("node not written to etcd")
	}
	if node["status"] != "active" {
		t.Fatalf("status = %v, want active", node["status"])
	}

	// Missing node_uuid is rejected.
	bad, err := gs.RegisterNode(ctx, &controllerpb.NodeRegisterRequest{})
	if err != nil {
		t.Fatalf("RegisterNode(empty): %v", err)
	}
	if bad.Success {
		t.Fatal("RegisterNode with empty uuid should not succeed")
	}
}

// TestHeartbeatUpdatesLastSeen: a heartbeat on a registered node refreshes
// last_seen_time and keeps status active.
func TestHeartbeatUpdatesLastSeen(t *testing.T) {
	gs := newTestGrpcServer(t)
	ctx := context.Background()
	const uuid = "test-hb-node"
	old := time.Now().Add(-30 * time.Second).Unix()
	// nic_model_wan set so Heartbeat does not spawn a NIC-fetch goroutine.
	seedNode(t, gs, uuid, map[string]interface{}{
		"node_uuid": uuid, "node_ip": "127.0.0.1", "status": "active",
		"last_seen_time": old, "nic_model_wan": "known",
	})
	t.Cleanup(func() {
		gs.nodeMonitorMgr.StopMonitoring(uuid)
		gs.etcd.Client().Delete(ctx, "nodes/"+uuid)
	})

	if _, err := gs.Heartbeat(ctx, &controllerpb.NodeHeartbeat{NodeUuid: uuid, Ip: "127.0.0.1"}); err != nil {
		t.Fatalf("Heartbeat: %v", err)
	}
	node, _ := readNode(t, gs, uuid)
	last, _ := node["last_seen_time"].(float64)
	if int64(last) <= old {
		t.Fatalf("last_seen_time not refreshed: got %d, seed was %d", int64(last), old)
	}
	if node["status"] != "active" {
		t.Fatalf("status = %v, want active", node["status"])
	}

	// Heartbeat for an unregistered node errors.
	if _, err := gs.Heartbeat(ctx, &controllerpb.NodeHeartbeat{NodeUuid: "no-such-node"}); err == nil {
		t.Fatal("Heartbeat for unregistered node should error")
	}
}

// TestCheckAndUnregisterStaleNodes: a node past HeartbeatTimeout is marked
// inactive (not deleted).
func TestCheckAndUnregisterStaleNodes(t *testing.T) {
	gs := newTestGrpcServer(t)
	ctx := context.Background()
	const uuid = "test-stale-node"
	stale := time.Now().Add(-time.Duration(HeartbeatTimeout+60) * time.Second).Unix()
	seedNode(t, gs, uuid, map[string]interface{}{
		"node_uuid": uuid, "node_ip": "127.0.0.1", "status": "active",
		"last_seen_time": stale,
	})
	t.Cleanup(func() { gs.etcd.Client().Delete(ctx, "nodes/"+uuid) })

	gs.checkAndUnregisterStaleNodes()

	node, ok := readNode(t, gs, uuid)
	if !ok {
		t.Fatal("stale node should remain in etcd (marked inactive, not deleted)")
	}
	if node["status"] != "inactive" {
		t.Fatalf("status = %v, want inactive", node["status"])
	}
}

// TestUnregisterNode: unregister removes the node doc from etcd.
func TestUnregisterNode(t *testing.T) {
	gs := newTestGrpcServer(t)
	ctx := context.Background()
	const uuid = "test-unreg-node"
	seedNode(t, gs, uuid, map[string]interface{}{
		"node_uuid": uuid, "status": "active", "last_seen_time": time.Now().Unix(),
	})

	if _, err := gs.UnregisterNode(ctx, &controllerpb.NodeRegisterRequest{NodeUuid: uuid}); err != nil {
		t.Fatalf("UnregisterNode: %v", err)
	}
	if _, ok := readNode(t, gs, uuid); ok {
		t.Fatal("node should be deleted from etcd after unregister")
	}
}
