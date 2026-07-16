package server

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"time"

	"github.com/sirupsen/logrus"

	"fastrg-controller/internal/storage"
	controllerpb "fastrg-controller/proto"

	clientv3 "go.etcd.io/etcd/client/v3"
	"google.golang.org/grpc"
	"google.golang.org/protobuf/types/known/emptypb"
)

const (
	// HeartbeatTimeout defines how long to wait before considering a node stale (in seconds)
	HeartbeatTimeout = 60
	// CheckInterval defines how often to check for stale nodes (in seconds)
	CheckInterval = 30
)

type GrpcServer struct {
	controllerpb.UnimplementedNodeManagementServer
	etcd           *storage.EtcdClient
	ctx            context.Context
	cancelCtx      context.CancelFunc
	grpcServer     *grpc.Server
	nodeMonitorMgr *NodeMonitorManager
}

func NewGrpcServer(etcd *storage.EtcdClient, nmm *NodeMonitorManager) *GrpcServer {
	ctx, cancel := context.WithCancel(context.Background())
	server := &GrpcServer{
		etcd:           etcd,
		ctx:            ctx,
		cancelCtx:      cancel,
		nodeMonitorMgr: nmm,
	}

	// Start the stale node monitor in a background goroutine
	go server.monitorStaleNodes()

	return server
}

func (s *GrpcServer) Start(addr string, configSvc *ConfigGrpcServer) error {
	lis, err := net.Listen("tcp", addr)
	if err != nil {
		return fmt.Errorf("grpc listen on %s: %w", addr, err)
	}

	s.grpcServer = grpc.NewServer()
	controllerpb.RegisterNodeManagementServer(s.grpcServer, s)
	controllerpb.RegisterConfigServiceServer(s.grpcServer, configSvc)

	logrus.Infof("gRPC server listening at %v", addr)
	// Serve returns nil after GracefulStop (normal shutdown); a non-nil error
	// means the listener died unexpectedly and should trigger process shutdown.
	return s.grpcServer.Serve(lis)
}

func (s *GrpcServer) RegisterNode(ctx context.Context, req *controllerpb.NodeRegisterRequest) (*controllerpb.NodeRegisterReply, error) {
	// Check required fields
	if req.NodeUuid == "" {
		return &controllerpb.NodeRegisterReply{
			Success: false,
			Message: "node_uuid is required",
		}, nil
	}

	// Prepare node data to be stored
	nodeData := map[string]interface{}{
		"node_uuid":      req.NodeUuid,
		"node_ip":        req.Ip,
		"version":        req.Version,
		"location":       req.Location,
		"registered_at":  time.Now().Unix(),
		"last_seen_time": time.Now().Unix(),
		"status":         "active",
	}

	// Serialize node data to JSON
	nodeDataJSON, err := json.Marshal(nodeData)
	if err != nil {
		logrus.WithError(err).Error("Failed to marshal node data")
		return &controllerpb.NodeRegisterReply{
			Success: false,
			Message: "Failed to process node data",
		}, nil
	}

	// Store to etcd, using nodes/{node_uuid} as key
	etcdKey := fmt.Sprintf("nodes/%s", req.NodeUuid)
	_, err = s.etcd.Client().Put(ctx, etcdKey, string(nodeDataJSON))
	if err != nil {
		logrus.WithError(err).Error("Failed to store node data to etcd")
		return &controllerpb.NodeRegisterReply{
			Success: false,
			Message: "Failed to register node",
		}, nil
	}

	logrus.Infof("Node registered successfully: UUID=%s, IP=%s, Version=%s", req.NodeUuid, req.Ip, req.Version)

	// Start monitoring the node; always fetch NIC model since the node itself restarted.
	if err := s.nodeMonitorMgr.StartMonitoring(req.NodeUuid, req.Ip); err != nil {
		logrus.WithError(err).Warnf("Failed to start monitoring node %s", req.NodeUuid)
	} else {
		go s.nodeMonitorMgr.FetchInitialNicModel(req.NodeUuid, s.etcd)
	}

	return &controllerpb.NodeRegisterReply{
		Success: true,
		Message: "Node registered successfully",
	}, nil
}

