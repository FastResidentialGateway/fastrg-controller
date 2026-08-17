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

	if err := nmm.StartMonitoring("node-x", "127.0.0.1", 0); err != nil {
		t.Fatalf("StartMonitoring: %v", err)
	}
	t.Cleanup(func() { nmm.StopMonitoring("node-x") })

	nmm.mu.RLock()
	firstMonitor, exists := nmm.monitors["node-x"]
	monitorCount := len(nmm.monitors)
	nmm.mu.RUnlock()
	if !exists {
		t.Fatal("StartMonitoring did not register the node monitor")
	}
	if monitorCount != 1 {
		t.Fatalf("monitor count after StartMonitoring = %d, want 1", monitorCount)
	}

	// Same node + IP is a no-op (no restart, no error).
	if err := nmm.StartMonitoring("node-x", "127.0.0.1", 0); err != nil {
		t.Fatalf("StartMonitoring (repeat): %v", err)
	}

	nmm.mu.RLock()
	repeatedMonitor, exists := nmm.monitors["node-x"]
	monitorCount = len(nmm.monitors)
	nmm.mu.RUnlock()
	if !exists {
		t.Fatal("repeated StartMonitoring removed the node monitor")
	}
	if monitorCount != 1 {
		t.Fatalf("monitor count after repeated StartMonitoring = %d, want 1", monitorCount)
	}
	if repeatedMonitor != firstMonitor {
		t.Fatal("repeated StartMonitoring replaced the existing monitor")
	}

	nmm.StopMonitoring("node-x")
	nmm.mu.RLock()
	_, exists = nmm.monitors["node-x"]
	monitorCount = len(nmm.monitors)
	nmm.mu.RUnlock()
	if exists {
		t.Fatal("StopMonitoring did not remove the node monitor")
	}
	if monitorCount != 0 {
		t.Fatalf("monitor count after StopMonitoring = %d, want 0", monitorCount)
	}

	// Stopping an unmonitored node is safe.
	nmm.StopMonitoring("never-monitored")
}
