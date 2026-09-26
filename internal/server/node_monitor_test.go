package server

import (
	"context"
	"net"
	"strconv"
	"sync"
	"testing"

	fastrgnodepb "fastrg-controller/proto/fastrgnodepb"

	"google.golang.org/grpc"
)

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

// dhcpInfoNodeServer records each request's user_id and echoes it back.
type dhcpInfoNodeServer struct {
	fastrgnodepb.UnimplementedFastrgServiceServer
	mu      sync.Mutex
	userIDs []uint32
}

func (s *dhcpInfoNodeServer) GetFastrgDhcpInfo(_ context.Context, req *fastrgnodepb.DhcpInfoRequest) (*fastrgnodepb.FastrgDhcpInfo, error) {
	s.mu.Lock()
	s.userIDs = append(s.userIDs, req.GetUserId())
	s.mu.Unlock()
	return &fastrgnodepb.FastrgDhcpInfo{DhcpInfos: []*fastrgnodepb.DhcpInfo{{
		UserId:     req.GetUserId(),
		Status:     "DHCP server is on",
		IpRange:    "192.168.4.2 - 192.168.4.10",
		InuseIps:   []string{"192.168.4.2"},
		InuseCount: 1,
	}}}, nil
}

// TestGetNodeDhcpConfig: the request carries the asked user_id.
func TestGetNodeDhcpConfig(t *testing.T) {
	node := &dhcpInfoNodeServer{}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen for in-process FastRG node: %v", err)
	}
	grpcServer := grpc.NewServer()
	fastrgnodepb.RegisterFastrgServiceServer(grpcServer, node)
	go func() {
		_ = grpcServer.Serve(listener)
	}()
	t.Cleanup(func() {
		grpcServer.Stop()
		_ = listener.Close()
	})
	host, portText, err := net.SplitHostPort(listener.Addr().String())
	if err != nil {
		t.Fatalf("split node address: %v", err)
	}
	port, err := strconv.ParseUint(portText, 10, 32)
	if err != nil {
		t.Fatalf("parse node port: %v", err)
	}

	manager := NewNodeMonitorManager(nil)
	const nodeUUID = "dhcp-info-node"
	if err := manager.StartMonitoring(nodeUUID, host, uint32(port)); err != nil {
		t.Fatalf("StartMonitoring: %v", err)
	}
	t.Cleanup(func() { manager.StopMonitoring(nodeUUID) })

	cfg, found, err := manager.GetNodeDhcpConfig(context.Background(), nodeUUID, "2")
	if err != nil || !found || cfg == nil {
		t.Fatalf("GetNodeDhcpConfig = (%+v, %v, %v), want a config", cfg, found, err)
	}

	node.mu.Lock()
	sent := append([]uint32(nil), node.userIDs...)
	node.mu.Unlock()
	if len(sent) != 1 || sent[0] != 2 {
		t.Fatalf("request user_id(s) = %v, want [2]", sent)
	}
	if cfg.UserID != 2 || len(cfg.InuseIps) != 1 || cfg.CurLeaseCount != 1 {
		t.Fatalf("config = %+v, want user 2 with one in-use IP", cfg)
	}
}
