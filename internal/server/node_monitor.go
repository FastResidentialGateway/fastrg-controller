package server

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
	"github.com/sirupsen/logrus"

	"fastrg-controller/internal/db"
	"fastrg-controller/internal/storage"
	"fastrg-controller/internal/utils"
	fastrgnodepb "fastrg-controller/proto/fastrgnodepb"

	"github.com/pkg/errors"
	clientv3 "go.etcd.io/etcd/client/v3"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/protobuf/types/known/emptypb"
)

// NodeMonitor manages the monitoring goroutine for a single node
type NodeMonitor struct {
	nodeUUID string
	nodeIP   string
	// Port actually dialled, after the default has been applied. Comparing the
	// resolved port is what keeps a node that starts reporting the port it was
	// already being dialled on from looking like a change.
	nodeGRPCPort uint32
	ctx          context.Context
	cancel       context.CancelFunc
	grpcConn     *grpc.ClientConn
	fastrgClient fastrgnodepb.FastrgServiceClient
	mgr          *NodeMonitorManager // back-reference for the leadership check
}

// NodeMonitorManager manages all node monitors
type NodeMonitorManager struct {
	mu       sync.RWMutex
	monitors map[string]*NodeMonitor
	// nicFetchInFlight (guarded by mu) tracks nodes whose NIC-model fetch is
	// running, so repeated heartbeats cannot stack duplicate fetch goroutines.
	nicFetchInFlight map[string]struct{}
	database         atomic.Pointer[db.DB] // Optional: for syncing PPPoE status to database
	// leader is true only on the elected leader replica. On-demand REST queries
	// run on every replica (so monitors/gRPC conns exist everywhere), but the
	// background poll-and-write loop runs only on the leader to avoid 3x node
	// load and duplicate pppoe_status writes.
	leader atomic.Bool
	// confirmMu guards confirmState. It is its own lock rather than mu because
	// the config-confirmation sweep does its bookkeeping in one short critical
	// section and then dials nodes with no lock held at all.
	confirmMu    sync.Mutex
	confirmState map[string]*configConfirmState
}

// SetLeader records whether this replica currently holds leadership.
func (nmm *NodeMonitorManager) SetLeader(v bool) { nmm.leader.Store(v) }

// IsLeader reports whether this replica currently holds leadership.
func (nmm *NodeMonitorManager) IsLeader() bool { return nmm.leader.Load() }

// NewNodeMonitorManager creates a new NodeMonitorManager.
// database parameter is optional (can be nil) for stateless recovery of PPPoE status.
func NewNodeMonitorManager(database *db.DB) *NodeMonitorManager {
	nmm := &NodeMonitorManager{
		monitors:         make(map[string]*NodeMonitor),
		nicFetchInFlight: make(map[string]struct{}),
		confirmState:     make(map[string]*configConfirmState),
	}
	nmm.SetDatabase(database)
	return nmm
}

// SetDatabase makes a PostgreSQL connection available to current and future
// node monitors. It is safe to call while monitor loops are running.
func (nmm *NodeMonitorManager) SetDatabase(database *db.DB) {
	nmm.database.Store(database)
}

// Database returns the currently available PostgreSQL connection, if any.
func (nmm *NodeMonitorManager) Database() *db.DB { return nmm.database.Load() }

// DefaultNodeGRPCPort is dialled when a node does not report its own gRPC port,
// which is what every node built before the port was part of registration does.
const DefaultNodeGRPCPort uint32 = 50052

// resolveNodeGRPCPort maps a reported port onto the one to dial. Nodes that do
// not report a port send 0.
func resolveNodeGRPCPort(reported uint32) uint32 {
	if reported == 0 {
		return DefaultNodeGRPCPort
	}
	return reported
}

