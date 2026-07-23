package server

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	controllerpb "fastrg-controller/proto"

	"github.com/gin-gonic/gin"
	clientv3 "go.etcd.io/etcd/client/v3"
	"google.golang.org/grpc/metadata"
)

func seedCascadeKeys(t *testing.T, nodeID, userID string) (*RestServer, context.Context) {
	t.Helper()
	etcd := serverTestEtcd(t)
	ctx := context.Background()
	prefix := "configs/" + nodeID + "/"
	if _, err := etcd.Client().Delete(ctx, prefix, clientv3.WithPrefix()); err != nil {
		t.Fatalf("clean test prefix: %v", err)
	}
	t.Cleanup(func() { _, _ = etcd.Client().Delete(context.Background(), prefix, clientv3.WithPrefix()) })

	if _, err := etcd.Client().Put(ctx, hsiKey(nodeID, userID),
		`{"config":{"user_id":"`+userID+`"},"metadata":{"resourceVersion":"1"}}`); err != nil {
		t.Fatalf("seed HSI key: %v", err)
	}
	if _, err := etcd.Client().Put(ctx, dnsKey(nodeID, userID),
		`{"records":[{"domain":"cascade.test","ip":"192.0.2.1","ttl":30}],"metadata":{"resourceVersion":"1"}}`); err != nil {
		t.Fatalf("seed DNS key: %v", err)
	}
	return &RestServer{etcd: etcd, jwtSecret: []byte("cascade-test-secret-1234567890")}, ctx
}

func assertCascadeKeysAbsent(t *testing.T, rs *RestServer, ctx context.Context, nodeID, userID string) {
	t.Helper()
	for _, key := range []string{hsiKey(nodeID, userID), dnsKey(nodeID, userID)} {
		resp, err := rs.etcd.Client().Get(ctx, key)
		if err != nil {
			t.Fatalf("get %s: %v", key, err)
		}
		if len(resp.Kvs) != 0 {
			t.Fatalf("key %s still exists after HSI deletion", key)
		}
	}
}

func TestRESTDeleteHSIConfigCascadesDNS(t *testing.T) {
	nodeID := "rest-cascade-" + time.Now().UTC().Format("150405")
	userID := "41"
	rs, ctx := seedCascadeKeys(t, nodeID, userID)

	gin.SetMode(gin.TestMode)
	router := gin.New()
	router.DELETE("/api/config/:nodeId/hsi/:userId", rs.DeleteHSIConfig)
	req := httptest.NewRequest(http.MethodDelete, "/api/config/"+nodeID+"/hsi/"+userID, nil)
	recorder := httptest.NewRecorder()
	router.ServeHTTP(recorder, req)
	if recorder.Code != http.StatusOK {
		t.Fatalf("DeleteHSIConfig status = %d body=%s, want 200", recorder.Code, recorder.Body.String())
	}
	assertCascadeKeysAbsent(t, rs, ctx, nodeID, userID)
}

func TestGRPCDeleteHSIConfigCascadesDNS(t *testing.T) {
	nodeID := "grpc-cascade-" + time.Now().UTC().Format("150405")
	userID := "42"
	rs, ctx := seedCascadeKeys(t, nodeID, userID)
	token, err := rs.generateToken("cascade-tester")
	if err != nil {
		t.Fatalf("generate token: %v", err)
	}
	authCtx := metadata.AppendToOutgoingContext(ctx, "authorization", token)
	outgoingMD, _ := metadata.FromOutgoingContext(authCtx)
	authCtx = metadata.NewIncomingContext(ctx, outgoingMD)

	service := NewConfigGrpcServer(rs.etcd, rs.jwtSecret)
	if _, err := service.DeleteHSIConfig(authCtx, &controllerpb.DeleteHSIConfigRequest{
		NodeId: nodeID,
		UserId: userID,
	}); err != nil {
		t.Fatalf("DeleteHSIConfig: %v", err)
	}
	assertCascadeKeysAbsent(t, rs, ctx, nodeID, userID)
}
