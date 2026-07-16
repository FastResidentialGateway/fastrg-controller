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

func TestNodeCASNormalFlow(t *testing.T) {
	gs := newTestGrpcServer(t)
	ctx := context.Background()
	uuid := fmt.Sprintf("test-cas-normal-%d", time.Now().UnixNano())
	t.Cleanup(func() {
		gs.nodeMonitorMgr.StopMonitoring(uuid)
		gs.etcd.Client().Delete(ctx, "nodes/"+uuid)
	})

	reply, err := gs.RegisterNode(ctx, &controllerpb.NodeRegisterRequest{
		NodeUuid: uuid,
		Ip:       "127.0.0.1",
		Version:  "v-cas-test",
	})
	if err != nil || !reply.Success {
		t.Fatalf("RegisterNode = (%v, %v), want success", reply, err)
	}
	gs.nodeMonitorMgr.StopMonitoring(uuid)

	registered, ok := readNode(t, gs, uuid)
	if !ok {
		t.Fatal("registered node not found")
	}
	registered["nic_model_wan"] = "seed-wan"
	registeredJSON, err := json.Marshal(registered)
	if err != nil {
		t.Fatalf("marshal registered node: %v", err)
	}
	if _, err := gs.etcd.Client().Put(ctx, "nodes/"+uuid, string(registeredJSON)); err != nil {
		t.Fatalf("seed NIC model: %v", err)
	}

	beforeHeartbeat := time.Now().Unix()
	if _, err := gs.Heartbeat(ctx, &controllerpb.NodeHeartbeat{
		NodeUuid:        uuid,
		Ip:              "127.0.0.1",
		UptimeTimestamp: 42,
		HostOs:          "linux",
	}); err != nil {
		t.Fatalf("Heartbeat: %v", err)
	}
	gs.checkAndUnregisterStaleNodes()

	node, ok := readNode(t, gs, uuid)
	if !ok {
		t.Fatal("node disappeared after heartbeat")
	}
	if node["status"] != "active" {
		t.Fatalf("status = %v, want active", node["status"])
	}
	lastSeen, ok := node["last_seen_time"].(float64)
	if !ok || int64(lastSeen) < beforeHeartbeat {
		t.Fatalf("last_seen_time = %v, want >= %d", node["last_seen_time"], beforeHeartbeat)
	}
	if node["uptime"] != float64(42) || node["host_os"] != "linux" {
		t.Fatalf("heartbeat fields not persisted: %#v", node)
	}
}

func TestStaleNodeCASValueRechecksCurrentState(t *testing.T) {
	now := time.Now().Unix()
	tests := []struct {
		name       string
		current    map[string]interface{}
		wantSkip   bool
		wantStatus string
	}{
		{
			name: "fresh concurrent heartbeat skips eviction",
			current: map[string]interface{}{
				"node_uuid": "node-fresh", "status": "active",
				"last_seen_time": now, "preserved": "value",
			},
			wantSkip: true,
		},
		{
			name: "stale current becomes inactive",
			current: map[string]interface{}{
				"node_uuid": "node-stale", "status": "active",
				"last_seen_time": now - HeartbeatTimeout - 1, "preserved": "value",
			},
			wantStatus: "inactive",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			current, err := json.Marshal(tt.current)
			if err != nil {
				t.Fatalf("marshal current: %v", err)
			}
			result, err := staleNodeCASValue(current, now)
			if tt.wantSkip {
				if !errors.Is(err, errNodeNoLongerStale) {
					t.Fatalf("staleNodeCASValue error = %v, want errNodeNoLongerStale", err)
				}
				if result.Value != nil {
					t.Fatalf("skip returned write value %q", result.Value)
				}
				return
			}
			if err != nil {
				t.Fatalf("staleNodeCASValue: %v", err)
			}
			var updated map[string]interface{}
			if err := json.Unmarshal(result.Value, &updated); err != nil {
				t.Fatalf("unmarshal result: %v", err)
			}
			if updated["status"] != tt.wantStatus {
				t.Fatalf("status = %v, want %s", updated["status"], tt.wantStatus)
			}
			if updated["preserved"] != "value" {
				t.Fatalf("unrelated current field was lost: %#v", updated)
			}
		})
	}
}

