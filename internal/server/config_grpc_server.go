package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"time"

	"fastrg-controller/internal/storage"
	"fastrg-controller/internal/validation"
	controllerpb "fastrg-controller/proto"

	"github.com/sirupsen/logrus"
	clientv3 "go.etcd.io/etcd/client/v3"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/emptypb"
)

// ConfigGrpcServer implements controllerpb.ConfigServiceServer.
// It reuses EtcdClient.CAS (slice 3) and internal/validation (slice 2) so
// the write paths and rules are identical to the REST API.
type ConfigGrpcServer struct {
	controllerpb.UnimplementedConfigServiceServer
	etcd      *storage.EtcdClient
	jwtSecret []byte
}

func NewConfigGrpcServer(etcd *storage.EtcdClient, jwtSecret []byte) *ConfigGrpcServer {
	return &ConfigGrpcServer{etcd: etcd, jwtSecret: jwtSecret}
}

// ── auth ──────────────────────────────────────────────────────────────────

// callerFromCtx extracts and validates the JWT from gRPC metadata, returning
// the username. Returns an Unauthenticated status error when missing or invalid.
func (s *ConfigGrpcServer) callerFromCtx(ctx context.Context) (string, error) {
	md, ok := metadata.FromIncomingContext(ctx)
	if !ok {
		return "", status.Error(codes.Unauthenticated, "missing metadata")
	}
	vals := md.Get("authorization")
	if len(vals) == 0 {
		return "", status.Error(codes.Unauthenticated, "authorization token required")
	}
	// Reuse the REST JWT validator (same secret, same claims).
	rs := &RestServer{jwtSecret: s.jwtSecret}
	user, err := rs.getUserFromToken(vals[0])
	if err != nil {
		return "", status.Error(codes.Unauthenticated, "invalid token")
	}

	blacklistKey := fmt.Sprintf("token_blacklist/%s", vals[0])
	blacklistCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	resp, err := s.etcd.Client().Get(blacklistCtx, blacklistKey)
	if err != nil {
		logrus.WithError(err).Error("Failed to check token blacklist")
		return "", status.Error(codes.Unavailable, "authentication service unavailable")
	}
	if len(resp.Kvs) > 0 {
		return "", status.Error(codes.Unauthenticated, "token has been revoked")
	}
	return user, nil
}

// ── validation → gRPC status mapping ─────────────────────────────────────

func validationToStatus(err error) error {
	if err == nil {
		return nil
	}
	var ve *validation.Error
	if errors.As(err, &ve) && ve.Conflict {
		return status.Error(codes.AlreadyExists, ve.Error())
	}
	return status.Error(codes.InvalidArgument, err.Error())
}

func casToStatus(err error) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, storage.ErrCASConflict) {
		return status.Error(codes.Aborted, "concurrent update conflict, please retry")
	}
	return status.Error(codes.Internal, err.Error())
}

// ── etcd helpers ──────────────────────────────────────────────────────────

func hsiKey(nodeID, userID string) string {
	return fmt.Sprintf("configs/%s/hsi/%s", nodeID, userID)
}

func dnsKey(nodeID, userID string) string {
	return fmt.Sprintf("configs/%s/dns/%s", nodeID, userID)
}

func subscriberCountKey(nodeID string) string {
	return fmt.Sprintf("user_counts/%s/", nodeID)
}

// fetchVlanOwners reads all HSI configs on a node to build the VLAN-owner list
// needed by validation.CheckVlanUnique.
func (s *ConfigGrpcServer) fetchVlanOwners(ctx context.Context, nodeID string) ([]validation.VlanOwner, error) {
	resp, err := s.etcd.Client().Get(ctx, fmt.Sprintf("configs/%s/hsi/", nodeID),
		clientv3.WithPrefix())
	if err != nil {
		return nil, err
	}
	owners := make([]validation.VlanOwner, 0, len(resp.Kvs))
	for _, kv := range resp.Kvs {
		var cwm hsiConfigWithMetadata
		if err := json.Unmarshal(kv.Value, &cwm); err != nil {
			continue
		}
		owners = append(owners, validation.VlanOwner{UserID: cwm.Config.UserID, VlanID: cwm.Config.VlanID})
	}
	return owners, nil
}

