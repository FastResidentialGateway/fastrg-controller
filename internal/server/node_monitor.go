package server

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/sirupsen/logrus"

	"fastrg-controller/internal/utils"
	fastrgnodepb "fastrg-controller/proto/fastrgnodepb"

	"github.com/pkg/errors"
	"github.com/prometheus/client_golang/prometheus"
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
	metrics      *NodeMetrics
}

// NodeMetrics holds Prometheus metrics for a node
type NodeMetrics struct {
	rxPackets                       *prometheus.GaugeVec
	txPackets                       *prometheus.GaugeVec
	rxBytes                         *prometheus.GaugeVec
	txBytes                         *prometheus.GaugeVec
	rxErrors                        *prometheus.GaugeVec
	txErrors                        *prometheus.GaugeVec
	rxDropped                       *prometheus.GaugeVec
	perUserRxPackets                *prometheus.GaugeVec
	perUserRxBytes                  *prometheus.GaugeVec
	perUserTxPackets                *prometheus.GaugeVec
	perUserTxBytes                  *prometheus.GaugeVec
	perUserDropPackets              *prometheus.GaugeVec
	perUserDropBytes                *prometheus.GaugeVec
	unknownUserRxPackets            *prometheus.GaugeVec
	unknownUserRxBytes              *prometheus.GaugeVec
	unknownUserTxPackets            *prometheus.GaugeVec
	unknownUserTxBytes              *prometheus.GaugeVec
	unknownUserDropPackets          *prometheus.GaugeVec
	unknownUserDropBytes            *prometheus.GaugeVec
	totalPPPoEDataSessions          *prometheus.GaugeVec
	totalPPPoEIPCPSessions          *prometheus.GaugeVec
	totalPPPoELCPSessions           *prometheus.GaugeVec
	totalPPPoEAuthSessions          *prometheus.GaugeVec
	totalPPPoEInitSessions          *prometheus.GaugeVec
	totalPPPoETerminatedSessions    *prometheus.GaugeVec
	totalPPPoENotConfiguredSessions *prometheus.GaugeVec
	totalPPPoEErrorSessions         *prometheus.GaugeVec
	perUserDhcpCurLeaseCount        *prometheus.GaugeVec
	perUserDhcpMaxLeaseCount        *prometheus.GaugeVec
	totalRunningDhcpServer          *prometheus.GaugeVec
	totalStoppedDhcpServer          *prometheus.GaugeVec
	totalNotConfiguredDhcpServer    *prometheus.GaugeVec
	perPPPoESessionRxPackets        *prometheus.GaugeVec
	perPPPoESessionRxBytes          *prometheus.GaugeVec
	perPPPoESessionTxPackets        *prometheus.GaugeVec
	perPPPoESessionTxBytes          *prometheus.GaugeVec
}

// NodeMonitorManager manages all node monitors
type NodeMonitorManager struct {
	mu       sync.RWMutex
	monitors map[string]*NodeMonitor
	metrics  *NodeMetrics
}

