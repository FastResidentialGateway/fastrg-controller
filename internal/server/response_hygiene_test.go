package server

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	fastrgnodepb "fastrg-controller/proto/fastrgnodepb"

	"github.com/gin-gonic/gin"
	clientv3 "go.etcd.io/etcd/client/v3"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/emptypb"
)

type emptyNodeInfoServer struct {
	fastrgnodepb.UnimplementedFastrgServiceServer
}

func (emptyNodeInfoServer) GetFastrgHsiInfo(context.Context, *emptypb.Empty) (*fastrgnodepb.FastrgHsiInfo, error) {
	return &fastrgnodepb.FastrgHsiInfo{HsiInfos: []*fastrgnodepb.HsiInfo{}}, nil
}

func (emptyNodeInfoServer) GetFastrgDhcpInfo(context.Context, *emptypb.Empty) (*fastrgnodepb.FastrgDhcpInfo, error) {
	return &fastrgnodepb.FastrgDhcpInfo{DhcpInfos: []*fastrgnodepb.DhcpInfo{}}, nil
}

func TestGetHSIConfigMissingReturnsNotFound(t *testing.T) {
	etcd := serverTestEtcd(t)
	const nodeID = "response-hygiene-missing"
	ctx := context.Background()
	_, err := etcd.Client().Delete(ctx, "configs/"+nodeID+"/", clientv3.WithPrefix())
	if err != nil {
		t.Fatalf("clear test prefix: %v", err)
	}
	t.Cleanup(func() {
		_, _ = etcd.Client().Delete(context.Background(), "configs/"+nodeID+"/", clientv3.WithPrefix())
	})

	rs := &RestServer{etcd: etcd}
	gin.SetMode(gin.TestMode)
	router := gin.New()
	router.GET("/api/config/:nodeId/hsi/:userId", rs.GetHSIConfig)

	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/config/"+nodeID+"/hsi/1001", nil)
	router.ServeHTTP(w, req)

	if w.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want %d; body=%s", w.Code, http.StatusNotFound, w.Body.String())
	}
	assertErrorResponse(t, w.Body.Bytes(), "HSI config not found")
}

func TestLiveNodeInfoMissingUserReturnsNotFound(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:50052")
	if err != nil {
		t.Fatalf("listen for in-process FastRG node: %v", err)
	}
	grpcServer := grpc.NewServer()
	fastrgnodepb.RegisterFastrgServiceServer(grpcServer, emptyNodeInfoServer{})
	go func() {
		_ = grpcServer.Serve(listener)
	}()
	t.Cleanup(func() {
		grpcServer.Stop()
		_ = listener.Close()
	})

	manager := NewNodeMonitorManager(nil)
	const nodeID = "response-hygiene-node"
	if err := manager.StartMonitoring(nodeID, "127.0.0.1"); err != nil {
		t.Fatalf("start monitoring: %v", err)
	}
	t.Cleanup(func() { manager.StopMonitoring(nodeID) })

	rs := &RestServer{nodeMonitorMgr: manager}
	gin.SetMode(gin.TestMode)
	router := gin.New()
	router.GET("/api/config/:nodeId/pppoe/:userId", rs.GetPPPoEInfo)
	router.GET("/api/config/:nodeId/dhcp/:userId", rs.GetDhcpConfig)

	tests := []struct {
		name        string
		path        string
		wantMessage string
	}{
		{name: "PPPoE session", path: "/api/config/" + nodeID + "/pppoe/1001", wantMessage: "PPPoE session not found for user"},
		{name: "DHCP config", path: "/api/config/" + nodeID + "/dhcp/1001", wantMessage: "DHCP config not found for user"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			w := httptest.NewRecorder()
			req := httptest.NewRequest(http.MethodGet, tt.path, nil)
			router.ServeHTTP(w, req)

			if w.Code != http.StatusNotFound {
				t.Fatalf("status = %d, want %d; body=%s", w.Code, http.StatusNotFound, w.Body.String())
			}
			assertErrorResponse(t, w.Body.Bytes(), tt.wantMessage)
		})
	}
}

func TestCASToStatusHidesInternalError(t *testing.T) {
	const secret = "secret-detail"
	err := casToStatus(errors.New(secret))
	if status.Code(err) != codes.Internal {
		t.Fatalf("code = %s, want %s", status.Code(err), codes.Internal)
	}
	if strings.Contains(status.Convert(err).Message(), secret) {
		t.Fatalf("message %q leaked internal error %q", status.Convert(err).Message(), secret)
	}
}

func assertErrorResponse(t *testing.T, body []byte, want string) {
	t.Helper()
	var response struct {
		Error string `json:"error"`
	}
	if err := json.Unmarshal(body, &response); err != nil {
		t.Fatalf("decode response %q: %v", body, err)
	}
	if response.Error != want {
		t.Fatalf("error = %q, want %q", response.Error, want)
	}
}