// fetchSubscriberCount reads the subscriber count for a node (-1 when absent).
func (s *ConfigGrpcServer) fetchSubscriberCount(ctx context.Context, nodeID string) int32 {
	resp, err := s.etcd.Client().Get(ctx, subscriberCountKey(nodeID))
	if err != nil || len(resp.Kvs) == 0 {
		return -1
	}
	var d subscriberCountData
	if err := json.Unmarshal(resp.Kvs[0].Value, &d); err != nil {
		return -1
	}
	n, err := strconv.ParseInt(d.SubscriberCount, 10, 32)
	if err != nil {
		return -1
	}
	return int32(n)
}

// ── shared inner types (mirror rest_server.go structs without HTTP coupling) ──

type hsiConfigInner struct {
	UserID             string        `json:"user_id"`
	VlanID             string        `json:"vlan_id"`
	AccountName        string        `json:"account_name"`
	Password           string        `json:"password"`
	DHCPAddrPool       string        `json:"dhcp_addr_pool"`
	DHCPSubnet         string        `json:"dhcp_subnet"`
	DHCPGateway        string        `json:"dhcp_gateway"`
	DNSProxyEnable     *bool         `json:"dns_proxy_enable,omitempty"`
	TCPConntrackEnable *bool         `json:"tcp_conntrack_enable,omitempty"`
	PortMappings       []portMapping `json:"port-mapping,omitempty"`
	DesireStatus       string        `json:"desire_status"`
}

type portMapping struct {
	Index string `json:"index"`
	DIP   string `json:"dip"`
	DPort string `json:"dport"`
	EPort string `json:"eport"`
}

type hsiMetaInner struct {
	Node            string `json:"node"`
	ResourceVersion string `json:"resourceVersion"`
	UpdatedBy       string `json:"updatedBy"`
	UpdatedAt       string `json:"updatedAt"`
}

type hsiConfigWithMetadata struct {
	Config   hsiConfigInner `json:"config"`
	Metadata hsiMetaInner   `json:"metadata"`
}

type subscriberCountData struct {
	SubscriberCount string `json:"subscriber_count"`
	Metadata        struct {
		Node            string `json:"node"`
		ResourceVersion string `json:"resourceVersion"`
		UpdatedAt       string `json:"updatedAt"`
		UpdatedBy       string `json:"updatedBy"`
	} `json:"metadata"`
}

type dnsRecordInner struct {
	Domain string `json:"domain"`
	IP     string `json:"ip"`
	TTL    uint32 `json:"ttl"`
}

// ── proto ↔ inner type converters ─────────────────────────────────────────

func protoToInner(p *controllerpb.HSIConfig) hsiConfigInner {
	inner := hsiConfigInner{
		UserID:       p.GetUserId(),
		VlanID:       p.GetVlanId(),
		AccountName:  p.GetAccountName(),
		Password:     p.GetPassword(),
		DHCPAddrPool: p.GetDhcpAddrPool(),
		DHCPSubnet:   p.GetDhcpSubnet(),
		DHCPGateway:  p.GetDhcpGateway(),
		DesireStatus: p.GetDesireStatus(),
	}
	t := p.GetDnsProxyEnable()
	inner.DNSProxyEnable = &t
	c := p.GetTcpConntrackEnable()
	inner.TCPConntrackEnable = &c
	for _, pm := range p.GetPortMappings() {
		inner.PortMappings = append(inner.PortMappings, portMapping{
			Index: pm.GetIndex(),
			DIP:   pm.GetDip(),
			DPort: pm.GetDport(),
			EPort: pm.GetEport(),
		})
	}
	return inner
}