// StartMonitoring starts monitoring a node. No-ops when monitoring is already
// active for this node at the same address. grpcPort is the port the node
// reported at registration; 0 means it did not report one.
func (nmm *NodeMonitorManager) StartMonitoring(nodeUUID, nodeIP string, grpcPort uint32) error {
	nmm.mu.Lock()
	defer nmm.mu.Unlock()

	port := resolveNodeGRPCPort(grpcPort)

	// Check if already monitoring this node
	if existing, exists := nmm.monitors[nodeUUID]; exists {
		if existing.nodeIP == nodeIP && existing.nodeGRPCPort == port {
			// Same node and address — gRPC connection is still valid, no restart needed.
			return nil
		}
		logrus.Infof("Node %s address changed %s:%d -> %s:%d, restarting monitoring",
			nodeUUID, existing.nodeIP, existing.nodeGRPCPort, nodeIP, port)
		nmm.stopMonitoringLocked(nodeUUID)
	}

	// Create gRPC connection to the node
	nodeAddr := fmt.Sprintf("%s:%d", nodeIP, port)
	conn, err := grpc.NewClient(nodeAddr, grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		logrus.WithError(err).Errorf("failed to connect to node %s at %s", nodeUUID, nodeAddr)
		return errors.Wrapf(err, "failed to connect to node %s at %s", nodeUUID, nodeAddr)
	}

	// Create FastRG service client
	fastrgClient := fastrgnodepb.NewFastrgServiceClient(conn)

	// Create context with cancel
	ctx, cancel := context.WithCancel(context.Background())

	// Create node monitor
	monitor := &NodeMonitor{
		nodeUUID:     nodeUUID,
		nodeIP:       nodeIP,
		nodeGRPCPort: port,
		ctx:          ctx,
		cancel:       cancel,
		grpcConn:     conn,
		fastrgClient: fastrgClient,
		mgr:          nmm,
	}

	// Store monitor
	nmm.monitors[nodeUUID] = monitor

	logrus.Infof("Started monitoring node %s at %s", nodeUUID, nodeAddr)
	return nil
}

// StopMonitoring stops monitoring a node
func (nmm *NodeMonitorManager) StopMonitoring(nodeUUID string) {
	nmm.mu.Lock()
	defer nmm.mu.Unlock()
	nmm.stopMonitoringLocked(nodeUUID)
}

// stopMonitoringLocked stops monitoring a node (must be called with lock held)
func (nmm *NodeMonitorManager) stopMonitoringLocked(nodeUUID string) {
	monitor, exists := nmm.monitors[nodeUUID]
	if !exists {
		logrus.Infof("Node %s is not being monitored", nodeUUID)
		return
	}

	// Cancel context to stop goroutine
	monitor.cancel()

	// Close gRPC connection
	if monitor.grpcConn != nil {
		monitor.grpcConn.Close()
	}

	// Remove from map
	delete(nmm.monitors, nodeUUID)

	logrus.Infof("Stopped monitoring node %s", nodeUUID)
}

// beginNicFetch marks nodeUUID's NIC-model fetch as in flight. It returns
// false when a fetch for the same node is already running.
func (nmm *NodeMonitorManager) beginNicFetch(nodeUUID string) bool {
	nmm.mu.Lock()
	defer nmm.mu.Unlock()
	if _, running := nmm.nicFetchInFlight[nodeUUID]; running {
		return false
	}
	nmm.nicFetchInFlight[nodeUUID] = struct{}{}
	return true
}

// endNicFetch clears the in-flight mark set by beginNicFetch.
func (nmm *NodeMonitorManager) endNicFetch(nodeUUID string) {
	nmm.mu.Lock()
	defer nmm.mu.Unlock()
	delete(nmm.nicFetchInFlight, nodeUUID)
}

// FetchInitialNicModel dials the registered node once to retrieve NIC model info
// (nics[0]=WAN, nics[1]=LAN) and persists it into etcd. Called as a goroutine
// after StartMonitoring so RegisterNode is not blocked.
// If the gRPC call or etcd write ultimately fails after retries, "unknown" is stored.
func (nmm *NodeMonitorManager) FetchInitialNicModel(nodeUUID string, etcd *storage.EtcdClient) {
	if etcd == nil {
		return
	}
	if !nmm.beginNicFetch(nodeUUID) {
		return // a fetch for this node is already in flight
	}
	defer nmm.endNicFetch(nodeUUID)

	// The node's gRPC server may not be ready immediately after RegisterNode.
	const grpcMaxRetries = 5
	const grpcRetryDelay = 3 * time.Second

	var sysInfo *fastrgnodepb.FastrgSystemStatsInfo
	for attempt := range grpcMaxRetries {
		nmm.mu.RLock()
		monitor, exists := nmm.monitors[nodeUUID]
		nmm.mu.RUnlock()
		if !exists {
			return
		}

		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		var err error
		sysInfo, err = monitor.fastrgClient.GetFastrgSystemStats(ctx, &emptypb.Empty{})
		cancel()
		if err == nil {
			break
		}
		logrus.WithError(err).Warnf("FetchInitialNicModel: gRPC attempt %d/%d failed for node %s", attempt+1, grpcMaxRetries, nodeUUID)
		if attempt < grpcMaxRetries-1 {
			time.Sleep(grpcRetryDelay)
		}
	}

	// Extract WAN (nics[0]) and LAN (nics[1]) model names.
	wanModel := "unknown"
	lanModel := "unknown"
	if sysInfo != nil {
		if len(sysInfo.Nics) > 0 && sysInfo.Nics[0].NicModel != "" {
			wanModel = sysInfo.Nics[0].NicModel
		}
		if len(sysInfo.Nics) > 1 && sysInfo.Nics[1].NicModel != "" {
			lanModel = sysInfo.Nics[1].NicModel
		}
	}

	if err := nmm.writeNicModelsToEtcd(etcd, nodeUUID, wanModel, lanModel); err != nil {
		logrus.WithError(err).Warnf("FetchInitialNicModel: etcd write failed for node %s, storing unknown", nodeUUID)
		if err2 := nmm.writeNicModelsToEtcd(etcd, nodeUUID, "unknown", "unknown"); err2 != nil {
			logrus.WithError(err2).Errorf("FetchInitialNicModel: failed to store fallback unknown for node %s", nodeUUID)
		}
	}
}

