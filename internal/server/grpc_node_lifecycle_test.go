package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"testing"
	"time"

	controllerpb "fastrg-controller/proto"
)

// mustMarshalNode renders a node doc the way etcd stores it, for the pure CAS
// mutate tests that take the current value as raw bytes.
func mustMarshalNode(t *testing.T, node map[string]interface{}) []byte {
	t.Helper()
	b, err := json.Marshal(node)
	if err != nil {
		t.Fatalf("marshal node: %v", err)
	}
	return b
}

// TestShutdownNodeCASValue: the self-reported shutdown mutate marks a node
// inactive regardless of how fresh its heartbeat is, and refuses to invent a
// node doc that is not already there.
func TestShutdownNodeCASValue(t *testing.T) {
	now := time.Now().Unix()
	tests := []struct {
		name    string
		current []byte
		wantErr error
		// wantFields is a subset of the resulting doc that must match exactly.
		wantFields map[string]interface{}
	}{
		{
			name:    "missing key is reported as unregistered",
			current: nil,
			wantErr: errNodeNotRegistered,
		},
		{
			name:    "unparsable current value is rejected",
			current: []byte("{not json"),
			wantErr: errInvalidNodeData,
		},
		{
			// The key difference from the stale path: a fresh heartbeat does
			// not veto a node that says it is shutting down.
			name: "fresh heartbeat still goes inactive",
			current: mustMarshalNode(t, map[string]interface{}{
				"node_uuid": "node-shutdown", "status": "active",
				"last_seen_time": now, "preserved": "value",
			}),
			wantFields: map[string]interface{}{
				"status": "inactive", "inactive_reason": "node_shutdown",
				"inactive_at": float64(now), "last_seen_time": float64(now),
				"preserved": "value",
			},
		},
		{
			name: "already inactive node gets refreshed markers",
			current: mustMarshalNode(t, map[string]interface{}{
				"node_uuid": "node-was-stale", "status": "inactive",
				"last_seen_time": now - HeartbeatTimeout - 120,
				"inactive_at":    now - 300, "inactive_reason": "heartbeat_timeout",
			}),
			wantFields: map[string]interface{}{
				"status": "inactive", "inactive_reason": "node_shutdown",
				"inactive_at": float64(now),
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result, err := shutdownNodeCASValue(tt.current, now)
			if tt.wantErr != nil {
				if !errors.Is(err, tt.wantErr) {
					t.Fatalf("shutdownNodeCASValue error = %v, want %v", err, tt.wantErr)
				}
				if result.Value != nil {
					t.Fatalf("aborted mutate returned write value %q", result.Value)
				}
				if result.Delete {
					t.Fatal("aborted mutate asked for a delete")
				}
				return
			}
			if err != nil {
				t.Fatalf("shutdownNodeCASValue: %v", err)
			}
			if result.Delete {
				t.Fatal("shutdown mutate asked for a delete, want the key kept")
			}
			var updated map[string]interface{}
			if err := json.Unmarshal(result.Value, &updated); err != nil {
				t.Fatalf("unmarshal result: %v", err)
			}
			for field, want := range tt.wantFields {
				if updated[field] != want {
					t.Fatalf("%s = %#v, want %#v (doc: %#v)", field, updated[field], want, updated)
				}
			}
		})
	}
}

// TestReportShutdownMarksInactiveKeepsKey: a node reporting shutdown becomes
// inactive in etcd, keeps its key and its other fields, and stops being
// monitored.
func TestReportShutdownMarksInactiveKeepsKey(t *testing.T) {
	gs := newTestGrpcServer(t)
	ctx := context.Background()
	uuid := fmt.Sprintf("test-shutdown-%d", time.Now().UnixNano())
	// nic_model_wan set so no NIC-fetch goroutine is spawned for this node.
	seedNode(t, gs, uuid, map[string]interface{}{
		"node_uuid": uuid, "node_ip": "127.0.0.1", "status": "active",
		"last_seen_time": time.Now().Unix(), "nic_model_wan": "known",
	})
	t.Cleanup(func() {
		gs.nodeMonitorMgr.StopMonitoring(uuid)
		gs.etcd.Client().Delete(ctx, "nodes/"+uuid)
	})
	if err := gs.nodeMonitorMgr.StartMonitoring(uuid, "127.0.0.1"); err != nil {
		t.Fatalf("StartMonitoring: %v", err)
	}

	beforeShutdown := time.Now().Unix()
	if _, err := gs.ReportShutdown(ctx, &controllerpb.NodeShutdownRequest{NodeUuid: uuid}); err != nil {
		t.Fatalf("ReportShutdown: %v", err)
	}

	node, ok := readNode(t, gs, uuid)
	if !ok {
		t.Fatal("node key should survive ReportShutdown (marked inactive, not deleted)")
	}
	if node["status"] != "inactive" {
		t.Fatalf("status = %v, want inactive", node["status"])
	}
	if node["inactive_reason"] != "node_shutdown" {
		t.Fatalf("inactive_reason = %v, want node_shutdown", node["inactive_reason"])
	}
	inactiveAt, ok := node["inactive_at"].(float64)
	if !ok || int64(inactiveAt) < beforeShutdown {
		t.Fatalf("inactive_at = %v, want >= %d", node["inactive_at"], beforeShutdown)
	}
	if node["nic_model_wan"] != "known" || node["node_ip"] != "127.0.0.1" {
		t.Fatalf("existing node fields were lost: %#v", node)
	}

	gs.nodeMonitorMgr.mu.RLock()
	_, stillMonitored := gs.nodeMonitorMgr.monitors[uuid]
	gs.nodeMonitorMgr.mu.RUnlock()
	if stillMonitored {
		t.Fatal("ReportShutdown did not stop monitoring the node")
	}

	// Missing node_uuid is rejected.
	if _, err := gs.ReportShutdown(ctx, &controllerpb.NodeShutdownRequest{}); err == nil {
		t.Fatal("ReportShutdown with empty uuid should error")
	}
}