func innerToProto(c hsiConfigInner, m hsiMetaInner) *controllerpb.HSIConfigResponse {
	cfg := &controllerpb.HSIConfig{
		UserId:             c.UserID,
		VlanId:             c.VlanID,
		AccountName:        c.AccountName,
		Password:           c.Password,
		DhcpAddrPool:       c.DHCPAddrPool,
		DhcpSubnet:         c.DHCPSubnet,
		DhcpGateway:        c.DHCPGateway,
		DnsProxyEnable:     derefBool(c.DNSProxyEnable),
		TcpConntrackEnable: derefBool(c.TCPConntrackEnable),
		DesireStatus:       c.DesireStatus,
	}
	for _, pm := range c.PortMappings {
		cfg.PortMappings = append(cfg.PortMappings, &controllerpb.PortMapping{
			Index: pm.Index,
			Dip:   pm.DIP,
			Dport: pm.DPort,
			Eport: pm.EPort,
		})
	}
	return &controllerpb.HSIConfigResponse{
		Config: cfg,
		Metadata: &controllerpb.HSIMetadata{
			Node:            m.Node,
			ResourceVersion: m.ResourceVersion,
			UpdatedBy:       m.UpdatedBy,
			UpdatedAt:       m.UpdatedAt,
		},
	}
}

func derefBool(b *bool) bool {
	if b == nil {
		return false
	}
	return *b
}

// ── HSI config CRUD ───────────────────────────────────────────────────────

func (s *ConfigGrpcServer) CreateHSIConfig(ctx context.Context, req *controllerpb.CreateHSIConfigRequest) (*controllerpb.HSIConfigResponse, error) {
	caller, err := s.callerFromCtx(ctx)
	if err != nil {
		return nil, err
	}
	if req.NodeId == "" {
		return nil, status.Error(codes.InvalidArgument, "node_id is required")
	}
	p := req.GetConfig()
	if p == nil {
		return nil, status.Error(codes.InvalidArgument, "config is required")
	}

	if err := validationToStatus(validation.ValidateHSIConfig(validation.HSIConfigInput{
		UserID:       p.GetUserId(),
		VlanID:       p.GetVlanId(),
		AccountName:  p.GetAccountName(),
		Password:     p.GetPassword(),
		DHCPAddrPool: p.GetDhcpAddrPool(),
		DHCPSubnet:   p.GetDhcpSubnet(),
		DHCPGateway:  p.GetDhcpGateway(),
	})); err != nil {
		return nil, err
	}

	if err := validationToStatus(validation.CheckSubscriberCount(
		p.GetUserId(), int(s.fetchSubscriberCount(ctx, req.NodeId)),
	)); err != nil {
		return nil, err
	}

	owners, err := s.fetchVlanOwners(ctx, req.NodeId)
	if err != nil {
		return nil, status.Error(codes.Internal, "failed to check VLAN availability")
	}
	if err := validationToStatus(validation.CheckVlanUnique(p.GetVlanId(), p.GetUserId(), owners)); err != nil {
		return nil, err
	}

	inner := protoToInner(p)
	inner.DesireStatus = desireStatusDisconnect // always start disconnected
	t := true
	inner.DNSProxyEnable = &t
	inner.TCPConntrackEnable = &t

	key := hsiKey(req.NodeId, inner.UserID)
	var rv string
	casErr := s.etcd.CAS(ctx, key, func(current []byte) (storage.CASResult, error) {
		if current != nil {
			return storage.CASResult{}, errHSIConfigExists
		}
		rv = nextResourceVersion(current)
		cwm := hsiConfigWithMetadata{
			Config: inner,
			Metadata: hsiMetaInner{
				Node:            req.NodeId,
				ResourceVersion: rv,
				UpdatedBy:       caller,
				UpdatedAt:       time.Now().UTC().Format(time.RFC3339),
			},
		}
		b, err := json.Marshal(cwm)
		if err != nil {
			return storage.CASResult{}, err
		}
		return storage.CASResult{Value: b}, nil
	})
	if casErr != nil {
		if errors.Is(casErr, errHSIConfigExists) {
			return nil, status.Error(codes.AlreadyExists, errHSIConfigExists.Error())
		}
		return nil, casToStatus(casErr)
	}
	logrus.Infof("grpc CreateHSIConfig node=%s user=%s by=%s", req.NodeId, inner.UserID, caller)
	return innerToProto(inner, hsiMetaInner{Node: req.NodeId, ResourceVersion: rv, UpdatedBy: caller, UpdatedAt: time.Now().UTC().Format(time.RFC3339)}), nil
}