// writeNicModelsToEtcd updates nic_model_wan and nic_model_lan in the node's etcd
// entry, retrying up to 3 times on transient failures.
func (nmm *NodeMonitorManager) writeNicModelsToEtcd(etcd *storage.EtcdClient, nodeUUID, wanModel, lanModel string) error {
	const maxRetries = 3
	const retryDelay = 2 * time.Second

	var lastErr error
	for attempt := range maxRetries {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		lastErr = nmm.doWriteNicModels(ctx, etcd, nodeUUID, wanModel, lanModel)
		cancel()
		if lastErr == nil {
			return nil
		}
		logrus.WithError(lastErr).Warnf("writeNicModelsToEtcd: attempt %d/%d failed for node %s", attempt+1, maxRetries, nodeUUID)
		if attempt < maxRetries-1 {
			time.Sleep(retryDelay)
		}
	}
	return lastErr
}

func (nmm *NodeMonitorManager) doWriteNicModels(ctx context.Context, etcd *storage.EtcdClient, nodeUUID, wanModel, lanModel string) error {
	etcdKey := fmt.Sprintf("nodes/%s", nodeUUID)
	return etcd.CAS(ctx, etcdKey, func(current []byte) (storage.CASResult, error) {
		if current == nil {
			return storage.CASResult{}, fmt.Errorf("node %s: %w", nodeUUID, errNodeNotRegistered)
		}

		var nodeData map[string]interface{}
		if err := json.Unmarshal(current, &nodeData); err != nil {
			return storage.CASResult{}, err
		}
		nodeData["nic_model_wan"] = wanModel
		nodeData["nic_model_lan"] = lanModel

		updated, err := json.Marshal(nodeData)
		if err != nil {
			return storage.CASResult{}, err
		}
		return storage.CASResult{Value: updated}, nil
	})
}

// DhcpLeaseResult holds DHCP lease information for one user
type DhcpLeaseResult struct {
	CurLeaseCount int
	MaxLeaseCount int
	InuseIps      []string
	Status        string
}

// GetNodeDhcpLease fetches real-time DHCP lease info for a given node and user via gRPC.
// Returns (result, found, err). found is false when the node is not actively monitored or
// the user has no DHCP info on the node.
func (nmm *NodeMonitorManager) GetNodeDhcpLease(ctx context.Context, nodeUUID, userID string) (*DhcpLeaseResult, bool, error) {
	nmm.mu.RLock()
	monitor, exists := nmm.monitors[nodeUUID]
	nmm.mu.RUnlock()
	if !exists {
		return nil, false, nil
	}

	dhcpInfo, err := monitor.fastrgClient.GetFastrgDhcpInfo(ctx, &emptypb.Empty{})
	if err != nil {
		return nil, false, err
	}

	for _, info := range dhcpInfo.DhcpInfos {
		if fmt.Sprint(info.UserId) == userID {
			curCount := len(info.InuseIps)
			maxCount := 0
			if info.IpRange != "" && info.IpRange != "Not configured" {
				ipStart, ipEnd, perr := utils.ParseIPRange(info.IpRange)
				if perr == nil {
					startUint, serr := utils.IPv4toInt(ipStart)
					endUint, eerr := utils.IPv4toInt(ipEnd)
					if serr == nil && eerr == nil {
						maxCount = int(endUint-startUint) + 1
					}
				}
			}
			return &DhcpLeaseResult{
				CurLeaseCount: curCount,
				MaxLeaseCount: maxCount,
				InuseIps:      info.InuseIps,
				Status:        info.Status,
			}, true, nil
		}
	}

	// user not found in DHCP info — return zero counts
	return &DhcpLeaseResult{CurLeaseCount: 0, MaxLeaseCount: 0, InuseIps: nil, Status: ""}, true, nil
}

// ArpTableEntry holds a single ARP table entry
type ArpTableEntry struct {
	EntryID uint32 `json:"entry_id"`
	IP      string `json:"ip"`
	MAC     string `json:"mac"`
}

