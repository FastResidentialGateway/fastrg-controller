package server

import (
	"context"
	"net"
	"os"
	"testing"

	"fastrg-controller/internal/storage"
	controllerpb "fastrg-controller/proto"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
)

func TestGrpcIDValidationHelpers(t *testing.T) {
	tests := []struct {
		name string
		err  error
	}{
		{name: "invalid node ID", err: validateNodeIDToStatus("bad/node")},
		{name: "empty node ID", err: validateNodeIDToStatus("")},
		{name: "non-numeric user ID", err: validateNodeAndUserIDsToStatus("node1", "abc")},
		{name: "leading-zero user ID", err: validateNodeAndUserIDsToStatus("node1", "01")},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if code := status.Code(tt.err); code != codes.InvalidArgument {
				t.Fatalf("status code = %s, want %s", code, codes.InvalidArgument)
			}
		})
	}

	if err := validateNodeIDToStatus("test-node-001"); err != nil {
		t.Fatalf("valid node ID rejected: %v", err)
	}
	if err := validateNodeAndUserIDsToStatus("node1", "1001"); err != nil {
		t.Fatalf("valid IDs rejected: %v", err)
	}
}

// TestConfigRPCRejectsInvalidIDs drives real RPCs (with authentication, over a
// real etcd) to prove invalid IDs are rejected after auth and before storage.
func TestConfigRPCRejectsInvalidIDs(t *testing.T) {
	etcdEndpoints := os.Getenv("TEST_ETCD_ENDPOINTS")
	if etcdEndpoints == "" {
		t.Skip("TEST_ETCD_ENDPOINTS not set; skipping ConfigService ID validation test")
	}

	t.Setenv("ETCD_ENDPOINTS", etcdEndpoints)
	etcdClient, err := storage.NewEtcdClient()
	if err != nil {
		t.Fatalf("etcd connect: %v", err)
	}
	defer etcdClient.Close()

	secret := []byte(GetJWTSecret())
	rs := &RestServer{jwtSecret: secret}
	token, err := rs.generateToken("id-validation-tester")
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
	authCtx := metadata.AppendToOutgoingContext(context.Background(), "authorization", token)

	tests := []struct {
		name string
		call func() error
	}{
		{
			name: "invalid node ID",
			call: func() error {
				_, err := client.ListHSIConfigs(authCtx, &controllerpb.ListHSIConfigsRequest{NodeId: "bad/node"})
				return err
			},
		},
		{
			name: "non-numeric user ID",
			call: func() error {
				_, err := client.GetHSIConfig(authCtx, &controllerpb.GetHSIConfigRequest{NodeId: "node1", UserId: "abc"})
				return err
			},
		},
		{
			name: "create body leading-zero user ID",
			call: func() error {
				_, err := client.CreateHSIConfig(authCtx, &controllerpb.CreateHSIConfigRequest{
					NodeId: "node1",
					Config: &controllerpb.HSIConfig{
						UserId: "01", VlanId: "100", AccountName: "user@example.com", Password: "pw",
						DhcpAddrPool: "192.0.2.2-192.0.2.10", DhcpSubnet: "255.255.255.0", DhcpGateway: "192.0.2.1",
					},
				})
				return err
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if code := status.Code(tt.call()); code != codes.InvalidArgument {
				t.Fatalf("status code = %s, want %s", code, codes.InvalidArgument)
			}
		})
	}

	// Authentication still comes first: without a token, an invalid ID must
	// surface Unauthenticated, not InvalidArgument.
	_, err = client.ListHSIConfigs(context.Background(), &controllerpb.ListHSIConfigsRequest{NodeId: "bad/node"})
	if code := status.Code(err); code != codes.Unauthenticated {
		t.Fatalf("no-token invalid ID: status code = %s, want %s", code, codes.Unauthenticated)
	}
}
