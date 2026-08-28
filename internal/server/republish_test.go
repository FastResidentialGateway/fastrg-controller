package server

import (
	"context"
	"fmt"
	"net"
	"strconv"
	"sync/atomic"
	"testing"
	"time"

	fastrgnodepb "fastrg-controller/proto/fastrgnodepb"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/emptypb"
)

// republishNodeServer is an in-process stand-in for a FastRG node: it counts
// republish requests and can answer like a node too old to know the RPC.
type republishNodeServer struct {
	fastrgnodepb.UnimplementedFastrgServiceServer
	calls       atomic.Int32
	configCalls atomic.Int32
	eventCount  uint32
	fail        bool
	// handleDelay is how long each request takes to answer, so a test can tell
	// concurrent requests from serial ones.
	handleDelay time.Duration
	// probe, when set, is shared by every node in a test and records how many
	// requests were being served across all of them at once.
	probe *concurrencyProbe
}

// concurrencyProbe counts requests in flight across a whole set of fake nodes
// and remembers the highest count reached.
type concurrencyProbe struct {
	inFlight atomic.Int32
	peak     atomic.Int32
}

func (p *concurrencyProbe) enter() {
	current := p.inFlight.Add(1)
	for {
		peak := p.peak.Load()
		if current <= peak || p.peak.CompareAndSwap(peak, current) {
			return
		}
	}
}

func (p *concurrencyProbe) leave() { p.inFlight.Add(-1) }

func (s *republishNodeServer) enter() {
	if s.probe != nil {
		s.probe.enter()
	}
}

func (s *republishNodeServer) leave() {
	if s.probe != nil {
		s.probe.leave()
	}
}

func (s *republishNodeServer) RepublishPPPoEStatus(context.Context, *emptypb.Empty) (*fastrgnodepb.RepublishPPPoEStatusReply, error) {
	s.calls.Add(1)
	s.enter()
	defer s.leave()
	if s.handleDelay > 0 {
		time.Sleep(s.handleDelay)
	}
	if s.fail {
		return nil, status.Error(codes.Unimplemented, "method RepublishPPPoEStatus not implemented")
	}
	return &fastrgnodepb.RepublishPPPoEStatusReply{EventCount: s.eventCount}, nil
}

func (s *republishNodeServer) RepublishConfigStatus(context.Context, *emptypb.Empty) (*fastrgnodepb.RepublishConfigStatusReply, error) {
	s.configCalls.Add(1)
	s.enter()
	defer s.leave()
	if s.handleDelay > 0 {
		time.Sleep(s.handleDelay)
	}
	if s.fail {
		return nil, status.Error(codes.Unimplemented, "method RepublishConfigStatus not implemented")
	}
	return &fastrgnodepb.RepublishConfigStatusReply{EventCount: s.eventCount}, nil
}

// startRepublishNode serves node until the test ends and returns its address.
func startRepublishNode(t *testing.T, node *republishNodeServer) (string, uint32) {
	t.Helper()

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
	return host, uint32(port)
}

// waitForCalls waits briefly for the node to receive want requests.
func waitForCalls(t *testing.T, node *republishNodeServer, want int32) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if node.calls.Load() >= want {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("node received %d republish request(s), want %d", node.calls.Load(), want)
}

// waitForConfigCalls waits briefly for the node to receive want config status
// requests.
func waitForConfigCalls(t *testing.T, node *republishNodeServer, want int32) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if node.configCalls.Load() >= want {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("node received %d config status request(s), want %d", node.configCalls.Load(), want)
}

// TestRepublishPPPoEStatusCallsNode: a monitored node is asked once; an
// unmonitored node is reported instead of dialled.
func TestRepublishPPPoEStatusCallsNode(t *testing.T) {
	node := &republishNodeServer{eventCount: 2}
	host, port := startRepublishNode(t, node)

	manager := NewNodeMonitorManager(nil)
	const nodeUUID = "republish-node"
	if err := manager.StartMonitoring(nodeUUID, host, port); err != nil {
		t.Fatalf("StartMonitoring: %v", err)
	}
	t.Cleanup(func() { manager.StopMonitoring(nodeUUID) })

	if err := manager.RepublishPPPoEStatus(context.Background(), nodeUUID); err != nil {
		t.Fatalf("RepublishPPPoEStatus: %v", err)
	}
	if got := node.calls.Load(); got != 1 {
		t.Fatalf("node received %d republish request(s), want 1", got)
	}

	if err := manager.RepublishPPPoEStatus(context.Background(), "not-monitored"); err != errNodeNotMonitored {
		t.Fatalf("RepublishPPPoEStatus on unmonitored node = %v, want %v", err, errNodeNotMonitored)
	}
}

