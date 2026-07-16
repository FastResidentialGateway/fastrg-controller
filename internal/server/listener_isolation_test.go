package server

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestNewMetricsMuxIsolatesRoutes(t *testing.T) {
	mux := newMetricsMux()

	metricsReq := httptest.NewRequest(http.MethodGet, "/metrics", nil)
	metricsResp := httptest.NewRecorder()
	mux.ServeHTTP(metricsResp, metricsReq)
	if metricsResp.Code != http.StatusOK {
		t.Fatalf("GET /metrics status = %d, want 200", metricsResp.Code)
	}

	rootReq := httptest.NewRequest(http.MethodGet, "/", nil)
	rootResp := httptest.NewRecorder()
	mux.ServeHTTP(rootResp, rootReq)
	if rootResp.Code != http.StatusNotFound {
		t.Fatalf("GET / status = %d, want 404", rootResp.Code)
	}
}

func TestLogRouterRequiresTokenAndDoesNotExposeMetrics(t *testing.T) {
	rest := NewRestServer(nil, nil, nil, []byte("log-unit-test-secret-1234567890"))
	router := rest.NewLogRouter(filepath.Join(t.TempDir(), "controller.log"))

	for _, requestPath := range []string{"/", "/metrics"} {
		req := httptest.NewRequest(http.MethodGet, requestPath, nil)
		resp := httptest.NewRecorder()
		router.ServeHTTP(resp, req)
		if resp.Code != http.StatusUnauthorized {
			t.Errorf("GET %s without token status = %d, want 401", requestPath, resp.Code)
		}
		if strings.Contains(resp.Body.String(), "# HELP") {
			t.Errorf("GET %s unexpectedly exposed Prometheus metrics", requestPath)
		}
	}
}

func TestLogRouterAuthentication(t *testing.T) {
	etcd := serverTestEtcd(t)
	rest := NewRestServer(etcd, nil, nil, []byte("log-integration-secret-1234567890"))
	logFilePath := filepath.Join(t.TempDir(), "controller.log")
	const logContent = "task-24 authenticated log content\n"
	if err := os.WriteFile(logFilePath, []byte(logContent), 0600); err != nil {
		t.Fatalf("write temporary log: %v", err)
	}
	router := rest.NewLogRouter(logFilePath)

	token, err := rest.generateToken("task-24-log-reader")
	if err != nil {
		t.Fatalf("generate token: %v", err)
	}

	doRequest := func() *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodGet, "/", nil)
		req.Header.Set("Authorization", token)
		resp := httptest.NewRecorder()
		router.ServeHTTP(resp, req)
		return resp
	}

	validResp := doRequest()
	if validResp.Code != http.StatusOK {
		t.Fatalf("valid token status = %d, want 200; body = %q", validResp.Code, validResp.Body.String())
	}
	if !strings.Contains(validResp.Body.String(), logContent) {
		t.Fatalf("valid token body = %q, want log content %q", validResp.Body.String(), logContent)
	}

	blacklistKey := fmt.Sprintf("token_blacklist/%s", token)
	if _, err := etcd.Client().Put(context.Background(), blacklistKey, "revoked"); err != nil {
		t.Fatalf("blacklist token: %v", err)
	}
	t.Cleanup(func() { _, _ = etcd.Client().Delete(context.Background(), blacklistKey) })

	revokedResp := doRequest()
	if revokedResp.Code != http.StatusUnauthorized {
		t.Fatalf("revoked token status = %d, want 401", revokedResp.Code)
	}
}
