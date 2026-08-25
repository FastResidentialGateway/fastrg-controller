package server

import (
	"context"
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
	calls      atomic.Int32
	eventCount uint32
	fail       bool
}

func (s *republishNodeServer) RepublishPPPoEStatus(context.Context, *emptypb.Empty) (*fastrgnodepb.RepublishPPPoEStatusReply, error) {
	s.calls.Add(1)
	if s.fail {
		return nil, status.Error(codes.Unimplemented, "method RepublishPPPoEStatus not implemented")
	}
	return &fastrgnodepb.RepublishPPPoEStatusReply{EventCount: s.eventCount}, nil
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

// TestAfterNodeRegisteredRepublishes: a successful registration asks the node
// to re-send its PPPoE state.
func TestAfterNodeRegisteredRepublishes(t *testing.T) {
	node := &republishNodeServer{eventCount: 3}
	host, port := startRepublishNode(t, node)

	manager := NewNodeMonitorManager(nil)
	const nodeUUID = "registered-node"
	t.Cleanup(func() { manager.StopMonitoring(nodeUUID) })

	// etcd is nil, so the NIC-model fetch this also starts returns immediately.
	gs := &GrpcServer{nodeMonitorMgr: manager}
	gs.afterNodeRegistered(nodeUUID, host, port)

	waitForCalls(t, node, 1)
	time.Sleep(200 * time.Millisecond)
	if got := node.calls.Load(); got != 1 {
		t.Fatalf("node received %d republish request(s), want exactly 1", got)
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