// NewNodeMonitorManager creates a new NodeMonitorManager
func NewNodeMonitorManager() *NodeMonitorManager {
	metrics := &NodeMetrics{
		rxPackets: prometheus.NewGaugeVec(
			prometheus.GaugeOpts{
				Name: "fastrg_node_rx_packets_total",
				Help: "Total number of received packets",
			},
			[]string{"node_uuid", "nic_index"},
		),
		txPackets: prometheus.NewGaugeVec(
			prometheus.GaugeOpts{
				Name: "fastrg_node_tx_packets_total",
				Help: "Total number of transmitted packets",
			},
			[]string{"node_uuid", "nic_index"},
		),
		rxBytes: prometheus.NewGaugeVec(
			prometheus.GaugeOpts{
				Name: "fastrg_node_rx_bytes_total",
				Help: "Total number of received bytes",
			},
			[]string{"node_uuid", "nic_index"},
		),
		txBytes: prometheus.NewGaugeVec(
			prometheus.GaugeOpts{
				Name: "fastrg_node_tx_bytes_total",
				Help: "Total number of transmitted bytes",
			},
			[]string{"node_uuid", "nic_index"},
		),
		rxErrors: prometheus.NewGaugeVec(
			prometheus.GaugeOpts{
				Name: "fastrg_node_rx_errors_total",
				Help: "Total number of receive errors",
			},
			[]string{"node_uuid", "nic_index"},
		),
		txErrors: prometheus.NewGaugeVec(
			prometheus.GaugeOpts{
				Name: "fastrg_node_tx_errors_total",
				Help: "Total number of transmit errors",
			},
			[]string{"node_uuid", "nic_index"},
		),
		rxDropped: prometheus.NewGaugeVec(
			prometheus.GaugeOpts{
				Name: "fastrg_node_rx_dropped_total",
				Help: "Total number of dropped received packets",
			},
			[]string{"node_uuid", "nic_index"},
		),
		perUserRxPackets: prometheus.NewGaugeVec(
			prometheus.GaugeOpts{
				Name: "fastrg_node_per_user_rx_packets_total",
				Help: "Total number of received packets per user",
			},
			[]string{"node_uuid", "nic_index", "user_id"},
		),
		perUserRxBytes: prometheus.NewGaugeVec(
			prometheus.GaugeOpts{
				Name: "fastrg_node_per_user_rx_bytes_total",
				Help: "Total number of received bytes per user",
			},
			[]string{"node_uuid", "nic_index", "user_id"},
		),
		perUserTxPackets: prometheus.NewGaugeVec(
			prometheus.GaugeOpts{
				Name: "fastrg_node_per_user_tx_packets_total",
				Help: "Total number of transmitted packets per user",
			},
			[]string{"node_uuid", "nic_index", "user_id"},
		),
		perUserTxBytes: prometheus.NewGaugeVec(
			prometheus.GaugeOpts{
				Name: "fastrg_node_per_user_tx_bytes_total",
				Help: "Total number of transmitted bytes per user",
			},
			[]string{"node_uuid", "nic_index", "user_id"},
		),
		perUserDropPackets: prometheus.NewGaugeVec(
			prometheus.GaugeOpts{
				Name: "fastrg_node_per_user_dropped_packets_total",
				Help: "Total number of dropped packets per user",
			},
			[]string{"node_uuid", "nic_index", "user_id"},
		),
		perUserDropBytes: prometheus.NewGaugeVec(
			prometheus.GaugeOpts{
				Name: "fastrg_node_per_user_dropped_bytes_total",
				Help: "Total number of dropped bytes per user",
			},
			[]string{"node_uuid", "nic_index", "user_id"},
		),
		unknownUserRxPackets: prometheus.NewGaugeVec(
			prometheus.GaugeOpts{
				Name: "fastrg_node_unknown_user_rx_packets_total",
				Help: "Total number of received packets for unknown user",
			},
			[]string{"node_uuid", "nic_index"},
		),
		unknownUserRxBytes: prometheus.NewGaugeVec(
			prometheus.GaugeOpts{
				Name: "fastrg_node_unknown_user_rx_bytes_total",
				Help: "Total number of received bytes for unknown user",
			},
			[]string{"node_uuid", "nic_index"},
		),
		unknownUserTxPackets: prometheus.NewGaugeVec(
			prometheus.GaugeOpts{
				Name: "fastrg_node_unknown_user_tx_packets_total",
				Help: "Total number of transmitted packets for unknown user",
			},
			[]string{"node_uuid", "nic_index"},
		),
		unknownUserTxBytes: prometheus.NewGaugeVec(
			prometheus.GaugeOpts{
				Name: "fastrg_node_unknown_user_tx_bytes_total",
				Help: "Total number of transmitted bytes for unknown user",
			},
			[]string{"node_uuid", "nic_index"},
		),
		unknownUserDropPackets: prometheus.NewGaugeVec(
			prometheus.GaugeOpts{
				Name: "fastrg_node_unknown_user_dropped_packets_total",
				Help: "Total number of dropped packets for unknown user",
			},
			[]string{"node_uuid", "nic_index"},
		),
		unknownUserDropBytes: prometheus.NewGaugeVec(
			prometheus.GaugeOpts{
				Name: "fastrg_node_unknown_user_dropped_bytes_total",
				Help: "Total number of dropped bytes for unknown user",
			},
			[]string{"node_uuid", "nic_index"},
		),
		totalPPPoEDataSessions: prometheus.NewGaugeVec(
			prometheus.GaugeOpts{
				Name: "fastrg_node_total_pppoe_data_sessions",
				Help: "Total number of PPPoE data sessions",
			},
			[]string{"node_uuid"},
		),
		totalPPPoEIPCPSessions: prometheus.NewGaugeVec(
			prometheus.GaugeOpts{
				Name: "fastrg_node_total_pppoe_ipcp_sessions",
				Help: "Total number of PPPoE IPCP sessions",
			},
			[]string{"node_uuid"},
		),
		totalPPPoEAuthSessions: prometheus.NewGaugeVec(
			prometheus.GaugeOpts{
				Name: "fastrg_node_total_pppoe_auth_sessions",
				Help: "Total number of PPPoE auth sessions",
			},
			[]string{"node_uuid"},
		),
		totalPPPoELCPSessions: prometheus.NewGaugeVec(
			prometheus.GaugeOpts{
				Name: "fastrg_node_total_pppoe_lcp_sessions",
				Help: "Total number of PPPoE LCP sessions",
			},
			[]string{"node_uuid"},
		),
		totalPPPoEInitSessions: prometheus.NewGaugeVec(
			prometheus.GaugeOpts{
				Name: "fastrg_node_total_pppoe_init_sessions",
				Help: "Total number of PPPoE init sessions",
			},
			[]string{"node_uuid"},
		),
		totalPPPoETerminatedSessions: prometheus.NewGaugeVec(
			prometheus.GaugeOpts{
				Name: "fastrg_node_total_pppoe_terminated_sessions",
				Help: "Total number of PPPoE terminated sessions",
			},
			[]string{"node_uuid"},
		),
		totalPPPoENotConfiguredSessions: prometheus.NewGaugeVec(
			prometheus.GaugeOpts{
				Name: "fastrg_node_total_pppoe_not_configured_sessions",
				Help: "Total number of PPPoE not configured sessions",
			},
			[]string{"node_uuid"},
		),
		totalPPPoEErrorSessions: prometheus.NewGaugeVec(
			prometheus.GaugeOpts{
				Name: "fastrg_node_total_pppoe_error_sessions",
				Help: "Total number of PPPoE sessions in unknown error state",
			},
			[]string{"node_uuid"},
		),
		perUserDhcpCurLeaseCount: prometheus.NewGaugeVec(
			prometheus.GaugeOpts{
				Name: "fastrg_node_per_user_dhcp_cur_lease_count",
				Help: "Current number of DHCP leases per user",
			},
			[]string{"node_uuid", "user_id"},
		),
		perUserDhcpMaxLeaseCount: prometheus.NewGaugeVec(
			prometheus.GaugeOpts{
				Name: "fastrg_node_per_user_dhcp_max_lease_count",
				Help: "Maximum Capacity number of DHCP leases per user",
			},
			[]string{"node_uuid", "user_id"},
		),
		totalRunningDhcpServer: prometheus.NewGaugeVec(
			prometheus.GaugeOpts{
				Name: "fastrg_node_total_running_dhcp_server",
				Help: "Total number of running DHCP servers",
			},
			[]string{"node_uuid"},
		),
		totalStoppedDhcpServer: prometheus.NewGaugeVec(
			prometheus.GaugeOpts{
				Name: "fastrg_node_total_stopped_dhcp_server",
				Help: "Total number of stopped DHCP servers",
			},
			[]string{"node_uuid"},
		),
		totalNotConfiguredDhcpServer: prometheus.NewGaugeVec(
			prometheus.GaugeOpts{
				Name: "fastrg_node_total_not_configured_dhcp_server",
				Help: "Total number of not configured DHCP servers",
			},
			[]string{"node_uuid"},
		),
		perPPPoESessionRxPackets: prometheus.NewGaugeVec(
			prometheus.GaugeOpts{
				Name: "fastrg_node_per_pppoe_session_rx_packets_total",
				Help: "Total number of received packets per PPPoE session",
			},
			[]string{"node_uuid", "user_id"},
		),
		perPPPoESessionRxBytes: prometheus.NewGaugeVec(
			prometheus.GaugeOpts{
				Name: "fastrg_node_per_pppoe_session_rx_bytes_total",
				Help: "Total number of received bytes per PPPoE session",
			},
			[]string{"node_uuid", "user_id"},
		),
		perPPPoESessionTxPackets: prometheus.NewGaugeVec(
			prometheus.GaugeOpts{
				Name: "fastrg_node_per_pppoe_session_tx_packets_total",
				Help: "Total number of transmitted packets per PPPoE session",
			},
			[]string{"node_uuid", "user_id"},
		),
		perPPPoESessionTxBytes: prometheus.NewGaugeVec(
			prometheus.GaugeOpts{
				Name: "fastrg_node_per_pppoe_session_tx_bytes_total",
				Help: "Total number of transmitted bytes per PPPoE session",
			},
			[]string{"node_uuid", "user_id"},
		),
	}

	// Register metrics with Prometheus
	prometheus.MustRegister(metrics.rxPackets)
	prometheus.MustRegister(metrics.txPackets)
	prometheus.MustRegister(metrics.rxBytes)
	prometheus.MustRegister(metrics.txBytes)
	prometheus.MustRegister(metrics.rxErrors)
	prometheus.MustRegister(metrics.txErrors)
	prometheus.MustRegister(metrics.rxDropped)
	prometheus.MustRegister(metrics.perUserRxPackets)
	prometheus.MustRegister(metrics.perUserRxBytes)
	prometheus.MustRegister(metrics.perUserTxPackets)
	prometheus.MustRegister(metrics.perUserTxBytes)
	prometheus.MustRegister(metrics.perUserDropPackets)
	prometheus.MustRegister(metrics.perUserDropBytes)
	prometheus.MustRegister(metrics.unknownUserRxPackets)
	prometheus.MustRegister(metrics.unknownUserRxBytes)
	prometheus.MustRegister(metrics.unknownUserTxPackets)
	prometheus.MustRegister(metrics.unknownUserTxBytes)
	prometheus.MustRegister(metrics.unknownUserDropPackets)
	prometheus.MustRegister(metrics.unknownUserDropBytes)
	prometheus.MustRegister(metrics.totalPPPoEDataSessions)
	prometheus.MustRegister(metrics.totalPPPoEIPCPSessions)
	prometheus.MustRegister(metrics.totalPPPoEAuthSessions)
	prometheus.MustRegister(metrics.totalPPPoELCPSessions)
	prometheus.MustRegister(metrics.totalPPPoEInitSessions)
	prometheus.MustRegister(metrics.totalPPPoETerminatedSessions)
	prometheus.MustRegister(metrics.totalPPPoENotConfiguredSessions)
	prometheus.MustRegister(metrics.totalPPPoEErrorSessions)
	prometheus.MustRegister(metrics.perUserDhcpCurLeaseCount)
	prometheus.MustRegister(metrics.perUserDhcpMaxLeaseCount)
	prometheus.MustRegister(metrics.totalRunningDhcpServer)
	prometheus.MustRegister(metrics.totalStoppedDhcpServer)
	prometheus.MustRegister(metrics.totalNotConfiguredDhcpServer)
	prometheus.MustRegister(metrics.perPPPoESessionRxPackets)
	prometheus.MustRegister(metrics.perPPPoESessionRxBytes)
	prometheus.MustRegister(metrics.perPPPoESessionTxPackets)
	prometheus.MustRegister(metrics.perPPPoESessionTxBytes)

	return &NodeMonitorManager{
		monitors: make(map[string]*NodeMonitor),
		metrics:  metrics,
	}
}

