package server

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"fastrg-controller/internal/storage"
	controllerpb "fastrg-controller/proto"

	"github.com/gin-gonic/gin"
	clientv3 "go.etcd.io/etcd/client/v3"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"
)

func TestRESTUpdateHSIConfigObjectMergeIntegration(t *testing.T) {
	etcd := serverTestEtcd(t)
	ctx := context.Background()
	nodeID := fmt.Sprintf("merge-rest-%d", time.Now().UnixNano())
	userID := "21"
	key := hsiKey(nodeID, userID)
	t.Cleanup(func() {
		_, _ = etcd.Client().Delete(ctx, "configs/"+nodeID+"/", clientv3.WithPrefix())
	})

	seed := HSIConfigWithMetadata{
		Config: HSIConfig{
			UserID:             userID,
			VlanID:             "210",
			AccountName:        "before@example.com",
			Password:           "before",
			DHCPAddrPool:       "192.168.21.10-192.168.21.200",
			DHCPSubnet:         "255.255.255.0",
			DHCPGateway:        "192.168.21.1",
			DNSProxyEnable:     boolPointer(false),
			TCPConntrackEnable: boolPointer(false),
			PortMappings: []PortMapping{
				{Index: "0", DIP: "192.168.21.10", DPort: "80", EPort: "8080"},
				{Index: "1", DIP: "192.168.21.11", DPort: "443", EPort: "8443"},
			},
			DesireStatus: desireStatusConnect,
		},
		Metadata: HSIMetadata{
			Node:            nodeID,
			ResourceVersion: "7",
			UpdatedBy:       "seed",
			UpdatedAt:       "2026-01-01T00:00:00Z",
		},
	}
	putJSONValue(t, etcd, key, seed)

	rs := &RestServer{etcd: etcd, jwtSecret: []byte("merge-rest-secret-1234567890")}
	token, err := rs.generateToken("merge-admin")
	if err != nil {
		t.Fatalf("generateToken: %v", err)
	}
	gin.SetMode(gin.TestMode)
	router := gin.New()
	router.PUT("/api/config/:nodeId/hsi/:userId", rs.UpdateHSIConfig)
	router.GET("/api/config/:nodeId/hsi/:userId", rs.GetHSIConfig)

	do := func(method, body string) *httptest.ResponseRecorder {
		t.Helper()
		var request *http.Request
		path := "/api/config/" + nodeID + "/hsi/" + userID
		if body == "" {
			request = httptest.NewRequest(method, path, nil)
		} else {
			request = httptest.NewRequest(method, path, strings.NewReader(body))
			request.Header.Set("Content-Type", "application/json")
		}
		request.Header.Set("Authorization", token)
		response := httptest.NewRecorder()
		router.ServeHTTP(response, request)
		return response
	}

	omittedOptionals := `{
		"user_id":"21",
		"vlan_id":"211",
		"account_name":"after@example.com",
		"password":"after",
		"dhcp_addr_pool":"192.168.22.10-192.168.22.200",
		"dhcp_subnet":"255.255.255.0",
		"dhcp_gateway":"192.168.22.1",
		"desire_status":"disconnect"
	}`
	if response := do(http.MethodPut, omittedOptionals); response.Code != http.StatusOK {
		t.Fatalf("PUT omitted optionals: got %d (%s), want 200", response.Code, response.Body.String())
	}

	response := do(http.MethodGet, "")
	if response.Code != http.StatusOK {
		t.Fatalf("GET merged config: got %d (%s), want 200", response.Code, response.Body.String())
	}
	var got HSIConfigWithMetadata
	if err := json.Unmarshal(response.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode GET response: %v", err)
	}
	if got.Config.DNSProxyEnable == nil || *got.Config.DNSProxyEnable {
		t.Fatalf("DNSProxyEnable = %v, want preserved false", got.Config.DNSProxyEnable)
	}
	if got.Config.TCPConntrackEnable == nil || *got.Config.TCPConntrackEnable {
		t.Fatalf("TCPConntrackEnable = %v, want preserved false", got.Config.TCPConntrackEnable)
	}
	if len(got.Config.PortMappings) != 2 {
		t.Fatalf("PortMappings = %+v, want two preserved mappings", got.Config.PortMappings)
	}
	if got.Config.DesireStatus != desireStatusConnect {
		t.Fatalf("DesireStatus = %q, want preserved %q", got.Config.DesireStatus, desireStatusConnect)
	}
	if got.Metadata.ResourceVersion != "8" {
		t.Fatalf("ResourceVersion = %q, want 8", got.Metadata.ResourceVersion)
	}
	if got.Metadata.UpdatedBy != "merge-admin" || got.Metadata.UpdatedAt == seed.Metadata.UpdatedAt {
		t.Fatalf("metadata not re-stamped: %+v", got.Metadata)
	}

	clearMappings := `{
		"user_id":"21",
		"vlan_id":"211",
		"account_name":"after@example.com",
		"password":"after",
		"dhcp_addr_pool":"192.168.22.10-192.168.22.200",
		"dhcp_subnet":"255.255.255.0",
		"dhcp_gateway":"192.168.22.1",
		"port-mapping":[]
	}`
	if response := do(http.MethodPut, clearMappings); response.Code != http.StatusOK {
		t.Fatalf("PUT explicit empty mappings: got %d (%s), want 200", response.Code, response.Body.String())
	}
	cleared := getRESTHSIEnvelope(t, etcd, key)
	if len(cleared.Config.PortMappings) != 0 {
		t.Fatalf("PortMappings after clear = %+v, want empty", cleared.Config.PortMappings)
	}
	if cleared.Metadata.ResourceVersion != "9" {
		t.Fatalf("ResourceVersion after clear = %q, want 9", cleared.Metadata.ResourceVersion)
	}
}