func (s *GrpcServer) UnregisterNode(ctx context.Context, req *controllerpb.NodeRegisterRequest) (*emptypb.Empty, error) {
	// Check required fields
	if req.NodeUuid == "" {
		logrus.Error("UnregisterNode failed: node_uuid is required")
		return &emptypb.Empty{}, fmt.Errorf("node_uuid is required")
	}

	// Check if the node is registered
	etcdKey := fmt.Sprintf("nodes/%s", req.NodeUuid)
	resp, err := s.etcd.Client().Get(ctx, etcdKey)
	if err != nil {
		logrus.WithError(err).Error("Failed to get node data from etcd")
		return &emptypb.Empty{}, fmt.Errorf("failed to check node registration")
	}

	if len(resp.Kvs) == 0 {
		logrus.Errorf("UnregisterNode failed: node %s not registered", req.NodeUuid)
		return &emptypb.Empty{}, fmt.Errorf("node not registered")
	}
	// Stop monitoring the node
	s.nodeMonitorMgr.StopMonitoring(req.NodeUuid)

	// Delete the node entry from etcd
	_, err = s.etcd.Client().Delete(ctx, etcdKey)
	if err != nil {
		logrus.WithError(err).Error("Failed to delete node data from etcd")
		return &emptypb.Empty{}, fmt.Errorf("failed to unregister node")
	}
	logrus.Infof("Node unregistered successfully: UUID=%s", req.NodeUuid)
	return &emptypb.Empty{}, nil
}

func (s *GrpcServer) Heartbeat(ctx context.Context, req *controllerpb.NodeHeartbeat) (*emptypb.Empty, error) {
	// Check required fields
	if req.GetNodeUuid() == "" {
		logrus.Error("Heartbeat failed: node_uuid is required")
		return &emptypb.Empty{}, fmt.Errorf("node_uuid is required")
	}

	// Check if the node is registered
	etcdKey := fmt.Sprintf("nodes/%s", req.GetNodeUuid())
	resp, err := s.etcd.Client().Get(ctx, etcdKey)
	if err != nil {
		logrus.WithError(err).Error("Failed to get node data from etcd")
		return &emptypb.Empty{}, fmt.Errorf("failed to check node registration")
	}

	if len(resp.Kvs) == 0 {
		logrus.Errorf("Heartbeat failed: node %s not registered", req.GetNodeUuid())
		return &emptypb.Empty{}, fmt.Errorf("node not registered")
	}

	// Deserialize existing node data
	var nodeData map[string]interface{}
	err = json.Unmarshal(resp.Kvs[0].Value, &nodeData)
	if err != nil {
		logrus.WithError(err).Error("Failed to unmarshal node data")
		return &emptypb.Empty{}, fmt.Errorf("failed to process node data")
	}

	// Update node data with heartbeat info
	nodeData["last_seen_time"] = time.Now().Unix()
	nodeData["uuid"] = req.GetNodeUuid()
	nodeData["uptime"] = req.GetUptimeTimestamp()
	nodeData["node_ip"] = req.GetIp()
	nodeData["status"] = "active"
	if req.GetHostOs() != "" {
		nodeData["host_os"] = req.GetHostOs()
	}

	// Serialize updated node data to JSON
	updatedNodeDataJSON, err := json.Marshal(nodeData)
	if err != nil {
		logrus.WithError(err).Error("Failed to marshal updated node data")
		return &emptypb.Empty{}, fmt.Errorf("failed to process updated node data")
	}

	// Update etcd with the new node data
	_, err = s.etcd.Client().Put(ctx, etcdKey, string(updatedNodeDataJSON))
	if err != nil {
		logrus.WithError(err).Error("Failed to update node data in etcd")
		return &emptypb.Empty{}, fmt.Errorf("failed to update node data")
	}

	logrus.Infof("Heartbeat received from node %s: Uptime=%d, IP=%s", req.GetNodeUuid(), req.GetUptimeTimestamp(), req.GetIp())

	// Ensure monitoring is started for this node
	nodeIP := req.GetIp()
	if nodeIP == "" {
		// Try to get IP from existing node data
		if ip, ok := nodeData["node_ip"].(string); ok && ip != "" {
			nodeIP = ip
		}
	}
	if nodeIP != "" {
		if err := s.nodeMonitorMgr.StartMonitoring(req.GetNodeUuid(), nodeIP); err != nil {
			logrus.WithError(err).Warnf("Failed to start monitoring for node %s", req.GetNodeUuid())
		}
	}

	// If nic_model_wan is absent (e.g. first heartbeat after controller restart),
	// fetch it now rather than waiting for the next RegisterNode.
	if _, ok := nodeData["nic_model_wan"]; !ok {
		go s.nodeMonitorMgr.FetchInitialNicModel(req.GetNodeUuid(), s.etcd)
	}

	return &emptypb.Empty{}, nil
}

