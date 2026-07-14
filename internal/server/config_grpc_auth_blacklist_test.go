package server

import (
	"context"
	"fmt"
	"net"
	"testing"
	"time"

	controllerpb "fastrg-controller/proto"

	clientv3 "go.etcd.io/etcd/client/v3"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
)

func TestConfigServiceTokenBlacklist(t *testing.T) {
	etcd := serverTestEtcd(t)
	testID := fmt.Sprintf("%d", time.Now().UnixNano())
	nodeID := "grpc-auth-node-" + testID
	blacklistedUsername := "grpc-revoked-" + testID
	activeUsername := "grpc-active-" + testID
	secret := []byte("grpc-blacklist-test-secret-1234567890")
	tokenIssuer := &RestServer{jwtSecret: secret}

	blacklistedToken, err := tokenIssuer.generateToken(blacklistedUsername)
	if err != nil {
		t.Fatalf("generate blacklisted token: %v", err)
	}
	activeToken, err := tokenIssuer.generateToken(activeUsername)
	if err != nil {
		t.Fatalf("generate active token: %v", err)
	}

	blacklistKey := fmt.Sprintf("token_blacklist/%s", blacklistedToken)
	cleanupKeys := func() {
		cleanupCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if _, err := etcd.Client().Delete(cleanupCtx, blacklistKey); err != nil {
			t.Errorf("cleanup blacklist key: %v", err)
		}
		if _, err := etcd.Client().Delete(cleanupCtx, subscriberCountKey(nodeID)); err != nil {
			t.Errorf("cleanup subscriber count key: %v", err)
		}
		if _, err := etcd.Client().Delete(cleanupCtx, fmt.Sprintf("configs/%s/", nodeID), clientv3.WithPrefix()); err != nil {
			t.Errorf("cleanup config keys: %v", err)
		}
	}
	t.Cleanup(cleanupKeys)

	setupCtx, setupCancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer setupCancel()
	if _, err := etcd.Client().Put(setupCtx, blacklistKey, "revoked"); err != nil {
		t.Fatalf("blacklist token: %v", err)
	}

	lis, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	grpcServer := grpc.NewServer()
	controllerpb.RegisterConfigServiceServer(grpcServer, NewConfigGrpcServer(etcd, secret))
	go func() {
		_ = grpcServer.Serve(lis)
	}()
	t.Cleanup(grpcServer.Stop)

	conn, err := grpc.NewClient(lis.Addr().String(), grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		t.Fatalf("dial gRPC: %v", err)
	}
	t.Cleanup(func() { conn.Close() })
	client := controllerpb.NewConfigServiceClient(conn)

	call := func(ctx context.Context) error {
		callCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
		defer cancel()
		_, err := client.SetSubscriberCount(callCtx, &controllerpb.SetSubscriberCountRequest{
			NodeId: nodeID, SubscriberCount: 10,
		})
		return err
	}

	activeCtx := metadata.AppendToOutgoingContext(context.Background(), "authorization", activeToken)
	if err := call(activeCtx); err != nil {
		t.Fatalf("unrevoked token should succeed: %v", err)
	}

	revokedCtx := metadata.AppendToOutgoingContext(context.Background(), "authorization", blacklistedToken)
	if err := call(revokedCtx); status.Code(err) != codes.Unauthenticated {
		t.Fatalf("revoked token: got %v, want Unauthenticated", err)
	} else if status.Convert(err).Message() != "token has been revoked" {
		t.Fatalf("revoked token message = %q, want %q", status.Convert(err).Message(), "token has been revoked")
	}

	if err := call(context.Background()); status.Code(err) != codes.Unauthenticated {
		t.Fatalf("missing token: got %v, want Unauthenticated", err)
	}

	invalidCtx := metadata.AppendToOutgoingContext(context.Background(), "authorization", "not-a-valid-token")
	if err := call(invalidCtx); status.Code(err) != codes.Unauthenticated {
		t.Fatalf("invalid token: got %v, want Unauthenticated", err)
	}
}