func TestGRPCUpdateHSIConfigObjectMergeIntegration(t *testing.T) {
	etcd := serverTestEtcd(t)
	ctx := context.Background()
	nodeID := fmt.Sprintf("merge-grpc-%d", time.Now().UnixNano())
	userID := "22"
	key := hsiKey(nodeID, userID)
	t.Cleanup(func() {
		_, _ = etcd.Client().Delete(ctx, "configs/"+nodeID+"/", clientv3.WithPrefix())
	})

	seed := hsiConfigWithMetadata{
		Config: hsiConfigInner{
			UserID:             userID,
			VlanID:             "220",
			AccountName:        "before@example.com",
			Password:           "before",
			DHCPAddrPool:       "192.168.22.10-192.168.22.200",
			DHCPSubnet:         "255.255.255.0",
			DHCPGateway:        "192.168.22.1",
			DNSProxyEnable:     boolPointer(true),
			TCPConntrackEnable: boolPointer(true),
			PortMappings: []portMapping{
				{Index: "0", DIP: "192.168.22.10", DPort: "80", EPort: "8080"},
				{Index: "1", DIP: "192.168.22.11", DPort: "443", EPort: "8443"},
			},
			DesireStatus: desireStatusConnect,
		},
		Metadata: hsiMetaInner{
			Node:            nodeID,
			ResourceVersion: "11",
			UpdatedBy:       "seed",
			UpdatedAt:       "2026-01-01T00:00:00Z",
		},
	}
	putJSONValue(t, etcd, key, seed)

	secret := []byte("merge-grpc-secret-123456789")
	rs := &RestServer{jwtSecret: secret}
	token, err := rs.generateToken("grpc-merge-admin")
	if err != nil {
		t.Fatalf("generateToken: %v", err)
	}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	grpcServer := grpc.NewServer()
	controllerpb.RegisterConfigServiceServer(grpcServer, NewConfigGrpcServer(etcd, secret))
	go func() {
		_ = grpcServer.Serve(listener)
	}()
	t.Cleanup(grpcServer.Stop)

	connection, err := grpc.NewClient(listener.Addr().String(), grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		t.Fatalf("grpc dial: %v", err)
	}
	t.Cleanup(func() { _ = connection.Close() })
	client := controllerpb.NewConfigServiceClient(connection)
	authCtx := metadata.AppendToOutgoingContext(ctx, "authorization", token)

	requestConfig := func() *controllerpb.HSIConfig {
		return &controllerpb.HSIConfig{
			UserId:       userID,
			VlanId:       "221",
			AccountName:  "after@example.com",
			Password:     "after",
			DhcpAddrPool: "192.168.23.10-192.168.23.200",
			DhcpSubnet:   "255.255.255.0",
			DhcpGateway:  "192.168.23.1",
			DesireStatus: desireStatusDisconnect,
		}
	}

	explicitFalse := requestConfig()
	explicitFalse.DnsProxyEnable = proto.Bool(false)
	explicitFalse.TcpConntrackEnable = proto.Bool(false)
	explicitFalseResponse, err := client.UpdateHSIConfig(authCtx, &controllerpb.UpdateHSIConfigRequest{
		NodeId: nodeID,
		UserId: userID,
		Config: explicitFalse,
	})
	if err != nil {
		t.Fatalf("UpdateHSIConfig explicit false: %v", err)
	}
	if explicitFalseResponse.Config.DnsProxyEnable == nil || explicitFalseResponse.Config.GetDnsProxyEnable() {
		t.Fatalf("DNSProxyEnable = %v, want explicit false", explicitFalseResponse.Config.DnsProxyEnable)
	}
	if explicitFalseResponse.Config.TcpConntrackEnable == nil || explicitFalseResponse.Config.GetTcpConntrackEnable() {
		t.Fatalf("TCPConntrackEnable = %v, want explicit false", explicitFalseResponse.Config.TcpConntrackEnable)
	}
	if len(explicitFalseResponse.Config.PortMappings) != 2 {
		t.Fatalf("PortMappings = %+v, want two preserved mappings", explicitFalseResponse.Config.PortMappings)
	}
	if explicitFalseResponse.Metadata.ResourceVersion != "12" {
		t.Fatalf("ResourceVersion = %q, want 12", explicitFalseResponse.Metadata.ResourceVersion)
	}

	omittedResponse, err := client.UpdateHSIConfig(authCtx, &controllerpb.UpdateHSIConfigRequest{
		NodeId: nodeID,
		UserId: userID,
		Config: requestConfig(),
	})
	if err != nil {
		t.Fatalf("UpdateHSIConfig omitted optionals: %v", err)
	}
	if omittedResponse.Config.DnsProxyEnable == nil || omittedResponse.Config.GetDnsProxyEnable() {
		t.Fatalf("DNSProxyEnable = %v, want preserved false", omittedResponse.Config.DnsProxyEnable)
	}
	if omittedResponse.Config.TcpConntrackEnable == nil || omittedResponse.Config.GetTcpConntrackEnable() {
		t.Fatalf("TCPConntrackEnable = %v, want preserved false", omittedResponse.Config.TcpConntrackEnable)
	}
	if len(omittedResponse.Config.PortMappings) != 2 {
		t.Fatalf("PortMappings = %+v, want two preserved mappings", omittedResponse.Config.PortMappings)
	}
	if omittedResponse.Config.DesireStatus != desireStatusConnect {
		t.Fatalf("DesireStatus = %q, want preserved %q", omittedResponse.Config.DesireStatus, desireStatusConnect)
	}
	if omittedResponse.Metadata.ResourceVersion != "13" || omittedResponse.Metadata.UpdatedBy != "grpc-merge-admin" {
		t.Fatalf("metadata after omitted update = %+v", omittedResponse.Metadata)
	}

	clearResponse, err := client.UpdateHSIConfig(authCtx, &controllerpb.UpdateHSIConfigRequest{
		NodeId:            nodeID,
		UserId:            userID,
		Config:            requestConfig(),
		ClearPortMappings: true,
	})
	if err != nil {
		t.Fatalf("UpdateHSIConfig clear mappings: %v", err)
	}
	if len(clearResponse.Config.PortMappings) != 0 {
		t.Fatalf("PortMappings after clear = %+v, want empty", clearResponse.Config.PortMappings)
	}
	if clearResponse.Metadata.ResourceVersion != "14" {
		t.Fatalf("ResourceVersion after clear = %q, want 14", clearResponse.Metadata.ResourceVersion)
	}

	conflicting := requestConfig()
	conflicting.PortMappings = []*controllerpb.PortMapping{{Index: "conflict"}}
	_, err = client.UpdateHSIConfig(authCtx, &controllerpb.UpdateHSIConfigRequest{
		NodeId:            nodeID,
		UserId:            userID,
		Config:            conflicting,
		ClearPortMappings: true,
	})
	if status.Code(err) != codes.InvalidArgument {
		t.Fatalf("conflicting port mapping update: got %v, want InvalidArgument", err)
	}
	stored := getGRPCHSIEnvelope(t, etcd, key)
	if stored.Metadata.ResourceVersion != "14" {
		t.Fatalf("conflicting request changed ResourceVersion to %q, want 14", stored.Metadata.ResourceVersion)
	}
}