// monitorStaleNodes runs in a background goroutine and periodically checks for nodes
// that haven't sent a heartbeat within the HeartbeatTimeout period
func (s *GrpcServer) monitorStaleNodes() {
	ticker := time.NewTicker(CheckInterval * time.Second)
	defer ticker.Stop()

	logrus.Infof("Started stale node monitor (checking every %d seconds, timeout: %d seconds)", CheckInterval, HeartbeatTimeout)

	for {
		select {
		case <-s.ctx.Done():
			logrus.Infof("Stopping stale node monitor")
			return
		case <-ticker.C:
			// Stale-node eviction writes to etcd; run it only on the leader so
			// replicas don't redundantly re-mark the same nodes inactive.
			if !s.nodeMonitorMgr.IsLeader() {
				continue
			}
			s.checkAndUnregisterStaleNodes()
		}
	}
}

// checkAndUnregisterStaleNodes checks all registered nodes and unregisters those
// that haven't sent a heartbeat within the HeartbeatTimeout period
func (s *GrpcServer) checkAndUnregisterStaleNodes() {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	// Get all nodes from etcd with prefix "nodes/"
	resp, err := s.etcd.Client().Get(ctx, "nodes/", clientv3.WithPrefix())
	if err != nil {
		logrus.WithError(err).Error("Failed to get nodes from etcd")
		return
	}

	currentTime := time.Now().Unix()
	staleCount := 0

	for _, kv := range resp.Kvs {
		var nodeData map[string]interface{}
		err := json.Unmarshal(kv.Value, &nodeData)
		if err != nil {
			logrus.WithError(err).Errorf("Failed to unmarshal node data for key %s", kv.Key)
			continue
		}

		// Check last_seen_time
		lastSeenTime, ok := nodeData["last_seen_time"].(float64)
		if !ok {
			logrus.Errorf("Node %s has invalid last_seen_time, skipping", kv.Key)
			continue
		}

		timeSinceLastSeen := currentTime - int64(lastSeenTime)

		if timeSinceLastSeen > HeartbeatTimeout {
			// Already marked inactive in a previous cycle; skip so we don't
			// repeatedly re-mark and re-log the same node every CheckInterval.
			if status, _ := nodeData["status"].(string); status == "inactive" {
				continue
			}

			// Node is stale, mark it inactive
			nodeUUID := nodeData["node_uuid"]
			if nodeUUID == nil {
				// Try to extract from key
				keyParts := string(kv.Key)
				if len(keyParts) > 6 { // "nodes/" is 6 characters
					nodeUUID = keyParts[6:]
				}
			}

			logrus.Infof("Node %v is stale (last seen %d seconds ago), marking inactive...", nodeUUID, timeSinceLastSeen)

			// Stop monitoring the node
			s.nodeMonitorMgr.StopMonitoring(fmt.Sprintf("%v", nodeUUID))

			// Mark the node inactive (a recovering node's Heartbeat will flip it
			// back to "active"); keep the entry in etcd for audit/visibility.
			nodeData["status"] = "inactive"
			nodeData["inactive_at"] = currentTime
			nodeData["inactive_reason"] = "heartbeat_timeout"

			updatedNodeDataJSON, err := json.Marshal(nodeData)
			if err != nil {
				logrus.WithError(err).Errorf("Failed to marshal inactive node %v", nodeUUID)
				continue
			}

			// Persist the inactive status back to etcd
			_, err = s.etcd.Client().Put(ctx, string(kv.Key), string(updatedNodeDataJSON))
			if err != nil {
				logrus.WithError(err).Errorf("Failed to mark stale node %v inactive", nodeUUID)
			} else {
				logrus.Infof("Marked stale node inactive: %v", nodeUUID)
				staleCount++
			}
		}
	}

	if staleCount > 0 {
		logrus.Infof("Marked %d stale node(s) inactive in this check cycle", staleCount)
	}
}

// Stop gracefully stops the gRPC server and background monitoring
func (s *GrpcServer) Stop() {
	logrus.Infof("Stopping gRPC server...")
	if s.cancelCtx != nil {
		s.cancelCtx()
	}
	if s.grpcServer != nil {
		s.grpcServer.GracefulStop()
	}
}
