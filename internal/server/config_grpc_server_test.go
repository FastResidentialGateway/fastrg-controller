package server

import (
	"context"
	"net"
	"os"
	"testing"

	"fastrg-controller/internal/storage"
	"fastrg-controller/internal/validation"
	controllerpb "fastrg-controller/proto"

	clientv3 "go.etcd.io/etcd/client/v3"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
)

// ── unit tests for pure helpers ───────────────────────────────────────────

func TestProtoInnerRoundtrip(t *testing.T) {
	p := &controllerpb.HSIConfig{
		UserId:             "2",
		VlanId:             "100",
		AccountName:        "user@isp.net",
		Password:           "secret",
		DhcpAddrPool:       "192.168.1.2-192.168.1.200",
		DhcpSubnet:         "255.255.255.0",
		DhcpGateway:        "192.168.1.1",
		DnsProxyEnable:     boolPointer(true),
		TcpConntrackEnable: boolPointer(false),
		DesireStatus:       "connect",
		PortMappings:       []*controllerpb.PortMapping{{Index: "0", Dip: "10.0.0.1", Dport: "80", Eport: "8080"}},
	}
	inner := protoToInner(p)
	if inner.UserID != "2" || inner.VlanID != "100" || inner.DesireStatus != "connect" {
		t.Fatalf("protoToInner: basic fields wrong: %+v", inner)
	}
	if inner.DNSProxyEnable == nil || !*inner.DNSProxyEnable {
		t.Fatalf("DNSProxyEnable should be true")
	}
	if inner.TCPConntrackEnable == nil || *inner.TCPConntrackEnable {
		t.Fatalf("TCPConntrackEnable should be false")
	}
	if len(inner.PortMappings) != 1 || inner.PortMappings[0].EPort != "8080" {
		t.Fatalf("PortMappings wrong: %+v", inner.PortMappings)
	}

	meta := hsiMetaInner{Node: "n1", ResourceVersion: "3", UpdatedBy: "admin", UpdatedAt: "2026-01-01T00:00:00Z"}
	resp := innerToProto(inner, meta)
	if resp.Config.UserId != "2" || resp.Metadata.Node != "n1" {
		t.Fatalf("innerToProto: fields wrong: %+v", resp)
	}
	if len(resp.Config.PortMappings) != 1 {
		t.Fatalf("PortMappings not preserved")
	}
}

func TestValidationToStatusMapping(t *testing.T) {
	// nil → nil
	if validationToStatus(nil) != nil {
		t.Fatal("nil err should return nil")
	}
	// plain validation error → InvalidArgument
	plainErr := validation.ValidateHSIConfig(validation.HSIConfigInput{}) // UserID missing
	st, _ := status.FromError(validationToStatus(plainErr))
	if st.Code() != codes.InvalidArgument {
		t.Fatalf("want InvalidArgument, got %v", st.Code())
	}
	// conflict error → AlreadyExists (VLAN taken by another user)
	conflictErr := validation.CheckVlanUnique("100", "2",
		[]validation.VlanOwner{{UserID: "1", VlanID: "100"}})
	st, _ = status.FromError(validationToStatus(conflictErr))
	if st.Code() != codes.AlreadyExists {
		t.Fatalf("want AlreadyExists, got %v", st.Code())
	}
	// CAS conflict → Aborted
	st, _ = status.FromError(casToStatus(storage.ErrCASConflict))
	if st.Code() != codes.Aborted {
		t.Fatalf("want Aborted, got %v", st.Code())
	}
}

// ── integration tests against real etcd ──────────────────────────────────