// TestRepublishPPPoEStatusOldNodeIsNotRetried: a node that does not implement
// the RPC reports the failure and is asked exactly once.
func TestRepublishPPPoEStatusOldNodeIsNotRetried(t *testing.T) {
	node := &republishNodeServer{fail: true}
	host, port := startRepublishNode(t, node)

	manager := NewNodeMonitorManager(nil)
	const nodeUUID = "old-node"
	if err := manager.StartMonitoring(nodeUUID, host, port); err != nil {
		t.Fatalf("StartMonitoring: %v", err)
	}
	t.Cleanup(func() { manager.StopMonitoring(nodeUUID) })

	err := manager.RepublishPPPoEStatus(context.Background(), nodeUUID)
	if status.Code(err) != codes.Unimplemented {
		t.Fatalf("RepublishPPPoEStatus error = %v, want Unimplemented", err)
	}
	if got := node.calls.Load(); got != 1 {
		t.Fatalf("node received %d republish request(s), want exactly 1 (no retry)", got)
	}
}

// TestRepublishConfigStatusCallsNode: a monitored node is asked once; an
// unmonitored node is reported instead of dialled.
func TestRepublishConfigStatusCallsNode(t *testing.T) {
	node := &republishNodeServer{eventCount: 4}
	host, port := startRepublishNode(t, node)

	manager := NewNodeMonitorManager(nil)
	const nodeUUID = "config-status-node"
	if err := manager.StartMonitoring(nodeUUID, host, port); err != nil {
		t.Fatalf("StartMonitoring: %v", err)
	}
	t.Cleanup(func() { manager.StopMonitoring(nodeUUID) })

	if err := manager.RepublishConfigStatus(context.Background(), nodeUUID); err != nil {
		t.Fatalf("RepublishConfigStatus: %v", err)
	}
	if got := node.configCalls.Load(); got != 1 {
		t.Fatalf("node received %d config status request(s), want 1", got)
	}

	if err := manager.RepublishConfigStatus(context.Background(), "not-monitored"); err != errNodeNotMonitored {
		t.Fatalf("RepublishConfigStatus on unmonitored node = %v, want %v", err, errNodeNotMonitored)
	}
}

// TestRepublishConfigStatusOldNodeIsNotRetried: a node that does not implement
// the RPC reports the failure and is asked exactly once.
func TestRepublishConfigStatusOldNodeIsNotRetried(t *testing.T) {
	node := &republishNodeServer{fail: true}
	host, port := startRepublishNode(t, node)

	manager := NewNodeMonitorManager(nil)
	const nodeUUID = "old-config-node"
	if err := manager.StartMonitoring(nodeUUID, host, port); err != nil {
		t.Fatalf("StartMonitoring: %v", err)
	}
	t.Cleanup(func() { manager.StopMonitoring(nodeUUID) })

	err := manager.RepublishConfigStatus(context.Background(), nodeUUID)
	if status.Code(err) != codes.Unimplemented {
		t.Fatalf("RepublishConfigStatus error = %v, want Unimplemented", err)
	}
	if got := node.configCalls.Load(); got != 1 {
		t.Fatalf("node received %d config status request(s), want exactly 1 (no retry)", got)
	}
}

// TestAfterNodeRegisteredAsksForConfigStatus: registration asks the node which
// config it is running, and does not ask for PPPoE state — a node that has just
// registered has no sessions to report.
func TestAfterNodeRegisteredAsksForConfigStatus(t *testing.T) {
	node := &republishNodeServer{eventCount: 3}
	host, port := startRepublishNode(t, node)

	manager := NewNodeMonitorManager(nil)
	const nodeUUID = "registered-node"
	t.Cleanup(func() { manager.StopMonitoring(nodeUUID) })

	// etcd is nil, so the NIC-model fetch this also starts returns immediately.
	gs := &GrpcServer{nodeMonitorMgr: manager}
	gs.afterNodeRegistered(nodeUUID, host, port)

	waitForConfigCalls(t, node, 1)
	time.Sleep(200 * time.Millisecond)
	if got := node.configCalls.Load(); got != 1 {
		t.Fatalf("node received %d config status request(s), want exactly 1", got)
	}
	if got := node.calls.Load(); got != 0 {
		t.Fatalf("node received %d PPPoE republish request(s), want 0", got)
	}
}