func (s *ConfigGrpcServer) UpdateHSIConfig(ctx context.Context, req *controllerpb.UpdateHSIConfigRequest) (*controllerpb.HSIConfigResponse, error) {
	caller, err := s.callerFromCtx(ctx)
	if err != nil {
		return nil, err
	}
	if req.NodeId == "" || req.UserId == "" {
		return nil, status.Error(codes.InvalidArgument, "node_id and user_id are required")
	}
	p := req.GetConfig()
	if p == nil {
		return nil, status.Error(codes.InvalidArgument, "config is required")
	}

	if err := validationToStatus(validation.ValidateHSIConfig(validation.HSIConfigInput{
		UserID:       p.GetUserId(),
		VlanID:       p.GetVlanId(),
		AccountName:  p.GetAccountName(),
		Password:     p.GetPassword(),
		DHCPAddrPool: p.GetDhcpAddrPool(),
		DHCPSubnet:   p.GetDhcpSubnet(),
		DHCPGateway:  p.GetDhcpGateway(),
	})); err != nil {
		return nil, err
	}
	if err := validationToStatus(validation.ValidateUserIDMatch(req.UserId, p.GetUserId())); err != nil {
		return nil, err
	}
	if err := validationToStatus(validation.CheckSubscriberCount(
		p.GetUserId(), int(s.fetchSubscriberCount(ctx, req.NodeId)),
	)); err != nil {
		return nil, err
	}
	owners, err := s.fetchVlanOwners(ctx, req.NodeId)
	if err != nil {
		return nil, status.Error(codes.Internal, "failed to check VLAN availability")
	}
	if err := validationToStatus(validation.CheckVlanUnique(p.GetVlanId(), req.UserId, owners)); err != nil {
		return nil, err
	}

	inner := protoToInner(p)
	key := hsiKey(req.NodeId, req.UserId)
	var rv string
	var meta hsiMetaInner
	casErr := s.etcd.CAS(ctx, key, func(current []byte) (storage.CASResult, error) {
		desire := desireStatusDisconnect
		if current != nil {
			var existing hsiConfigWithMetadata
			if err := json.Unmarshal(current, &existing); err == nil && existing.Config.DesireStatus != "" {
				desire = existing.Config.DesireStatus
			}
		}
		inner.DesireStatus = desire
		if inner.DNSProxyEnable == nil {
			t := true
			inner.DNSProxyEnable = &t
		}
		if inner.TCPConntrackEnable == nil {
			t := true
			inner.TCPConntrackEnable = &t
		}
		rv = nextResourceVersion(current)
		meta = hsiMetaInner{
			Node:            req.NodeId,
			ResourceVersion: rv,
			UpdatedBy:       caller,
			UpdatedAt:       time.Now().UTC().Format(time.RFC3339),
		}
		cwm := hsiConfigWithMetadata{Config: inner, Metadata: meta}
		b, err := json.Marshal(cwm)
		if err != nil {
			return storage.CASResult{}, err
		}
		return storage.CASResult{Value: b}, nil
	})
	if casErr != nil {
		return nil, casToStatus(casErr)
	}
	logrus.Infof("grpc UpdateHSIConfig node=%s user=%s by=%s", req.NodeId, req.UserId, caller)
	return innerToProto(inner, meta), nil
}