func TestConfigServiceIntegration(t *testing.T) {
	etcdEndpoints := os.Getenv("TEST_ETCD_ENDPOINTS")
	if etcdEndpoints == "" {
		t.Skip("TEST_ETCD_ENDPOINTS not set; skipping ConfigService integration test")
	}

	t.Setenv("ETCD_ENDPOINTS", etcdEndpoints)
	etcdClient, err := storage.NewEtcdClient()
	if err != nil {
		t.Fatalf("etcd connect: %v", err)
	}
	defer etcdClient.Close()

	ctx := context.Background()
	etcdClient.Client().Delete(ctx, "configs/", clientv3.WithPrefix())
	etcdClient.Client().Delete(ctx, "user_counts/", clientv3.WithPrefix())

	secret := []byte(GetJWTSecret())
	rs := &RestServer{jwtSecret: secret}
	token, err := rs.generateToken("testuser")
	if err != nil {
		t.Fatalf("generateToken: %v", err)
	}

	lis, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	srv := grpc.NewServer()
	controllerpb.RegisterConfigServiceServer(srv, NewConfigGrpcServer(etcdClient, secret))
	go srv.Serve(lis)
	defer srv.Stop()

	cc, err := grpc.NewClient(lis.Addr().String(), grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		t.Fatalf("grpc dial: %v", err)
	}
	defer cc.Close()
	client := controllerpb.NewConfigServiceClient(cc)

	authCtx := metadata.AppendToOutgoingContext(ctx, "authorization", token)

	const nodeID = "node-test-1"
	const userID = "5"

	// SetSubscriberCount
	if _, err := client.SetSubscriberCount(authCtx, &controllerpb.SetSubscriberCountRequest{
		NodeId: nodeID, SubscriberCount: 100,
	}); err != nil {
		t.Fatalf("SetSubscriberCount: %v", err)
	}

	// GetSubscriberCount
	cntResp, err := client.GetSubscriberCount(authCtx, &controllerpb.GetSubscriberCountRequest{NodeId: nodeID})
	if err != nil || cntResp.SubscriberCount != 100 {
		t.Fatalf("GetSubscriberCount = (%v,%v), want 100", cntResp, err)
	}

	// CreateHSIConfig
	cfg := &controllerpb.HSIConfig{
		UserId: userID, VlanId: "100", AccountName: "u@isp.net", Password: "pw",
		DhcpAddrPool: "192.168.1.2-192.168.1.200", DhcpSubnet: "255.255.255.0", DhcpGateway: "192.168.1.1",
	}
	createResp, err := client.CreateHSIConfig(authCtx, &controllerpb.CreateHSIConfigRequest{NodeId: nodeID, Config: cfg})
	if err != nil {
		t.Fatalf("CreateHSIConfig: %v", err)
	}
	if createResp.Config.UserId != userID || createResp.Config.DesireStatus != desireStatusDisconnect {
		t.Fatalf("Create: bad response: %+v", createResp)
	}

	// Duplicate create → AlreadyExists
	_, err = client.CreateHSIConfig(authCtx, &controllerpb.CreateHSIConfigRequest{NodeId: nodeID, Config: cfg})
	if status.Code(err) != codes.AlreadyExists {
		t.Fatalf("duplicate create: want AlreadyExists, got %v", err)
	}

	// VLAN conflict (different user, same VLAN)
	_, err = client.CreateHSIConfig(authCtx, &controllerpb.CreateHSIConfigRequest{
		NodeId: nodeID,
		Config: &controllerpb.HSIConfig{
			UserId: "6", VlanId: "100", AccountName: "v@isp.net", Password: "pw2",
			DhcpAddrPool: cfg.DhcpAddrPool, DhcpSubnet: cfg.DhcpSubnet, DhcpGateway: cfg.DhcpGateway,
		},
	})
	if status.Code(err) != codes.AlreadyExists {
		t.Fatalf("VLAN conflict: want AlreadyExists, got %v", err)
	}

	// GetHSIConfig
	getResp, err := client.GetHSIConfig(authCtx, &controllerpb.GetHSIConfigRequest{NodeId: nodeID, UserId: userID})
	if err != nil || getResp.Config.VlanId != "100" {
		t.Fatalf("GetHSIConfig: %v %v", getResp, err)
	}

	// ListHSIConfigs
	listResp, err := client.ListHSIConfigs(authCtx, &controllerpb.ListHSIConfigsRequest{NodeId: nodeID})
	if err != nil || len(listResp.Configs) != 1 {
		t.Fatalf("ListHSIConfigs: got %d configs (%v)", len(listResp.GetConfigs()), err)
	}

	// UpdateHSIConfig — must preserve desire_status, ignore body's desire_status
	updateResp, err := client.UpdateHSIConfig(authCtx, &controllerpb.UpdateHSIConfigRequest{
		NodeId: nodeID, UserId: userID,
		Config: &controllerpb.HSIConfig{
			UserId: userID, VlanId: "100", AccountName: "updated@isp.net", Password: "pw",
			DhcpAddrPool: cfg.DhcpAddrPool, DhcpSubnet: cfg.DhcpSubnet, DhcpGateway: cfg.DhcpGateway,
			DesireStatus: "connect", // must be ignored; backend preserves stored value
		},
	})
	if err != nil {
		t.Fatalf("UpdateHSIConfig: %v", err)
	}
	if updateResp.Config.AccountName != "updated@isp.net" {
		t.Fatalf("Update: account not updated: %+v", updateResp)
	}
	if updateResp.Config.DesireStatus != desireStatusDisconnect {
		t.Fatalf("Update: desire_status leaked in (want disconnect): %q", updateResp.Config.DesireStatus)
	}

	// DialPPPoE → desire_status = connect
	if _, err := client.DialPPPoE(authCtx, &controllerpb.PPPoEActionRequest{NodeId: nodeID, UserId: userID}); err != nil {
		t.Fatalf("DialPPPoE: %v", err)
	}
	dialGet, _ := client.GetHSIConfig(authCtx, &controllerpb.GetHSIConfigRequest{NodeId: nodeID, UserId: userID})
	if dialGet.Config.DesireStatus != desireStatusConnect {
		t.Fatalf("DialPPPoE: desire_status = %q, want connect", dialGet.Config.DesireStatus)
	}

	// HangupPPPoE → desire_status = disconnect
	if _, err := client.HangupPPPoE(authCtx, &controllerpb.PPPoEActionRequest{NodeId: nodeID, UserId: userID}); err != nil {
		t.Fatalf("HangupPPPoE: %v", err)
	}
	hangGet, _ := client.GetHSIConfig(authCtx, &controllerpb.GetHSIConfigRequest{NodeId: nodeID, UserId: userID})
	if hangGet.Config.DesireStatus != desireStatusDisconnect {
		t.Fatalf("HangupPPPoE: desire_status = %q, want disconnect", hangGet.Config.DesireStatus)
	}

	// DNS record CRUD
	if _, err := client.AddOrUpdateDNSRecord(authCtx, &controllerpb.DNSRecordRequest{
		NodeId: nodeID, UserId: userID,
		Record: &controllerpb.DNSRecord{Domain: "a.example.com", Ip: "1.2.3.4", Ttl: 60},
	}); err != nil {
		t.Fatalf("AddOrUpdateDNSRecord: %v", err)
	}
	dnsResp, err := client.ListDNSRecords(authCtx, &controllerpb.ListDNSRecordsRequest{NodeId: nodeID, UserId: userID})
	if err != nil || len(dnsResp.Records) != 1 || dnsResp.Records[0].Domain != "a.example.com" {
		t.Fatalf("ListDNSRecords: %v %v", dnsResp, err)
	}
	if _, err := client.DeleteDNSRecord(authCtx, &controllerpb.DeleteDNSRecordRequest{
		NodeId: nodeID, UserId: userID, Domain: "a.example.com",
	}); err != nil {
		t.Fatalf("DeleteDNSRecord: %v", err)
	}
	dnsResp2, _ := client.ListDNSRecords(authCtx, &controllerpb.ListDNSRecordsRequest{NodeId: nodeID, UserId: userID})
	if len(dnsResp2.GetRecords()) != 0 {
		t.Fatalf("after delete: %d DNS records remain", len(dnsResp2.GetRecords()))
	}

	// Unauthenticated call → UNAUTHENTICATED
	_, err = client.GetHSIConfig(ctx, &controllerpb.GetHSIConfigRequest{NodeId: nodeID, UserId: userID})
	if status.Code(err) != codes.Unauthenticated {
		t.Fatalf("no-token: want Unauthenticated, got %v", err)
	}

	// DeleteHSIConfig
	if _, err := client.DeleteHSIConfig(authCtx, &controllerpb.DeleteHSIConfigRequest{NodeId: nodeID, UserId: userID}); err != nil {
		t.Fatalf("DeleteHSIConfig: %v", err)
	}
	_, err = client.GetHSIConfig(authCtx, &controllerpb.GetHSIConfigRequest{NodeId: nodeID, UserId: userID})
	if status.Code(err) != codes.NotFound {
		t.Fatalf("after delete: want NotFound, got %v", err)
	}

}