// ArpTableResult holds ARP table information for a user
type ArpTableResult struct {
	UserID     uint32          `json:"user_id"`
	TotalCount uint32          `json:"total_count"`
	Entries    []ArpTableEntry `json:"entries"`
}

// GetNodeArpTable fetches real-time ARP table info for a given node and user via gRPC.
func (nmm *NodeMonitorManager) GetNodeArpTable(ctx context.Context, nodeUUID, userID string) (*ArpTableResult, bool, error) {
	nmm.mu.RLock()
	monitor, exists := nmm.monitors[nodeUUID]
	nmm.mu.RUnlock()
	if !exists {
		return nil, false, nil
	}

	// Parse user ID as uint32
	uid64, err := strconv.ParseUint(userID, 10, 32)
	if err != nil {
		return nil, false, fmt.Errorf("invalid user ID: %v", err)
	}
	uid := uint32(uid64)

	arpReply, err := monitor.fastrgClient.GetArpTable(ctx, &fastrgnodepb.ArpTableRequest{
		UserId:   uid,
		MaxCount: 0,
	})
	if err != nil {
		return nil, false, err
	}

	entries := make([]ArpTableEntry, 0)
	if arpReply.Entries != nil {
		for _, entry := range arpReply.Entries {
			entries = append(entries, ArpTableEntry{
				EntryID: entry.EntryId,
				IP:      entry.Ip,
				MAC:     entry.Mac,
			})
		}
	}

	return &ArpTableResult{
		UserID:     arpReply.UserId,
		TotalCount: arpReply.TotalCount,
		Entries:    entries,
	}, true, nil
}

// DnsCacheEntry holds a single DNS cache entry
type DnsCacheEntry struct {
	Domain       string `json:"domain"`
	Qtype        uint32 `json:"qtype"`
	TTL          uint32 `json:"ttl"`
	RemainingTTL uint32 `json:"remaining_ttl"`
	HitCount     uint32 `json:"hit_count"`
}

// DnsCacheResult holds DNS cache information for a user
type DnsCacheResult struct {
	UserID       uint32          `json:"user_id"`
	TotalEntries uint32          `json:"total_entries"`
	Entries      []DnsCacheEntry `json:"entries"`
}

// GetNodeDnsCache fetches real-time DNS cache info for a given node and user via gRPC.
func (nmm *NodeMonitorManager) GetNodeDnsCache(ctx context.Context, nodeUUID, userID string) (*DnsCacheResult, bool, error) {
	nmm.mu.RLock()
	monitor, exists := nmm.monitors[nodeUUID]
	nmm.mu.RUnlock()
	if !exists {
		return nil, false, nil
	}

	// Parse user ID as uint32
	uid64, err := strconv.ParseUint(userID, 10, 32)
	if err != nil {
		return nil, false, fmt.Errorf("invalid user ID: %v", err)
	}
	uid := uint32(uid64)

	dnsReply, err := monitor.fastrgClient.GetDnsCache(ctx, &fastrgnodepb.DnsCacheRequest{
		UserId: uid,
	})
	if err != nil {
		return nil, false, err
	}

	entries := make([]DnsCacheEntry, 0)
	if dnsReply.Entries != nil {
		for _, entry := range dnsReply.Entries {
			entries = append(entries, DnsCacheEntry{
				Domain:       entry.Domain,
				Qtype:        entry.Qtype,
				TTL:          entry.Ttl,
				RemainingTTL: entry.RemainingTtl,
				HitCount:     entry.HitCount,
			})
		}
	}

	return &DnsCacheResult{
		UserID:       dnsReply.UserId,
		TotalEntries: dnsReply.TotalEntries,
		Entries:      entries,
	}, true, nil
}

// PPPoEInfo holds PPPoE session information for a user
type PPPoEInfo struct {
	UserID     uint32   `json:"user_id"`
	SessionID  uint32   `json:"session_id"`
	ClientIP   string   `json:"client_ip"`
	ServerIP   string   `json:"server_ip"`
	DnsServers []string `json:"dns_servers"`
	Status     string   `json:"status"`
}