func TestRESTHSIConfigMergeUsesRefreshedCurrentAfterCASConflict(t *testing.T) {
	etcd := serverTestEtcd(t)
	ctx := context.Background()
	nodeID := fmt.Sprintf("merge-cas-%d", time.Now().UnixNano())
	userID := "23"
	key := hsiKey(nodeID, userID)
	t.Cleanup(func() {
		_, _ = etcd.Client().Delete(ctx, key)
	})

	initial := HSIConfigWithMetadata{
		Config: HSIConfig{
			UserID:             userID,
			PortMappings:       []PortMapping{{Index: "initial"}},
			DNSProxyEnable:     boolPointer(false),
			TCPConntrackEnable: boolPointer(false),
			DesireStatus:       desireStatusDisconnect,
		},
		Metadata: HSIMetadata{ResourceVersion: "1"},
	}
	putJSONValue(t, etcd, key, initial)

	requested := HSIConfig{
		UserID:       userID,
		VlanID:       "230",
		AccountName:  "cas@example.com",
		Password:     "after",
		DHCPAddrPool: "192.168.23.10-192.168.23.200",
		DHCPSubnet:   "255.255.255.0",
		DHCPGateway:  "192.168.23.1",
	}
	calls := 0
	err := etcd.CAS(ctx, key, func(current []byte) (storage.CASResult, error) {
		calls++
		var existing HSIConfigWithMetadata
		if err := json.Unmarshal(current, &existing); err != nil {
			return storage.CASResult{}, err
		}
		merged := mergeRESTHSIConfigUpdate(requested, &existing.Config)
		next := HSIConfigWithMetadata{
			Config: merged,
			Metadata: HSIMetadata{
				Node:            nodeID,
				ResourceVersion: nextResourceVersion(current),
			},
		}

		if calls == 1 {
			concurrent := existing
			concurrent.Config.PortMappings = []PortMapping{{Index: "concurrent"}}
			concurrent.Config.DesireStatus = desireStatusConnect
			concurrent.Metadata.ResourceVersion = "2"
			putJSONValue(t, etcd, key, concurrent)
		}

		value, err := json.Marshal(next)
		if err != nil {
			return storage.CASResult{}, err
		}
		return storage.CASResult{Value: value}, nil
	})
	if err != nil {
		t.Fatalf("CAS merge: %v", err)
	}
	if calls != 2 {
		t.Fatalf("mutate calls = %d, want 2", calls)
	}
	got := getRESTHSIEnvelope(t, etcd, key)
	if len(got.Config.PortMappings) != 1 || got.Config.PortMappings[0].Index != "concurrent" {
		t.Fatalf("PortMappings = %+v, want concurrently written mapping", got.Config.PortMappings)
	}
	if got.Config.DesireStatus != desireStatusConnect {
		t.Fatalf("DesireStatus = %q, want concurrently written %q", got.Config.DesireStatus, desireStatusConnect)
	}
	if got.Metadata.ResourceVersion != "3" {
		t.Fatalf("ResourceVersion = %q, want 3", got.Metadata.ResourceVersion)
	}
}