// TestRepublishAllIsBounded: the per-node work RepublishAll runs covers all 100
// nodes, keeps at most republishConcurrency requests in flight, and finishes far
// sooner than asking them one at a time would.
func TestRepublishAllIsBounded(t *testing.T) {
	const (
		nodeCount   = 100
		handleDelay = 20 * time.Millisecond
	)

	manager := NewNodeMonitorManager(nil)
	probe := &concurrencyProbe{}
	nodes := make([]*republishNodeServer, nodeCount)
	targets := make([]republishTarget, nodeCount)
	for i := range nodes {
		nodes[i] = &republishNodeServer{eventCount: 1, handleDelay: handleDelay, probe: probe}
		host, port := startRepublishNode(t, nodes[i])
		uuid := fmt.Sprintf("bounded-node-%03d", i)
		targets[i] = republishTarget{nodeUUID: uuid, nodeIP: host, grpcPort: port}
		t.Cleanup(func() { manager.StopMonitoring(uuid) })
	}

	// Each node answers two requests, so asking them one at a time would take
	// nodeCount * 2 * handleDelay.
	serial := time.Duration(nodeCount) * 2 * handleDelay
	start := time.Now()
	runBounded(context.Background(), targets, func(target republishTarget) {
		manager.republishNode(context.Background(), target)
	})
	elapsed := time.Since(start)

	for i, node := range nodes {
		if got := node.calls.Load(); got != 1 {
			t.Fatalf("node %d received %d PPPoE republish request(s), want 1", i, got)
		}
		if got := node.configCalls.Load(); got != 1 {
			t.Fatalf("node %d received %d config status request(s), want 1", i, got)
		}
	}

	if peak := probe.peak.Load(); peak > republishConcurrency {
		t.Fatalf("%d requests were in flight at once, want at most %d", peak, republishConcurrency)
	}
	if elapsed > serial/5 {
		t.Fatalf("republish of %d nodes took %v, want well under the serial %v", nodeCount, elapsed, serial)
	}
}

// TestParseRepublishTarget: only active nodes with an address are called, and a
// node that never reported a gRPC port falls back to the default port.
func TestParseRepublishTarget(t *testing.T) {
	tests := []struct {
		name     string
		key      string
		value    string
		want     republishTarget
		wantCall bool
	}{
		{
			name:     "active node with reported port",
			key:      "nodes/node-a",
			value:    `{"node_ip":"10.0.0.5","status":"active","grpc_port":50055}`,
			want:     republishTarget{nodeUUID: "node-a", nodeIP: "10.0.0.5", grpcPort: 50055},
			wantCall: true,
		},
		{
			name:     "active node without reported port",
			key:      "nodes/node-b",
			value:    `{"node_ip":"10.0.0.6","status":"active","grpc_port":0}`,
			want:     republishTarget{nodeUUID: "node-b", nodeIP: "10.0.0.6", grpcPort: 0},
			wantCall: true,
		},
		{
			name:  "inactive node",
			key:   "nodes/node-c",
			value: `{"node_ip":"10.0.0.7","status":"inactive","grpc_port":50055}`,
		},
		{
			name:  "node without an address",
			key:   "nodes/node-d",
			value: `{"status":"active"}`,
		},
		{
			name:  "unparsable entry",
			key:   "nodes/node-e",
			value: `not json`,
		},
		{
			name:  "prefix key itself",
			key:   "nodes/",
			value: `{"node_ip":"10.0.0.8","status":"active"}`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, ok := parseRepublishTarget([]byte(tt.key), []byte(tt.value))
			if ok != tt.wantCall {
				t.Fatalf("parseRepublishTarget ok = %v, want %v", ok, tt.wantCall)
			}
			if ok && got != tt.want {
				t.Fatalf("parseRepublishTarget = %+v, want %+v", got, tt.want)
			}
		})
	}
}