// StartMonitoring starts monitoring a node
func (nmm *NodeMonitorManager) StartMonitoring(nodeUUID, nodeIP string) error {
	nmm.mu.Lock()
	defer nmm.mu.Unlock()

	// Check if already monitoring this node
	if _, exists := nmm.monitors[nodeUUID]; exists {
		logrus.Infof("Already monitoring node %s, restarting...", nodeUUID)
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
		metrics:      nmm.metrics,
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

	// Delete metrics for this node
	nmm.deleteNodeMetrics(nodeUUID)

	// Remove from map
	delete(nmm.monitors, nodeUUID)

	logrus.Infof("Stopped monitoring node %s", nodeUUID)
}

// deleteNodeMetrics removes all metrics for a node
func (nmm *NodeMonitorManager) deleteNodeMetrics(nodeUUID string) {
	// We need to delete all metrics with the node_uuid label
	// We assume there are 2 NICs (index 0 and 1) on each node
	for i := 0; i < 2; i++ {
		nicIndex := fmt.Sprintf("%d", i)
		nmm.metrics.rxPackets.DeleteLabelValues(nodeUUID, nicIndex)
		nmm.metrics.txPackets.DeleteLabelValues(nodeUUID, nicIndex)
		nmm.metrics.rxBytes.DeleteLabelValues(nodeUUID, nicIndex)
		nmm.metrics.txBytes.DeleteLabelValues(nodeUUID, nicIndex)
		nmm.metrics.rxErrors.DeleteLabelValues(nodeUUID, nicIndex)
		nmm.metrics.txErrors.DeleteLabelValues(nodeUUID, nicIndex)
		nmm.metrics.rxDropped.DeleteLabelValues(nodeUUID, nicIndex)
	}
}

// monitorLoop is the main monitoring loop for a node
func (nm *NodeMonitor) monitorLoop() {
	ticker := time.NewTicker(1 * time.Second)
	defer ticker.Stop()

	logrus.Infof("Started monitoring loop for node %s", nm.nodeUUID)

	for {
		select {
		case <-nm.ctx.Done():
			logrus.Infof("Stopping monitoring loop for node %s", nm.nodeUUID)
			return
		case <-ticker.C:
			nm.collectMetrics()
		}
	}
}

// collectMetrics collects metrics from the node
func (nm *NodeMonitor) collectMetrics() {
	ctx, cancel := context.WithTimeout(nm.ctx, 5*time.Second)
	defer cancel()

	if err := nm.getNicCounter(ctx); err != nil {
		logrus.WithError(err).Errorf("Failed to get NIC counters from node %s", nm.nodeUUID)
		return
	}

	if err := nm.getPPPoESessionStats(ctx); err != nil {
		logrus.WithError(err).Errorf("Failed to get PPPoE session stats from node %s", nm.nodeUUID)
		return
	}

	if err := nm.getDhcpLeaseStats(ctx); err != nil {
		logrus.WithError(err).Errorf("Failed to get DHCP lease stats from node %s", nm.nodeUUID)
		return
	}
}

func (nm *NodeMonitor) getNicCounter(ctx context.Context) error {
	sysInfo, err := nm.fastrgClient.GetFastrgSystemStats(ctx, &emptypb.Empty{})
	if err != nil {
		return err
	}

	for i, stat := range sysInfo.Stats {
		nicIndex := fmt.Sprintf("%d", i)
		nm.metrics.rxPackets.WithLabelValues(nm.nodeUUID, nicIndex).Set(float64(stat.RxPackets))
		nm.metrics.txPackets.WithLabelValues(nm.nodeUUID, nicIndex).Set(float64(stat.TxPackets))
		nm.metrics.rxBytes.WithLabelValues(nm.nodeUUID, nicIndex).Set(float64(stat.RxBytes))
		nm.metrics.txBytes.WithLabelValues(nm.nodeUUID, nicIndex).Set(float64(stat.TxBytes))
		nm.metrics.rxErrors.WithLabelValues(nm.nodeUUID, nicIndex).Set(float64(stat.RxErrors))
		nm.metrics.txErrors.WithLabelValues(nm.nodeUUID, nicIndex).Set(float64(stat.TxErrors))
		nm.metrics.rxDropped.WithLabelValues(nm.nodeUUID, nicIndex).Set(float64(stat.RxDropped))
		for i := 0; i < len(stat.PerUserStats)-1; i++ {
			userStat := stat.PerUserStats[i]
			userID := fmt.Sprintf("%d", userStat.UserId)
			nm.metrics.perUserRxPackets.WithLabelValues(nm.nodeUUID, nicIndex, userID).Set(float64(userStat.RxPackets))
			nm.metrics.perUserRxBytes.WithLabelValues(nm.nodeUUID, nicIndex, userID).Set(float64(userStat.RxBytes))
			nm.metrics.perUserTxPackets.WithLabelValues(nm.nodeUUID, nicIndex, userID).Set(float64(userStat.TxPackets))
			nm.metrics.perUserTxBytes.WithLabelValues(nm.nodeUUID, nicIndex, userID).Set(float64(userStat.TxBytes))
			nm.metrics.perUserDropPackets.WithLabelValues(nm.nodeUUID, nicIndex, userID).Set(float64(userStat.DroppedPackets))
			nm.metrics.perUserDropBytes.WithLabelValues(nm.nodeUUID, nicIndex, userID).Set(float64(userStat.DroppedBytes))
		}
		unknownStat := stat.PerUserStats[len(stat.PerUserStats)-1]
		nm.metrics.unknownUserRxPackets.WithLabelValues(nm.nodeUUID, nicIndex).Set(float64(unknownStat.RxPackets))
		nm.metrics.unknownUserRxBytes.WithLabelValues(nm.nodeUUID, nicIndex).Set(float64(unknownStat.RxBytes))
		nm.metrics.unknownUserTxPackets.WithLabelValues(nm.nodeUUID, nicIndex).Set(float64(unknownStat.TxPackets))
		nm.metrics.unknownUserTxBytes.WithLabelValues(nm.nodeUUID, nicIndex).Set(float64(unknownStat.TxBytes))
		nm.metrics.unknownUserDropPackets.WithLabelValues(nm.nodeUUID, nicIndex).Set(float64(unknownStat.DroppedPackets))
		nm.metrics.unknownUserDropBytes.WithLabelValues(nm.nodeUUID, nicIndex).Set(float64(unknownStat.DroppedBytes))
	}

	return nil
}

func (nm *NodeMonitor) getPPPoESessionStats(ctx context.Context) error {
	var (
		totalPPPoEDataSessions          uint64
		totalPPPoEIPCPSessions          uint64
		totalPPPoEAuthSessions          uint64
		totalPPPoELCPSessions           uint64
		totalPPPoEInitSessions          uint64
		totalPPPoETerminatedSessions    uint64
		totalPPPoENotConfiguredSessions uint64
		totalPPPoEErrorSessions         uint64
	)

	hsiInfo, err := nm.fastrgClient.GetFastrgHsiInfo(ctx, &emptypb.Empty{})
	if err != nil {
		return err
	}
	for _, hsi := range hsiInfo.HsiInfos {
		switch hsi.Status {
		case "Data phase":
			totalPPPoEDataSessions++
		case "IPCP phase":
			totalPPPoEIPCPSessions++
		case "Auth phase":
			totalPPPoEAuthSessions++
		case "LCP phase":
			totalPPPoELCPSessions++
		case "PPPoE Init":
			totalPPPoEInitSessions++
		case "End phase":
			totalPPPoETerminatedSessions++
		case "Not configured":
			totalPPPoENotConfiguredSessions++
		default:
			totalPPPoEErrorSessions++
		}
		nm.metrics.perPPPoESessionRxPackets.WithLabelValues(nm.nodeUUID, fmt.Sprint(hsi.UserId)).Set(float64(hsi.PppoesRxPackets))
		nm.metrics.perPPPoESessionRxBytes.WithLabelValues(nm.nodeUUID, fmt.Sprint(hsi.UserId)).Set(float64(hsi.PppoesRxBytes))
		nm.metrics.perPPPoESessionTxPackets.WithLabelValues(nm.nodeUUID, fmt.Sprint(hsi.UserId)).Set(float64(hsi.PppoesTxPackets))
		nm.metrics.perPPPoESessionTxBytes.WithLabelValues(nm.nodeUUID, fmt.Sprint(hsi.UserId)).Set(float64(hsi.PppoesTxBytes))
	}

	nm.metrics.totalPPPoEDataSessions.WithLabelValues(nm.nodeUUID).Set(float64(totalPPPoEDataSessions))
	nm.metrics.totalPPPoEIPCPSessions.WithLabelValues(nm.nodeUUID).Set(float64(totalPPPoEIPCPSessions))
	nm.metrics.totalPPPoEAuthSessions.WithLabelValues(nm.nodeUUID).Set(float64(totalPPPoEAuthSessions))
	nm.metrics.totalPPPoELCPSessions.WithLabelValues(nm.nodeUUID).Set(float64(totalPPPoELCPSessions))
	nm.metrics.totalPPPoEInitSessions.WithLabelValues(nm.nodeUUID).Set(float64(totalPPPoEInitSessions))
	nm.metrics.totalPPPoETerminatedSessions.WithLabelValues(nm.nodeUUID).Set(float64(totalPPPoETerminatedSessions))
	nm.metrics.totalPPPoENotConfiguredSessions.WithLabelValues(nm.nodeUUID).Set(float64(totalPPPoENotConfiguredSessions))
	nm.metrics.totalPPPoEErrorSessions.WithLabelValues(nm.nodeUUID).Set(float64(totalPPPoEErrorSessions))

	return nil
}

func (nm *NodeMonitor) getDhcpLeaseStats(ctx context.Context) error {
	var (
		totalRunningDhcpServer       uint64
		totalStoppedDhcpServer       uint64
		totalNotConfiguredDhcpServer uint64
	)

	dhcpInfo, err := nm.fastrgClient.GetFastrgDhcpInfo(ctx, &emptypb.Empty{})
	if err != nil {
		return err
	}

	for _, dhcpInfo := range dhcpInfo.DhcpInfos {
		if dhcpInfo.Status == "DHCP server is on" {
			curLeaseCount := len(dhcpInfo.InuseIps)
			nm.metrics.perUserDhcpCurLeaseCount.WithLabelValues(nm.nodeUUID, fmt.Sprint(dhcpInfo.UserId)).Set(float64(curLeaseCount))
			ipStart, ipEnd, err := utils.ParseIPRange(dhcpInfo.IpRange)
			if err != nil {
				logrus.WithError(err).Debugf("Failed to parse IP range %s from node %s", dhcpInfo.IpRange, nm.nodeUUID)
				continue
			}
			ipStartUint, err := utils.IPv4toInt(ipStart)
			if err != nil {
				logrus.WithError(err).Debugf("Failed to convert start IP %s to int from node %s", ipStart.String(), nm.nodeUUID)
				continue
			}
			ipEndUint, err := utils.IPv4toInt(ipEnd)
			if err != nil {
				logrus.WithError(err).Debugf("Failed to convert end IP %s to int from node %s", ipEnd.String(), nm.nodeUUID)
				continue
			}
			maxLeaseCount := ipEndUint - ipStartUint + 1
			nm.metrics.perUserDhcpMaxLeaseCount.WithLabelValues(nm.nodeUUID, fmt.Sprint(dhcpInfo.UserId)).Set(float64(maxLeaseCount))
			totalRunningDhcpServer++
		} else if dhcpInfo.Status == "DHCP server is off" && dhcpInfo.IpRange != "Not configured" {
			curLeaseCount := len(dhcpInfo.InuseIps)
			nm.metrics.perUserDhcpCurLeaseCount.WithLabelValues(nm.nodeUUID, fmt.Sprint(dhcpInfo.UserId)).Set(float64(curLeaseCount))
			ipStart, ipEnd, err := utils.ParseIPRange(dhcpInfo.IpRange)
			if err != nil {
				logrus.WithError(err).Debugf("Failed to parse IP range %s from node %s", dhcpInfo.IpRange, nm.nodeUUID)
				continue
			}
			ipStartUint, err := utils.IPv4toInt(ipStart)
			if err != nil {
				logrus.WithError(err).Debugf("Failed to convert start IP %s to int from node %s", ipStart.String(), nm.nodeUUID)
				continue
			}
			ipEndUint, err := utils.IPv4toInt(ipEnd)
			if err != nil {
				logrus.WithError(err).Debugf("Failed to convert end IP %s to int from node %s", ipEnd.String(), nm.nodeUUID)
				continue
			}
			maxLeaseCount := ipEndUint - ipStartUint + 1
			nm.metrics.perUserDhcpMaxLeaseCount.WithLabelValues(nm.nodeUUID, fmt.Sprint(dhcpInfo.UserId)).Set(float64(maxLeaseCount))
			totalStoppedDhcpServer++
		} else {
			totalNotConfiguredDhcpServer++
		}
	}

	nm.metrics.totalRunningDhcpServer.WithLabelValues(nm.nodeUUID).Set(float64(totalRunningDhcpServer))
	nm.metrics.totalStoppedDhcpServer.WithLabelValues(nm.nodeUUID).Set(float64(totalStoppedDhcpServer))
	nm.metrics.totalNotConfiguredDhcpServer.WithLabelValues(nm.nodeUUID).Set(float64(totalNotConfiguredDhcpServer))

	return nil
}
