package server

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"sync"
	"sync/atomic"
	"time"

	"github.com/sirupsen/logrus"

	"fastrg-controller/internal/db"
	"fastrg-controller/internal/storage"
	"fastrg-controller/internal/utils"
	fastrgnodepb "fastrg-controller/proto/fastrgnodepb"

	"github.com/pkg/errors"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/protobuf/types/known/emptypb"
)

// NodeMonitor manages the monitoring goroutine for a single node
type NodeMonitor struct {
	nodeUUID     string
	nodeIP       string
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
	database atomic.Pointer[db.DB] // Optional: for syncing PPPoE status to database
	// leader is true only on the elected leader replica. On-demand REST queries
	// run on every replica (so monitors/gRPC conns exist everywhere), but the
	// background poll-and-write loop runs only on the leader to avoid 3x node
	// load and duplicate pppoe_status writes.
	leader atomic.Bool
}

// SetLeader records whether this replica currently holds leadership.
func (nmm *NodeMonitorManager) SetLeader(v bool) { nmm.leader.Store(v) }

// IsLeader reports whether this replica currently holds leadership.
func (nmm *NodeMonitorManager) IsLeader() bool { return nmm.leader.Load() }

// NewNodeMonitorManager creates a new NodeMonitorManager.
// database parameter is optional (can be nil) for stateless recovery of PPPoE status.
func NewNodeMonitorManager(database *db.DB) *NodeMonitorManager {
	nmm := &NodeMonitorManager{
		monitors: make(map[string]*NodeMonitor),
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

// StartMonitoring starts monitoring a node. No-ops when monitoring is already
// active for this node at the same IP.
func (nmm *NodeMonitorManager) StartMonitoring(nodeUUID, nodeIP string) error {
	nmm.mu.Lock()
	defer nmm.mu.Unlock()

	// Check if already monitoring this node
	if existing, exists := nmm.monitors[nodeUUID]; exists {
		if existing.nodeIP == nodeIP {
			// Same node and IP — gRPC connection is still valid, no restart needed.
			return nil
		}
		logrus.Infof("Node %s IP changed %s -> %s, restarting monitoring", nodeUUID, existing.nodeIP, nodeIP)
		nmm.stopMonitoringLocked(nodeUUID)
	}

	// Create gRPC connection to the node
	nodeAddr := fmt.Sprintf("%s:50052", nodeIP)
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
		ctx:          ctx,
		cancel:       cancel,
		grpcConn:     conn,
		fastrgClient: fastrgClient,
		mgr:          nmm,
	}

	// Store monitor
	nmm.monitors[nodeUUID] = monitor

	// Start monitoring goroutine
	go monitor.monitorLoop()

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

// monitorLoop is the main monitoring loop for a node
func (nm *NodeMonitor) monitorLoop() {
	ticker := time.NewTicker(5 * time.Second)
	defer ticker.Stop()

	logrus.Infof("Started monitoring loop for node %s", nm.nodeUUID)

	for {
		select {
		case <-nm.ctx.Done():
			logrus.Infof("Stopping monitoring loop for node %s", nm.nodeUUID)
			return
		case <-ticker.C:
			// Only the leader polls nodes and writes pppoe_status; non-leader
			// replicas keep the monitor (and its gRPC conn) alive solely for
			// on-demand REST queries.
			if nm.mgr != nil && !nm.mgr.IsLeader() {
				continue
			}
			nm.syncNodeState()
		}
	}
}

// syncNodeState polls per-node state the controller still needs after Prometheus
// began scraping nodes directly. It only syncs each subscriber's PPPoE phase into
// the database for stateless recovery; NIC/system/DHCP metrics are now exposed by
// the node's own /metrics endpoint and are no longer collected here.
func (nm *NodeMonitor) syncNodeState() {
	ctx, cancel := context.WithTimeout(nm.ctx, 5*time.Second)
	defer cancel()

	if err := nm.syncPPPoEStatus(ctx); err != nil {
		logrus.WithError(err).Errorf("Failed to sync PPPoE status from node %s", nm.nodeUUID)
		return
	}
}

// FetchInitialNicModel dials the registered node once to retrieve NIC model info
// (nics[0]=WAN, nics[1]=LAN) and persists it into etcd. Called as a goroutine
// after StartMonitoring so RegisterNode is not blocked.
// If the gRPC call or etcd write ultimately fails after retries, "unknown" is stored.
func (nmm *NodeMonitorManager) FetchInitialNicModel(nodeUUID string, etcd *storage.EtcdClient) {
	if etcd == nil {
		return
	}

	// Node gRPC server (port 50052) may not be ready immediately after RegisterNode.
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
	resp, err := etcd.Client().Get(ctx, etcdKey)
	if err != nil {
		return err
	}
	if len(resp.Kvs) == 0 {
		return fmt.Errorf("node %s not found in etcd", nodeUUID)
	}

	var nodeData map[string]interface{}
	if err := json.Unmarshal(resp.Kvs[0].Value, &nodeData); err != nil {
		return err
	}
	nodeData["nic_model_wan"] = wanModel
	nodeData["nic_model_lan"] = lanModel

	updated, err := json.Marshal(nodeData)
	if err != nil {
		return err
	}
	_, err = etcd.Client().Put(ctx, etcdKey, string(updated))
	return err
}

// syncPPPoEStatus pulls each subscriber's PPPoE phase from the node and upserts it
// into the database for stateless recovery. The Prometheus session metrics formerly
// derived here are now exposed by the node's own /metrics endpoint.
func (nm *NodeMonitor) syncPPPoEStatus(ctx context.Context) error {
	hsiInfo, err := nm.fastrgClient.GetFastrgHsiInfo(ctx, &emptypb.Empty{})
	if err != nil {
		return err
	}
	if nm.mgr == nil {
		return nil
	}
	database := nm.mgr.Database()
	if database == nil {
		return nil
	}
	for _, hsi := range hsiInfo.HsiInfos {
		userID := fmt.Sprint(hsi.UserId)
		statusErr := database.UpsertPPPoEStatus(ctx, db.PPPoEStatusRow{
			NodeUUID:     nm.nodeUUID,
			UserID:       userID,
			Phase:        hsi.Status,
			HSIIPv4:      hsi.IpAddr,
			HSIIPv4GW:    hsi.Gateway,
			ErrorMessage: "",
			EventTime:    time.Now().UTC(),
		})
		if statusErr != nil {
			logrus.WithError(statusErr).Debugf("Failed to sync PPPoE status to database for node=%s user=%s", nm.nodeUUID, userID)
		}
	}
	return nil
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
