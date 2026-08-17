package server

import (
	"encoding/json"
	"testing"

	controllerpb "fastrg-controller/proto"
)

// TestRegisterNodeCASStoresGrpcPort: the port a node reports is what gets
// written to its etcd record, and a node that reports none records 0 rather
// than a guess.
func TestRegisterNodeCASStoresGrpcPort(t *testing.T) {
	for _, tc := range []struct {
		name     string
		reported uint32
		want     float64
	}{
		{"reported port is stored", 60000, 60000},
		{"unreported port is stored as zero", 0, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			req := &controllerpb.NodeRegisterRequest{
				NodeUuid: "node-port",
				Ip:       "127.0.0.1",
				GrpcPort: tc.reported,
			}
			result, err := registerNodeCASValue(nil, req, 1234)
			if err != nil {
				t.Fatalf("registerNodeCASValue: %v", err)
			}

			var stored map[string]interface{}
			if err := json.Unmarshal(result.Value, &stored); err != nil {
				t.Fatalf("unmarshal stored node: %v", err)
			}
			got, ok := stored["grpc_port"].(float64)
			if !ok {
				t.Fatalf("grpc_port missing from stored node: %v", stored)
			}
			if got != tc.want {
				t.Fatalf("stored grpc_port = %v, want %v", got, tc.want)
			}
		})
	}
}

// TestResolveNodeGRPCPort: a node that reports no port is dialled on the
// default, which is what keeps nodes built before this field worked.
func TestResolveNodeGRPCPort(t *testing.T) {
	if got := resolveNodeGRPCPort(0); got != DefaultNodeGRPCPort {
		t.Fatalf("resolveNodeGRPCPort(0) = %d, want %d", got, DefaultNodeGRPCPort)
	}
	if got := resolveNodeGRPCPort(60000); got != 60000 {
		t.Fatalf("resolveNodeGRPCPort(60000) = %d, want 60000", got)
	}
}

// TestStartMonitoringPortHandling: monitoring follows the port the node
// reports. A node that starts reporting the port it was already being dialled
// on must not cause a needless reconnect, but a genuinely different port must.
func TestStartMonitoringPortHandling(t *testing.T) {
	nmm := NewNodeMonitorManager(nil)
	t.Cleanup(func() { nmm.StopMonitoring("node-p") })

	// No port reported: dial the default.
	if err := nmm.StartMonitoring("node-p", "127.0.0.1", 0); err != nil {
		t.Fatalf("StartMonitoring (no port): %v", err)
	}
	nmm.mu.RLock()
	first := nmm.monitors["node-p"]
	nmm.mu.RUnlock()
	if first == nil {
		t.Fatal("StartMonitoring did not register the node monitor")
	}
	if first.nodeGRPCPort != DefaultNodeGRPCPort {
		t.Fatalf("monitor port = %d, want the default %d", first.nodeGRPCPort, DefaultNodeGRPCPort)
	}

	// Now the node reports the very port it was already being dialled on.
	if err := nmm.StartMonitoring("node-p", "127.0.0.1", DefaultNodeGRPCPort); err != nil {
		t.Fatalf("StartMonitoring (same effective port): %v", err)
	}
	nmm.mu.RLock()
	same := nmm.monitors["node-p"]
	nmm.mu.RUnlock()
	if same != first {
		t.Fatal("reporting the port already in use restarted monitoring")
	}

	// A different port has to restart monitoring, or the controller would keep
	// talking to the old address.
	if err := nmm.StartMonitoring("node-p", "127.0.0.1", 60000); err != nil {
		t.Fatalf("StartMonitoring (new port): %v", err)
	}
	nmm.mu.RLock()
	restarted := nmm.monitors["node-p"]
	monitorCount := len(nmm.monitors)
	nmm.mu.RUnlock()
	if restarted == first {
		t.Fatal("a changed port did not restart monitoring")
	}
	if restarted == nil || restarted.nodeGRPCPort != 60000 {
		t.Fatalf("monitor port after change = %v, want 60000", restarted)
	}
	if monitorCount != 1 {
		t.Fatalf("monitor count = %d, want 1", monitorCount)
	}
}