func (s *ConfigGrpcServer) DeleteHSIConfig(ctx context.Context, req *controllerpb.DeleteHSIConfigRequest) (*emptypb.Empty, error) {
	if _, err := s.callerFromCtx(ctx); err != nil {
		return nil, err
	}
	if req.NodeId == "" || req.UserId == "" {
		return nil, status.Error(codes.InvalidArgument, "node_id and user_id are required")
	}
	casErr := s.etcd.CAS(ctx, hsiKey(req.NodeId, req.UserId), func(current []byte) (storage.CASResult, error) {
		if current == nil {
			return storage.CASResult{}, errHSIConfigNotFound
		}
		return storage.CASResult{Delete: true}, nil
	})
	if casErr != nil {
		if errors.Is(casErr, errHSIConfigNotFound) {
			return nil, status.Error(codes.NotFound, errHSIConfigNotFound.Error())
		}
		return nil, casToStatus(casErr)
	}
	logrus.Infof("grpc DeleteHSIConfig node=%s user=%s", req.NodeId, req.UserId)
	return &emptypb.Empty{}, nil
}

func (s *ConfigGrpcServer) GetHSIConfig(ctx context.Context, req *controllerpb.GetHSIConfigRequest) (*controllerpb.HSIConfigResponse, error) {
	if _, err := s.callerFromCtx(ctx); err != nil {
		return nil, err
	}
	if req.NodeId == "" || req.UserId == "" {
		return nil, status.Error(codes.InvalidArgument, "node_id and user_id are required")
	}
	resp, err := s.etcd.Client().Get(ctx, hsiKey(req.NodeId, req.UserId))
	if err != nil {
		return nil, status.Error(codes.Internal, err.Error())
	}
	if len(resp.Kvs) == 0 {
		return nil, status.Error(codes.NotFound, "HSI config not found")
	}
	var cwm hsiConfigWithMetadata
	if err := json.Unmarshal(resp.Kvs[0].Value, &cwm); err != nil {
		return nil, status.Error(codes.Internal, "failed to parse HSI config")
	}
	return innerToProto(cwm.Config, cwm.Metadata), nil
}

func (s *ConfigGrpcServer) ListHSIConfigs(ctx context.Context, req *controllerpb.ListHSIConfigsRequest) (*controllerpb.ListHSIConfigsResponse, error) {
	if _, err := s.callerFromCtx(ctx); err != nil {
		return nil, err
	}
	if req.NodeId == "" {
		return nil, status.Error(codes.InvalidArgument, "node_id is required")
	}
	resp, err := s.etcd.Client().Get(ctx, fmt.Sprintf("configs/%s/hsi/", req.NodeId), clientv3.WithPrefix())
	if err != nil {
		return nil, status.Error(codes.Internal, err.Error())
	}
	out := &controllerpb.ListHSIConfigsResponse{}
	for _, kv := range resp.Kvs {
		var cwm hsiConfigWithMetadata
		if err := json.Unmarshal(kv.Value, &cwm); err != nil {
			continue
		}
		out.Configs = append(out.Configs, innerToProto(cwm.Config, cwm.Metadata))
	}
	return out, nil
}

// ── PPPoE desire-state ────────────────────────────────────────────────────

func (s *ConfigGrpcServer) DialPPPoE(ctx context.Context, req *controllerpb.PPPoEActionRequest) (*emptypb.Empty, error) {
	caller, err := s.callerFromCtx(ctx)
	if err != nil {
		return nil, err
	}
	if req.NodeId == "" || req.UserId == "" {
		return nil, status.Error(codes.InvalidArgument, "node_id and user_id are required")
	}
	// Reuse the RestServer helper through a thin adapter.
	rs := &RestServer{etcd: s.etcd}
	if err := rs.setDesireStatus(ctx, req.NodeId, req.UserId, caller, desireStatusConnect); err != nil {
		if errors.Is(err, errHSIConfigNotFound) {
			return nil, status.Error(codes.NotFound, err.Error())
		}
		return nil, casToStatus(err)
	}
	logrus.Infof("grpc DialPPPoE node=%s user=%s by=%s", req.NodeId, req.UserId, caller)
	return &emptypb.Empty{}, nil
}