// TestReportShutdownUnregisteredNode: reporting shutdown for a node the
// controller does not know errors out and must not resurrect the node as an
// inactive record.
func TestReportShutdownUnregisteredNode(t *testing.T) {
	gs := newTestGrpcServer(t)
	ctx := context.Background()
	uuid := fmt.Sprintf("test-shutdown-unknown-%d", time.Now().UnixNano())
	if _, err := gs.etcd.Client().Delete(ctx, "nodes/"+uuid); err != nil {
		t.Fatalf("clear node key: %v", err)
	}
	t.Cleanup(func() { gs.etcd.Client().Delete(ctx, "nodes/"+uuid) })

	if _, err := gs.ReportShutdown(ctx, &controllerpb.NodeShutdownRequest{NodeUuid: uuid}); err == nil {
		t.Fatal("ReportShutdown for an unregistered node should error")
	}
	if _, ok := readNode(t, gs, uuid); ok {
		t.Fatal("ReportShutdown created an etcd key for an unregistered node")
	}
}

// TestReportShutdownThenReregisterReactivates: a node marked inactive by its
// own shutdown report comes back as active when it registers again, with the
// inactive markers gone.
func TestReportShutdownThenReregisterReactivates(t *testing.T) {
	gs := newTestGrpcServer(t)
	ctx := context.Background()
	uuid := fmt.Sprintf("test-shutdown-restart-%d", time.Now().UnixNano())
	oldRegisteredAt := time.Now().Add(-time.Hour).Unix()
	seedNode(t, gs, uuid, map[string]interface{}{
		"node_uuid": uuid, "node_ip": "127.0.0.1", "status": "active",
		"registered_at": oldRegisteredAt, "last_seen_time": oldRegisteredAt,
		"nic_model_wan": "known",
	})
	t.Cleanup(func() {
		gs.nodeMonitorMgr.StopMonitoring(uuid)
		gs.etcd.Client().Delete(ctx, "nodes/"+uuid)
	})

	if _, err := gs.ReportShutdown(ctx, &controllerpb.NodeShutdownRequest{NodeUuid: uuid}); err != nil {
		t.Fatalf("ReportShutdown: %v", err)
	}
	down, ok := readNode(t, gs, uuid)
	if !ok {
		t.Fatal("node key should survive ReportShutdown")
	}
	if down["status"] != "inactive" {
		t.Fatalf("status after shutdown = %v, want inactive", down["status"])
	}

	reply, err := gs.RegisterNode(ctx, &controllerpb.NodeRegisterRequest{
		NodeUuid: uuid, Ip: "127.0.0.1", Version: "v-after-restart",
	})
	if err != nil || !reply.Success {
		t.Fatalf("RegisterNode = (%v, %v), want success", reply, err)
	}
	gs.nodeMonitorMgr.StopMonitoring(uuid)

	node, ok := readNode(t, gs, uuid)
	if !ok {
		t.Fatal("re-registered node not found")
	}
	if node["status"] != "active" || node["version"] != "v-after-restart" {
		t.Fatalf("node did not come back active: %#v", node)
	}
	for _, marker := range []string{"inactive_at", "inactive_reason"} {
		if _, exists := node[marker]; exists {
			t.Fatalf("re-registration kept %s: %#v", marker, node)
		}
	}
	if node["nic_model_wan"] != "known" {
		t.Fatalf("NIC model not carried over: %#v", node)
	}
	registeredAt, ok := node["registered_at"].(float64)
	if !ok || int64(registeredAt) <= oldRegisteredAt {
		t.Fatalf("registered_at = %v, want > %d", node["registered_at"], oldRegisteredAt)
	}
	lastSeen, ok := node["last_seen_time"].(float64)
	if !ok || int64(lastSeen) <= oldRegisteredAt {
		t.Fatalf("last_seen_time = %v, want > %d", node["last_seen_time"], oldRegisteredAt)
	}
}