// GetNodePPPoEInfo fetches real-time PPPoE info for a given node and user via gRPC.
func (nmm *NodeMonitorManager) GetNodePPPoEInfo(ctx context.Context, nodeUUID, userID string) (*PPPoEInfo, bool, error) {
	nmm.mu.RLock()
	monitor, exists := nmm.monitors[nodeUUID]
	nmm.mu.RUnlock()
	if !exists {
		return nil, false, nil
	}

	// Parse user ID as uint32
	uid64, err := strconv.ParseUint(userID, 10, 32)
	if err != nil {
		return nil, false, fmt.Errorf("invalid user ID: %v", err)
	}
	uid := uint32(uid64)

	hsiReply, err := monitor.fastrgClient.GetFastrgHsiInfo(ctx, &emptypb.Empty{})
	if err != nil {
		return nil, false, err
	}

	// Find the HSI info for this user
	for _, hsiInfo := range hsiReply.HsiInfos {
		if hsiInfo.UserId == uid {
			return &PPPoEInfo{
				UserID:     hsiInfo.UserId,
				SessionID:  hsiInfo.SessionId,
				ClientIP:   hsiInfo.IpAddr,
				ServerIP:   hsiInfo.Gateway,
				DnsServers: hsiInfo.Dnss,
				Status:     hsiInfo.Status,
			}, true, nil
		}
	}

	// User not found
	return nil, true, nil
}

// DhcpConfig holds DHCP server configuration for a user
type DhcpConfig struct {
	UserID        uint32   `json:"user_id"`
	Status        string   `json:"status"`
	IpRange       string   `json:"ip_range"`
	SubnetMask    string   `json:"subnet_mask"`
	Gateway       string   `json:"gateway"`
	InuseIps      []string `json:"inuse_ips"`
	CurLeaseCount int      `json:"cur_lease_count"`
	MaxLeaseCount int      `json:"max_lease_count"`
}

// GetNodeDhcpConfig fetches real-time DHCP config for a given node and user via gRPC.
func (nmm *NodeMonitorManager) GetNodeDhcpConfig(ctx context.Context, nodeUUID, userID string) (*DhcpConfig, bool, error) {
	nmm.mu.RLock()
	monitor, exists := nmm.monitors[nodeUUID]
	nmm.mu.RUnlock()
	if !exists {
		return nil, false, nil
	}

	// Parse user ID as uint32
	uid64, err := strconv.ParseUint(userID, 10, 32)
	if err != nil {
		return nil, false, fmt.Errorf("invalid user ID: %v", err)
	}
	uid := uint32(uid64)

	dhcpReply, err := monitor.fastrgClient.GetFastrgDhcpInfo(ctx, &emptypb.Empty{})
	if err != nil {
		return nil, false, err
	}

	// Find the DHCP info for this user
	for _, dhcpInfo := range dhcpReply.DhcpInfos {
		if dhcpInfo.UserId == uid {
			curCount := len(dhcpInfo.InuseIps)
			maxCount := 0
			if dhcpInfo.IpRange != "" && dhcpInfo.IpRange != "Not configured" {
				ipStart, ipEnd, perr := utils.ParseIPRange(dhcpInfo.IpRange)
				if perr == nil {
					startUint, serr := utils.IPv4toInt(ipStart)
					endUint, eerr := utils.IPv4toInt(ipEnd)
					if serr == nil && eerr == nil {
						maxCount = int(endUint-startUint) + 1
					}
				}
			}

			return &DhcpConfig{
				UserID:        dhcpInfo.UserId,
				Status:        dhcpInfo.Status,
				IpRange:       dhcpInfo.IpRange,
				SubnetMask:    dhcpInfo.SubnetMask,
				Gateway:       dhcpInfo.Gateway,
				InuseIps:      dhcpInfo.InuseIps,
				CurLeaseCount: curCount,
				MaxLeaseCount: maxCount,
			}, true, nil
		}
	}

	// User not found
	return nil, true, nil
}

// nodeKeyPrefix is the etcd prefix under which registered nodes are stored.
const nodeKeyPrefix = "nodes/"

// republishRPCTimeout bounds one republish call. A node that cannot answer
// within it is treated as a failed republish.
const republishRPCTimeout = 10 * time.Second

// republishConcurrency bounds how many nodes are asked at once. At the design
// target of 100 nodes, 16 in flight caps a full sweep at roughly 7 rounds of the
// 10s timeout instead of the ~1000s a strictly serial sweep would take.
const republishConcurrency = 16

// errNodeNotMonitored says the node has no gRPC connection on this replica, so
// there is nothing to ask.
var errNodeNotMonitored = errors.New("node is not being monitored")

// RepublishPPPoEStatus asks one node to re-emit the current PPPoE state of every
// subscriber as Kafka events. That is how pppoe_status rows come back after the
// Kafka log or the table itself lost them: the events travel the normal consumer
// path, so there is still exactly one writer of the table.
//
// Failures are logged and dropped, never retried — a node too old to know the
// RPC answers Unimplemented, and the next trigger (controller restart or node
// re-registration) covers whatever this attempt missed.
func (nmm *NodeMonitorManager) RepublishPPPoEStatus(ctx context.Context, nodeUUID string) error {
	nmm.mu.RLock()
	monitor, exists := nmm.monitors[nodeUUID]
	nmm.mu.RUnlock()
	if !exists {
		logrus.Warnf("RepublishPPPoEStatus: node %s is not being monitored", nodeUUID)
		return errNodeNotMonitored
	}

	callCtx, cancel := context.WithTimeout(ctx, republishRPCTimeout)
	defer cancel()

	reply, err := monitor.fastrgClient.RepublishPPPoEStatus(callCtx, &emptypb.Empty{})
	if err != nil {
		logrus.WithError(err).Warnf("RepublishPPPoEStatus: node %s did not republish its PPPoE status", nodeUUID)
		return err
	}

	logrus.Infof("Node %s republished %d PPPoE status event(s)", nodeUUID, reply.GetEventCount())
	return nil
}