func (s *ConfigGrpcServer) HangupPPPoE(ctx context.Context, req *controllerpb.PPPoEActionRequest) (*emptypb.Empty, error) {
	caller, err := s.callerFromCtx(ctx)
	if err != nil {
		return nil, err
	}
	if req.NodeId == "" || req.UserId == "" {
		return nil, status.Error(codes.InvalidArgument, "node_id and user_id are required")
	}
	rs := &RestServer{etcd: s.etcd}
	if err := rs.setDesireStatus(ctx, req.NodeId, req.UserId, caller, desireStatusDisconnect); err != nil {
		if errors.Is(err, errHSIConfigNotFound) {
			return nil, status.Error(codes.NotFound, err.Error())
		}
		return nil, casToStatus(err)
	}
	logrus.Infof("grpc HangupPPPoE node=%s user=%s by=%s", req.NodeId, req.UserId, caller)
	return &emptypb.Empty{}, nil
}

// ── Subscriber count ──────────────────────────────────────────────────────

func (s *ConfigGrpcServer) SetSubscriberCount(ctx context.Context, req *controllerpb.SetSubscriberCountRequest) (*emptypb.Empty, error) {
	caller, err := s.callerFromCtx(ctx)
	if err != nil {
		return nil, err
	}
	if req.NodeId == "" {
		return nil, status.Error(codes.InvalidArgument, "node_id is required")
	}
	if err := validationToStatus(validation.ValidateSubscriberCount(int(req.SubscriberCount))); err != nil {
		return nil, err
	}
	key := subscriberCountKey(req.NodeId)
	casErr := s.etcd.CAS(ctx, key, func(current []byte) (storage.CASResult, error) {
		d := subscriberCountData{}
		d.SubscriberCount = fmt.Sprintf("%d", req.SubscriberCount)
		d.Metadata.Node = req.NodeId
		d.Metadata.ResourceVersion = nextResourceVersion(current)
		d.Metadata.UpdatedBy = caller
		d.Metadata.UpdatedAt = time.Now().UTC().Format(time.RFC3339)
		b, err := json.Marshal(d)
		if err != nil {
			return storage.CASResult{}, err
		}
		return storage.CASResult{Value: b}, nil
	})
	if casErr != nil {
		return nil, casToStatus(casErr)
	}
	logrus.Infof("grpc SetSubscriberCount node=%s count=%d by=%s", req.NodeId, req.SubscriberCount, caller)
	return &emptypb.Empty{}, nil
}

func (s *ConfigGrpcServer) GetSubscriberCount(ctx context.Context, req *controllerpb.GetSubscriberCountRequest) (*controllerpb.GetSubscriberCountResponse, error) {
	if _, err := s.callerFromCtx(ctx); err != nil {
		return nil, err
	}
	if req.NodeId == "" {
		return nil, status.Error(codes.InvalidArgument, "node_id is required")
	}
	n := s.fetchSubscriberCount(ctx, req.NodeId)
	if n < 0 {
		return nil, status.Error(codes.NotFound, "subscriber count not set for this node")
	}
	return &controllerpb.GetSubscriberCountResponse{SubscriberCount: n}, nil
}

// ── DNS records ───────────────────────────────────────────────────────────