// TestHeartbeatNodeCASValueClearsInactiveMarkers: flipping a node back to
// active drops the inactive markers, and leaves everything else alone.
func TestHeartbeatNodeCASValueClearsInactiveMarkers(t *testing.T) {
	now := time.Now().Unix()
	req := &controllerpb.NodeHeartbeat{NodeUuid: "node-hb", Ip: "127.0.0.1", UptimeTimestamp: 7}
	tests := []struct {
		name    string
		current map[string]interface{}
	}{
		{
			name: "node marked inactive loses its markers",
			current: map[string]interface{}{
				"node_uuid": "node-hb", "status": "inactive",
				"last_seen_time": now - HeartbeatTimeout - 120,
				"inactive_at":    now - 100, "inactive_reason": "heartbeat_timeout",
				"preserved": "value",
			},
		},
		{
			name: "node without markers is unaffected",
			current: map[string]interface{}{
				"node_uuid": "node-hb", "status": "active",
				"last_seen_time": now - 5, "preserved": "value",
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result, nodeData, err := heartbeatNodeCASValue(mustMarshalNode(t, tt.current), req, now)
			if err != nil {
				t.Fatalf("heartbeatNodeCASValue: %v", err)
			}
			var updated map[string]interface{}
			if err := json.Unmarshal(result.Value, &updated); err != nil {
				t.Fatalf("unmarshal result: %v", err)
			}
			if updated["status"] != "active" {
				t.Fatalf("status = %v, want active", updated["status"])
			}
			for _, marker := range []string{"inactive_at", "inactive_reason"} {
				if _, exists := updated[marker]; exists {
					t.Fatalf("%s survived the flip back to active: %#v", marker, updated)
				}
				if _, exists := nodeData[marker]; exists {
					t.Fatalf("%s survived in the returned node data: %#v", marker, nodeData)
				}
			}
			if updated["last_seen_time"] != float64(now) {
				t.Fatalf("last_seen_time = %v, want %d", updated["last_seen_time"], now)
			}
			if updated["preserved"] != "value" {
				t.Fatalf("unrelated current field was lost: %#v", updated)
			}
		})
	}
}

// TestHeartbeatReactivatesAndClearsInactiveMarkers: a node wrongly marked
// inactive by the stale sweep heals on its next heartbeat, leaving no inactive
// markers behind in etcd.
func TestHeartbeatReactivatesAndClearsInactiveMarkers(t *testing.T) {
	gs := newTestGrpcServer(t)
	ctx := context.Background()
	uuid := fmt.Sprintf("test-hb-reactivate-%d", time.Now().UnixNano())
	markedAt := time.Now().Add(-2 * time.Minute).Unix()
	// Looks like the stale sweep marked it; nic_model_wan set so no NIC-fetch
	// goroutine is spawned for this node.
	seedNode(t, gs, uuid, map[string]interface{}{
		"node_uuid": uuid, "node_ip": "127.0.0.1", "status": "inactive",
		"last_seen_time": markedAt, "inactive_at": markedAt,
		"inactive_reason": "heartbeat_timeout", "nic_model_wan": "known",
	})
	t.Cleanup(func() {
		gs.nodeMonitorMgr.StopMonitoring(uuid)
		gs.etcd.Client().Delete(ctx, "nodes/"+uuid)
	})

	if _, err := gs.Heartbeat(ctx, &controllerpb.NodeHeartbeat{NodeUuid: uuid, Ip: "127.0.0.1"}); err != nil {
		t.Fatalf("Heartbeat: %v", err)
	}

	node, ok := readNode(t, gs, uuid)
	if !ok {
		t.Fatal("node disappeared after heartbeat")
	}
	if node["status"] != "active" {
		t.Fatalf("status = %v, want active", node["status"])
	}
	for _, marker := range []string{"inactive_at", "inactive_reason"} {
		if _, exists := node[marker]; exists {
			t.Fatalf("%s survived the heartbeat: %#v", marker, node)
		}
	}
	if node["nic_model_wan"] != "known" {
		t.Fatalf("NIC model lost after heartbeat: %#v", node)
	}
}
