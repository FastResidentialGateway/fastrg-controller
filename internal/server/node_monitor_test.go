package server

import "testing"

// TestNodeMonitorManagerLifecycle covers the leader flag and monitor
// add/no-op/remove paths. No node is needed: the gRPC client is created lazily.
func TestNodeMonitorManagerLifecycle(t *testing.T) {
	nmm := NewNodeMonitorManager(nil)

	if nmm.IsLeader() {
		t.Fatal("new manager should not be leader")
	}
	nmm.SetLeader(true)
	if !nmm.IsLeader() {
		t.Fatal("SetLeader(true) not reflected by IsLeader")
	}
	nmm.SetLeader(false)

	if err := nmm.StartMonitoring("node-x", "127.0.0.1"); err != nil {
		t.Fatalf("StartMonitoring: %v", err)
	}
	// Same node + IP is a no-op (no restart, no error).
	if err := nmm.StartMonitoring("node-x", "127.0.0.1"); err != nil {
		t.Fatalf("StartMonitoring (repeat): %v", err)
	}
	nmm.StopMonitoring("node-x")
	// Stopping an unmonitored node is safe.
	nmm.StopMonitoring("never-monitored")
}