func putJSONValue(t *testing.T, etcd *storage.EtcdClient, key string, value any) {
	t.Helper()
	encoded, err := json.Marshal(value)
	if err != nil {
		t.Fatalf("marshal %s: %v", key, err)
	}
	if _, err := etcd.Client().Put(context.Background(), key, string(encoded)); err != nil {
		t.Fatalf("put %s: %v", key, err)
	}
}

func getRESTHSIEnvelope(t *testing.T, etcd *storage.EtcdClient, key string) HSIConfigWithMetadata {
	t.Helper()
	response, err := etcd.Client().Get(context.Background(), key)
	if err != nil || len(response.Kvs) != 1 {
		t.Fatalf("get %s: kvs=%d err=%v", key, len(response.Kvs), err)
	}
	var got HSIConfigWithMetadata
	if err := json.Unmarshal(response.Kvs[0].Value, &got); err != nil {
		t.Fatalf("decode %s: %v", key, err)
	}
	return got
}

func getGRPCHSIEnvelope(t *testing.T, etcd *storage.EtcdClient, key string) hsiConfigWithMetadata {
	t.Helper()
	response, err := etcd.Client().Get(context.Background(), key)
	if err != nil || len(response.Kvs) != 1 {
		t.Fatalf("get %s: kvs=%d err=%v", key, len(response.Kvs), err)
	}
	var got hsiConfigWithMetadata
	if err := json.Unmarshal(response.Kvs[0].Value, &got); err != nil {
		t.Fatalf("decode %s: %v", key, err)
	}
	return got
}