// RepublishConfigStatus asks one node to restate which config it is running for
// every subscriber. The node compares etcd against its own copy, re-applies
// where they differ, and reports each subscriber as a Kafka event — so a
// config-apply result the controller never received is repaired through the
// normal consumer path.
//
// The reply only says how many subscribers were queued; the results arrive
// later as Kafka events. Whether the node actually caught up is therefore
// decided by the next confirmation sweep, not by this call.
//
// Failures are logged and dropped, never retried — a node too old to know the
// RPC answers Unimplemented, and the next sweep covers whatever this missed.
func (nmm *NodeMonitorManager) RepublishConfigStatus(ctx context.Context, nodeUUID string) error {
	nmm.mu.RLock()
	monitor, exists := nmm.monitors[nodeUUID]
	nmm.mu.RUnlock()
	if !exists {
		logrus.Warnf("RepublishConfigStatus: node %s is not being monitored", nodeUUID)
		return errNodeNotMonitored
	}

	callCtx, cancel := context.WithTimeout(ctx, republishRPCTimeout)
	defer cancel()

	reply, err := monitor.fastrgClient.RepublishConfigStatus(callCtx, &emptypb.Empty{})
	if err != nil {
		logrus.WithError(err).Warnf("RepublishConfigStatus: node %s did not restate its config status", nodeUUID)
		return err
	}

	logrus.Infof("Node %s queued %d subscriber(s) for a config status re-check", nodeUUID, reply.GetEventCount())
	return nil
}

// RepublishAll asks every active registered node to restate both the PPPoE state
// and the config status of every subscriber. The Kafka consumer runs it once at
// startup, so restarting the controller is the operator's single recovery action
// after the Kafka log was truncated or the PostgreSQL read model was rebuilt.
//
// Nodes are asked republishConcurrency at a time: a node that has gone quiet
// costs a full RPC timeout, and serially that alone would outlast the outage
// the sweep is meant to repair.
func (nmm *NodeMonitorManager) RepublishAll(ctx context.Context, etcd *storage.EtcdClient) {
	if etcd == nil {
		return
	}

	resp, err := etcd.Client().Get(ctx, nodeKeyPrefix, clientv3.WithPrefix())
	if err != nil {
		logrus.WithError(err).Warn("RepublishAll: failed to list registered nodes")
		return
	}

	var targets []republishTarget
	for _, kv := range resp.Kvs {
		if target, ok := parseRepublishTarget(kv.Key, kv.Value); ok {
			targets = append(targets, target)
		}
	}

	runBounded(ctx, targets, func(target republishTarget) {
		nmm.republishNode(ctx, target)
	})
}

// republishNode asks one node for everything the controller projects from its
// events. Each request is warn-only: a node that cannot answer is left to the
// next trigger rather than holding up the others.
func (nmm *NodeMonitorManager) republishNode(ctx context.Context, target republishTarget) {
	// A freshly started controller has no monitors yet, so the connection these
	// calls need is created here rather than waited for.
	if err := nmm.StartMonitoring(target.nodeUUID, target.nodeIP, target.grpcPort); err != nil {
		logrus.WithError(err).Warnf("RepublishAll: cannot connect to node %s", target.nodeUUID)
		return
	}
	_ = nmm.RepublishPPPoEStatus(ctx, target.nodeUUID)
	_ = nmm.RepublishConfigStatus(ctx, target.nodeUUID)
}

// runBounded calls fn for every target with at most republishConcurrency calls
// in flight and returns once they have all finished. A cancelled context stops
// new calls from starting; the ones already running end on their own timeout.
func runBounded(ctx context.Context, targets []republishTarget, fn func(republishTarget)) {
	var wg sync.WaitGroup
	slots := make(chan struct{}, republishConcurrency)

	for _, target := range targets {
		if ctx.Err() != nil {
			break
		}
		slots <- struct{}{}
		wg.Add(1)
		go func() {
			defer wg.Done()
			defer func() { <-slots }()
			fn(target)
		}()
	}
	wg.Wait()
}

