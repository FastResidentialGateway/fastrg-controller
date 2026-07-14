package server

import (
	"context"
	"fmt"
	"net"
	"os"
	"testing"
	"time"

	"fastrg-controller/internal/storage"
	controllerpb "fastrg-controller/proto"

	clientv3 "go.etcd.io/etcd/client/v3"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
)

func TestConfigServiceTokenBlacklistIntegration(t *testing.T) {
	etcdEndpoints := os.Getenv("TEST_ETCD_ENDPOINTS")
	if etcdEndpoints == "" {
		t.Skip("TEST_ETCD_ENDPOINTS not set; skipping ConfigService token blacklist integration test")
	}

	t.Setenv("ETCD_ENDPOINTS", etcdEndpoints)
	etcdClient, err := storage.NewEtcdClient()
	if err != nil {
		t.Fatalf("etcd connect: %v", err)
	}
	t.Cleanup(etcdClient.Close)

	uniqueID := fmt.Sprintf("%d", time.Now().UnixNano())
	username := "grpc-auth-" + uniqueID
	nodeID := "grpc-auth-node-" + uniqueID
	secret := []byte(GetJWTSecret())
	token, err := (&RestServer{jwtSecret: secret}).generateToken(username)
	if err != nil {
		t.Fatalf("generateToken: %v", err)
	}

	blacklistKey := fmt.Sprintf("token_blacklist/%s", token)
	configPrefix := fmt.Sprintf("configs/%s/", nodeID)
	subscriberCountKey := fmt.Sprintf("user_counts/%s/", nodeID)
	cleanupCtx := context.Background()
	t.Cleanup(func() {
		if _, err := etcdClient.Client().Delete(cleanupCtx, blacklistKey); err != nil {
			t.Errorf("cleanup blacklist key: %v", err)
		}
		if _, err := etcdClient.Client().Delete(cleanupCtx, configPrefix, clientv3.WithPrefix()); err != nil {
			t.Errorf("cleanup config prefix: %v", err)
		}
		if _, err := etcdClient.Client().Delete(cleanupCtx, subscriberCountKey); err != nil {
			t.Errorf("cleanup subscriber count key: %v", err)
		}
	})

	lis, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	grpcServer := grpc.NewServer()
	controllerpb.RegisterConfigServiceServer(grpcServer, NewConfigGrpcServer(etcdClient, secret))
	go func() {
		_ = grpcServer.Serve(lis)
	}()
	t.Cleanup(grpcServer.Stop)

	conn, err := grpc.NewClient(lis.Addr().String(), grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		t.Fatalf("grpc dial: %v", err)
	}
	defer conn.Close()
	client := controllerpb.NewConfigServiceClient(conn)
	request := &controllerpb.SetSubscriberCountRequest{NodeId: nodeID, SubscriberCount: 10}

	t.Run("valid token", func(t *testing.T) {
		authCtx := metadata.AppendToOutgoingContext(context.Background(), "authorization", token)
		if _, err := client.SetSubscriberCount(authCtx, request); err != nil {
			t.Fatalf("valid token rejected: %v", err)
		}
	})

	if _, err := etcdClient.Client().Put(context.Background(), blacklistKey, "revoked"); err != nil {
		t.Fatalf("blacklist token: %v", err)
	}

	t.Run("revoked token", func(t *testing.T) {
		authCtx := metadata.AppendToOutgoingContext(context.Background(), "authorization", token)
		_, err := client.SetSubscriberCount(authCtx, request)
		if status.Code(err) != codes.Unauthenticated {
			t.Fatalf("revoked token: want Unauthenticated, got %v", err)
		}
	})

	t.Run("missing token", func(t *testing.T) {
		_, err := client.SetSubscriberCount(context.Background(), request)
		if status.Code(err) != codes.Unauthenticated {
			t.Fatalf("missing token: want Unauthenticated, got %v", err)
		}
	})

	t.Run("invalid token", func(t *testing.T) {
		authCtx := metadata.AppendToOutgoingContext(context.Background(), "authorization", "not-a-valid-token")
		_, err := client.SetSubscriberCount(authCtx, request)
		if status.Code(err) != codes.Unauthenticated {
			t.Fatalf("invalid token: want Unauthenticated, got %v", err)
		}
	})
}