func TestHeartbeatAndNicModelWritesPreserveEachOther(t *testing.T) {
	gs := newTestGrpcServer(t)
	ctx := context.Background()
	uuid := fmt.Sprintf("test-cas-race-%d", time.Now().UnixNano())
	seedNode(t, gs, uuid, map[string]interface{}{
		"node_uuid": uuid, "node_ip": "127.0.0.1", "status": "active",
		"last_seen_time": time.Now().Unix(), "nic_model_wan": "initial-wan",
		"nic_model_lan": "initial-lan",
	})
	t.Cleanup(func() {
		gs.nodeMonitorMgr.StopMonitoring(uuid)
		gs.etcd.Client().Delete(ctx, "nodes/"+uuid)
	})

	started := make(chan struct{})
	heartbeatErr := make(chan error, 1)
	go func() {
		for i := int64(1); i <= 50; i++ {
			if i == 10 {
				close(started)
			}
			_, err := gs.Heartbeat(ctx, &controllerpb.NodeHeartbeat{
				NodeUuid: uuid, Ip: "127.0.0.1", UptimeTimestamp: i,
			})
			if err != nil {
				heartbeatErr <- err
				return
			}
		}
		heartbeatErr <- nil
	}()

	<-started
	if err := gs.nodeMonitorMgr.writeNicModelsToEtcd(gs.etcd, uuid, "final-wan", "final-lan"); err != nil {
		t.Fatalf("writeNicModelsToEtcd: %v", err)
	}
	if err := <-heartbeatErr; err != nil {
		t.Fatalf("Heartbeat loop: %v", err)
	}

	node, ok := readNode(t, gs, uuid)
	if !ok {
		t.Fatal("node missing after concurrent writes")
	}
	if node["nic_model_wan"] != "final-wan" || node["nic_model_lan"] != "final-lan" {
		t.Fatalf("NIC models lost after heartbeat race: %#v", node)
	}
	if node["uptime"] != float64(50) || node["status"] != "active" {
		t.Fatalf("latest heartbeat fields lost after NIC race: %#v", node)
	}
}

func TestRegisterNodeCarriesOverNicModels(t *testing.T) {
	gs := newTestGrpcServer(t)
	ctx := context.Background()
	uuid := fmt.Sprintf("test-cas-reregister-%d", time.Now().UnixNano())
	oldRegisteredAt := time.Now().Add(-time.Hour).Unix()
	seedNode(t, gs, uuid, map[string]interface{}{
		"node_uuid": uuid, "status": "inactive", "registered_at": oldRegisteredAt,
		"last_seen_time": oldRegisteredAt, "nic_model_wan": "carry-wan",
		"nic_model_lan": "carry-lan", "old_runtime_field": "reset-me",
	})
	t.Cleanup(func() {
		gs.nodeMonitorMgr.StopMonitoring(uuid)
		gs.etcd.Client().Delete(ctx, "nodes/"+uuid)
	})

	reply, err := gs.RegisterNode(ctx, &controllerpb.NodeRegisterRequest{
		NodeUuid: uuid,
		Ip:       "127.0.0.1",
		Version:  "v-reregistered",
		Location: "new-location",
	})
	if err != nil || !reply.Success {
		t.Fatalf("RegisterNode = (%v, %v), want success", reply, err)
	}
	gs.nodeMonitorMgr.StopMonitoring(uuid)

	node, ok := readNode(t, gs, uuid)
	if !ok {
		t.Fatal("re-registered node not found")
	}
	if node["nic_model_wan"] != "carry-wan" || node["nic_model_lan"] != "carry-lan" {
		t.Fatalf("NIC models not carried over: %#v", node)
	}
	registeredAt, ok := node["registered_at"].(float64)
	if !ok || int64(registeredAt) <= oldRegisteredAt {
		t.Fatalf("registered_at = %v, want > %d", node["registered_at"], oldRegisteredAt)
	}
	if node["status"] != "active" || node["version"] != "v-reregistered" {
		t.Fatalf("registration fields were not reset: %#v", node)
	}
	if _, exists := node["old_runtime_field"]; exists {
		t.Fatalf("re-registration retained reset-only field: %#v", node)
	}
}