// republishTarget is one node RepublishAll calls.
type republishTarget struct {
	nodeUUID string
	nodeIP   string
	grpcPort uint32
}

// parseRepublishTarget reads one etcd nodes/ entry. ok is false for entries that
// are not an active node with an address to dial, which cannot be called at all.
func parseRepublishTarget(key, value []byte) (republishTarget, bool) {
	nodeUUID := strings.TrimPrefix(string(key), nodeKeyPrefix)
	if nodeUUID == "" {
		return republishTarget{}, false
	}

	var nodeData map[string]interface{}
	if err := json.Unmarshal(value, &nodeData); err != nil {
		return republishTarget{}, false
	}
	if status, _ := nodeData["status"].(string); status != "active" {
		return republishTarget{}, false
	}
	nodeIP, _ := nodeData["node_ip"].(string)
	if nodeIP == "" {
		return republishTarget{}, false
	}

	// JSON numbers decode as float64; anything else means "not reported".
	var grpcPort uint32
	if port, ok := nodeData["grpc_port"].(float64); ok && port > 0 {
		grpcPort = uint32(port)
	}
	return republishTarget{nodeUUID: nodeUUID, nodeIP: nodeIP, grpcPort: grpcPort}, true
}

// The config-confirmation sweep closes the one gap a delivery acknowledgement
// cannot cover: the controller knows which config it pushed (the ModRevision of
// each configs/<node>/hsi/<user> key) and which config the node itself attested
// to running (hsi_config_current.mod_revision, taken from the node's own
// CONFIG_APPLY_OK). A result lost between the node and the consumer leaves those
// two apart forever, because nothing re-sends it on its own. The sweep spots the
// gap and asks the node to restate its config status.
const (
	// configConfirmTimeout is how long a pushed config may stay unconfirmed
	// before its node is asked to restate it. It sits well above a normal
	// apply-and-report round trip so an apply still in flight is left alone.
	configConfirmTimeout = 60 * time.Second

	// configConfirmSweepInterval is how often the periodic sweep runs.
	configConfirmSweepInterval = 30 * time.Second

	// configConfirmBackoffMax bounds how rarely a node that never converges is
	// asked again, so a permanently broken node cannot be nudged every sweep
	// forever.
	configConfirmBackoffMax = 15 * time.Minute
)

// configUnconfirmedNodes reports how many active nodes are running behind the
// config the controller pushed. It is observation only; the sweep acts on the
// same data regardless of who is watching.
var configUnconfirmedNodes = promauto.NewGauge(prometheus.GaugeOpts{
	Name: "fastrg_config_unconfirmed_nodes",
	Help: "Active nodes with at least one pushed HSI config they have not confirmed applying.",
})

// configConfirmState is one node's place in the sweep's backoff. It exists only
// while that node has an unconfirmed config.
type configConfirmState struct {
	// unconfirmedSince is when the sweep first saw this node fall behind.
	unconfirmedSince time.Time
	// nextNudge is the earliest time this node may be asked again.
	nextNudge time.Time
	// backoff is the wait added after the most recent request.
	backoff time.Duration
}

// configKeyPrefix is the etcd prefix under which per-subscriber configs live.
const configKeyPrefix = "configs/"

// parseHSIConfigKey reads a configs/<node>/hsi/<user> key. ok is false for
// anything else under the prefix — DNS records, for instance — which carries no
// config-apply confirmation.
func parseHSIConfigKey(key []byte) (db.ConfigKey, bool) {
	text := string(key)
	if !strings.HasPrefix(text, configKeyPrefix) {
		return db.ConfigKey{}, false
	}
	parts := strings.Split(strings.TrimPrefix(text, configKeyPrefix), "/")
	if len(parts) != 3 || parts[1] != "hsi" || parts[0] == "" || parts[2] == "" {
		return db.ConfigKey{}, false
	}
	return db.ConfigKey{NodeUUID: parts[0], UserID: parts[2]}, true
}

// RunConfigConfirmationSweep sweeps every configConfirmSweepInterval until ctx
// is cancelled. It is a singleton background worker: one replica running it is
// enough, and three would triple the request load on every node.
func (nmm *NodeMonitorManager) RunConfigConfirmationSweep(ctx context.Context, etcd *storage.EtcdClient) {
	ticker := time.NewTicker(configConfirmSweepInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			nmm.SweepConfigConfirmations(ctx, etcd)
		}
	}
}

