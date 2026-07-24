package server

import (
	"context"
	"crypto/tls"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"fastrg-controller/internal/storage"

	"github.com/gin-gonic/gin"
	"github.com/golang-jwt/jwt/v5"
)

func TestNewHardenedTLSServerPinsMinVersion(t *testing.T) {
	srv := NewHardenedTLSServer(":0", nil)
	if srv.TLSConfig == nil || srv.TLSConfig.MinVersion != tls.VersionTLS12 {
		t.Fatalf("TLSConfig = %+v, want MinVersion TLS 1.2", srv.TLSConfig)
	}
}

func TestLogoutRejectsTokenWithoutExp(t *testing.T) {
	secret := []byte("logout-test-secret-1234567890")
	rs := &RestServer{jwtSecret: secret}
	gin.SetMode(gin.TestMode)
	router := gin.New()
	router.Use(gin.Recovery())
	router.POST("/api/logout", rs.Logout)

	// A validly signed token with no exp claim: the old code panicked on the
	// unchecked claims["exp"].(float64) assertion before reaching etcd.
	noExp := jwt.NewWithClaims(jwt.SigningMethodHS256, jwt.MapClaims{"username": "alice"})
	tokenString, err := noExp.SignedString(secret)
	if err != nil {
		t.Fatalf("sign no-exp token: %v", err)
	}

	req := httptest.NewRequest(http.MethodPost, "/api/logout", nil)
	req.Header.Set("Authorization", tokenString)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	if w.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d (%s), want 401", w.Code, w.Body.String())
	}
}

func TestNicFetchInFlightGuard(t *testing.T) {
	nmm := NewNodeMonitorManager(nil)

	if !nmm.beginNicFetch("node-a") {
		t.Fatal("first beginNicFetch for node-a should succeed")
	}
	if nmm.beginNicFetch("node-a") {
		t.Fatal("second beginNicFetch for node-a should be rejected while in flight")
	}
	if !nmm.beginNicFetch("node-b") {
		t.Fatal("beginNicFetch for a different node should not be blocked")
	}
	nmm.endNicFetch("node-a")
	if !nmm.beginNicFetch("node-a") {
		t.Fatal("beginNicFetch should succeed again after endNicFetch")
	}
}

func TestSetDesireStatusMalformedStoredValue(t *testing.T) {
	if os.Getenv("TEST_ETCD_ENDPOINTS") == "" {
		t.Skip("TEST_ETCD_ENDPOINTS not set; skipping setDesireStatus integration test")
	}
	t.Setenv("ETCD_ENDPOINTS", os.Getenv("TEST_ETCD_ENDPOINTS"))
	etcd, err := storage.NewEtcdClient()
	if err != nil {
		t.Fatalf("etcd connect: %v", err)
	}
	defer etcd.Close()

	ctx := context.Background()
	nodeID := fmt.Sprintf("hardening-node-%d", time.Now().UnixNano())
	key := "configs/" + nodeID + "/hsi/9999"
	// Stored value parses as JSON but carries no user_id: the old combined
	// check wrapped a nil error and produced "parse HSI config: %!w(<nil>)".
	if _, err := etcd.Client().Put(ctx, key, `{"config":{}}`); err != nil {
		t.Fatalf("seed malformed config: %v", err)
	}
	t.Cleanup(func() {
		_, _ = etcd.Client().Delete(context.Background(), key)
	})

	rs := &RestServer{etcd: etcd}
	err = rs.setDesireStatus(ctx, nodeID, "9999", "tester", desireStatusConnect)
	if err == nil {
		t.Fatal("setDesireStatus should refuse a stored value without user_id")
	}
	if strings.Contains(err.Error(), "%!w") {
		t.Fatalf("error still carries a nil-wrapped verb: %q", err.Error())
	}
	if !strings.Contains(err.Error(), "no user_id") {
		t.Fatalf("error = %q, want the fixed no-user_id message", err.Error())
	}

	// The malformed value must be left untouched (the guard's whole point).
	resp, err := etcd.Client().Get(ctx, key)
	if err != nil || len(resp.Kvs) != 1 || string(resp.Kvs[0].Value) != `{"config":{}}` {
		t.Fatalf("stored value changed or unreadable: %v %v", resp, err)
	}
}