func (s *ConfigGrpcServer) AddOrUpdateDNSRecord(ctx context.Context, req *controllerpb.DNSRecordRequest) (*emptypb.Empty, error) {
	if _, err := s.callerFromCtx(ctx); err != nil {
		return nil, err
	}
	if req.NodeId == "" || req.UserId == "" {
		return nil, status.Error(codes.InvalidArgument, "node_id and user_id are required")
	}
	r := req.GetRecord()
	if r == nil {
		return nil, status.Error(codes.InvalidArgument, "record is required")
	}
	if err := validationToStatus(validation.ValidateDnsRecord(r.GetDomain(), r.GetIp(), r.GetTtl())); err != nil {
		return nil, err
	}
	newRec := dnsRecordInner{Domain: r.GetDomain(), IP: r.GetIp(), TTL: r.GetTtl()}
	casErr := s.etcd.CAS(ctx, dnsKey(req.NodeId, req.UserId), func(current []byte) (storage.CASResult, error) {
		var records []dnsRecordInner
		if current != nil {
			if err := json.Unmarshal(current, &records); err != nil {
				return storage.CASResult{}, err
			}
		}
		updated := false
		for i, rec := range records {
			if rec.Domain == newRec.Domain {
				records[i] = newRec
				updated = true
				break
			}
		}
		if !updated {
			if len(records) >= 64 {
				return storage.CASResult{}, errDNSRecordLimit
			}
			records = append(records, newRec)
		}
		b, err := json.Marshal(records)
		if err != nil {
			return storage.CASResult{}, err
		}
		return storage.CASResult{Value: b}, nil
	})
	if casErr != nil {
		if errors.Is(casErr, errDNSRecordLimit) {
			return nil, status.Error(codes.ResourceExhausted, errDNSRecordLimit.Error())
		}
		return nil, casToStatus(casErr)
	}
	return &emptypb.Empty{}, nil
}

func (s *ConfigGrpcServer) DeleteDNSRecord(ctx context.Context, req *controllerpb.DeleteDNSRecordRequest) (*emptypb.Empty, error) {
	if _, err := s.callerFromCtx(ctx); err != nil {
		return nil, err
	}
	if req.NodeId == "" || req.UserId == "" || req.Domain == "" {
		return nil, status.Error(codes.InvalidArgument, "node_id, user_id and domain are required")
	}
	casErr := s.etcd.CAS(ctx, dnsKey(req.NodeId, req.UserId), func(current []byte) (storage.CASResult, error) {
		if current == nil {
			return storage.CASResult{}, errDNSRecordNotFound
		}
		var records []dnsRecordInner
		if err := json.Unmarshal(current, &records); err != nil {
			return storage.CASResult{}, err
		}
		found := false
		kept := records[:0]
		for _, rec := range records {
			if rec.Domain == req.Domain {
				found = true
			} else {
				kept = append(kept, rec)
			}
		}
		if !found {
			return storage.CASResult{}, errDNSRecordNotFound
		}
		if len(kept) == 0 {
			return storage.CASResult{Delete: true}, nil
		}
		b, err := json.Marshal(kept)
		if err != nil {
			return storage.CASResult{}, err
		}
		return storage.CASResult{Value: b}, nil
	})
	if casErr != nil {
		if errors.Is(casErr, errDNSRecordNotFound) {
			return nil, status.Error(codes.NotFound, errDNSRecordNotFound.Error())
		}
		return nil, casToStatus(casErr)
	}
	return &emptypb.Empty{}, nil
}

func (s *ConfigGrpcServer) ListDNSRecords(ctx context.Context, req *controllerpb.ListDNSRecordsRequest) (*controllerpb.ListDNSRecordsResponse, error) {
	if _, err := s.callerFromCtx(ctx); err != nil {
		return nil, err
	}
	if req.NodeId == "" || req.UserId == "" {
		return nil, status.Error(codes.InvalidArgument, "node_id and user_id are required")
	}
	resp, err := s.etcd.Client().Get(ctx, dnsKey(req.NodeId, req.UserId))
	if err != nil {
		return nil, status.Error(codes.Internal, err.Error())
	}
	out := &controllerpb.ListDNSRecordsResponse{}
	if len(resp.Kvs) > 0 {
		var records []dnsRecordInner
		if err := json.Unmarshal(resp.Kvs[0].Value, &records); err != nil {
			return nil, status.Error(codes.Internal, "failed to parse DNS records")
		}
		for _, r := range records {
			out.Records = append(out.Records, &controllerpb.DNSRecord{
				Domain: r.Domain,
				Ip:     r.IP,
				Ttl:    r.TTL,
			})
		}
	}
	return out, nil
}