// SweepConfigConfirmations asks every active node whose pushed config has been
// unconfirmed for longer than configConfirmTimeout to restate its config status.
// The request only queues work on the node, so convergence is checked by the
// next sweep rather than by the reply.
//
// Nodes that etcd no longer lists as active are skipped: heartbeat eviction has
// already decided they are gone, and dialling them would only burn RPC timeouts.
func (nmm *NodeMonitorManager) SweepConfigConfirmations(ctx context.Context, etcd *storage.EtcdClient) {
	database := nmm.Database()
	if etcd == nil || database == nil {
		return
	}

	pending, err := unconfirmedNodes(ctx, etcd, database)
	if err != nil {
		logrus.WithError(err).Warn("config confirmation sweep: cannot compare pushed config against confirmed config")
		return
	}
	configUnconfirmedNodes.Set(float64(len(pending)))

	due := nmm.dueForNudge(pending, time.Now())
	if len(due) == 0 {
		return
	}

	runBounded(ctx, due, func(target republishTarget) {
		if err := nmm.StartMonitoring(target.nodeUUID, target.nodeIP, target.grpcPort); err != nil {
			logrus.WithError(err).Warnf("config confirmation sweep: cannot connect to node %s", target.nodeUUID)
			return
		}
		_ = nmm.RepublishConfigStatus(ctx, target.nodeUUID)
	})
}

// unconfirmedNodes returns the active nodes holding at least one HSI config
// whose etcd ModRevision is newer than the one the node has confirmed.
func unconfirmedNodes(ctx context.Context, etcd *storage.EtcdClient, database *db.DB) (map[string]republishTarget, error) {
	nodesResp, err := etcd.Client().Get(ctx, nodeKeyPrefix, clientv3.WithPrefix())
	if err != nil {
		return nil, errors.Wrap(err, "list registered nodes")
	}
	active := make(map[string]republishTarget)
	for _, kv := range nodesResp.Kvs {
		if target, ok := parseRepublishTarget(kv.Key, kv.Value); ok {
			active[target.nodeUUID] = target
		}
	}
	if len(active) == 0 {
		return nil, nil
	}

	confirmed, err := database.ListCurrentModRevisions(ctx)
	if err != nil {
		return nil, errors.Wrap(err, "read confirmed config revisions")
	}

	configsResp, err := etcd.Client().Get(ctx, configKeyPrefix, clientv3.WithPrefix())
	if err != nil {
		return nil, errors.Wrap(err, "list pushed configs")
	}

	pushed := make(map[db.ConfigKey]int64)
	for _, kv := range configsResp.Kvs {
		if configKey, ok := parseHSIConfigKey(kv.Key); ok {
			pushed[configKey] = kv.ModRevision
		}
	}
	return unconfirmedFromRevisions(active, confirmed, pushed), nil
}

// unconfirmedFromRevisions picks the active nodes holding at least one config
// whose pushed revision is newer than the revision the node confirmed applying.
func unconfirmedFromRevisions(
	active map[string]republishTarget,
	confirmed map[db.ConfigKey]int64,
	pushed map[db.ConfigKey]int64,
) map[string]republishTarget {
	pending := make(map[string]republishTarget)
	for configKey, pushedRevision := range pushed {
		target, isActive := active[configKey.NodeUUID]
		if !isActive {
			continue
		}
		// A config the node never confirmed has no row, which reads as revision
		// 0 and is therefore behind any real config.
		if confirmed[configKey] >= pushedRevision {
			continue
		}
		pending[configKey.NodeUUID] = target
	}
	return pending
}

// dueForNudge advances the per-node backoff and returns the nodes to ask now.
// A node is asked once its config has been unconfirmed for configConfirmTimeout
// and then only as often as its growing backoff allows. A node that catches up
// loses its state, so its next lag starts over from the full timeout.
func (nmm *NodeMonitorManager) dueForNudge(pending map[string]republishTarget, now time.Time) []republishTarget {
	nmm.confirmMu.Lock()
	defer nmm.confirmMu.Unlock()

	for nodeUUID := range nmm.confirmState {
		if _, stillPending := pending[nodeUUID]; !stillPending {
			delete(nmm.confirmState, nodeUUID)
		}
	}

	var due []republishTarget
	for nodeUUID, target := range pending {
		state, tracked := nmm.confirmState[nodeUUID]
		if !tracked {
			nmm.confirmState[nodeUUID] = &configConfirmState{
				unconfirmedSince: now,
				nextNudge:        now.Add(configConfirmTimeout),
				backoff:          configConfirmTimeout,
			}
			continue
		}
		if now.Before(state.nextNudge) {
			continue
		}
		state.backoff = min(state.backoff*2, configConfirmBackoffMax)
		state.nextNudge = now.Add(state.backoff)
		logrus.Infof("config confirmation sweep: node %s has been unconfirmed for %s, asking it to restate its config status",
			nodeUUID, now.Sub(state.unconfirmedSince).Round(time.Second))
		due = append(due, target)
	}
	return due
}
