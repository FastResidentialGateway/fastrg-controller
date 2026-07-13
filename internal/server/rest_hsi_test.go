package server

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	clientv3 "go.etcd.io/etcd/client/v3"
)

// TestHSIConfigCRUD drives the HSI create/list/get/update/delete handlers
// through the HTTP layer (request binding, status codes, response bodies) plus
// the CAS-conflict (409) and validation (400/409) paths.
func TestHSIConfigCRUD(t *testing.T) {
	etcd := serverTestEtcd(t)
	rs := &RestServer{etcd: etcd, jwtSecret: []byte("hsi-test-secret-1234567890abcdef")}
	tok, err := rs.generateToken("tester")
	if err != nil {
		t.Fatalf("generateToken: %v", err)
	}

	gin.SetMode(gin.TestMode)
	router := gin.New()
	api := router.Group("/api")
	// Same registration order as the app (static "users" before ":userId").
	api.GET("/config/:nodeId/hsi/users", rs.GetHSIUserIds)
	api.GET("/config/:nodeId/hsi/:userId", rs.GetHSIConfig)
	api.POST("/config/:nodeId/hsi", rs.CreateHSIConfig)
	api.PUT("/config/:nodeId/hsi/:userId", rs.UpdateHSIConfig)
	api.DELETE("/config/:nodeId/hsi/:userId", rs.DeleteHSIConfig)

	const node = "resttest-node"
	ctx := context.Background()
	etcd.Client().Delete(ctx, "configs/"+node+"/", clientv3.WithPrefix())
	t.Cleanup(func() { etcd.Client().Delete(ctx, "configs/"+node+"/", clientv3.WithPrefix()) })

	do := func(method, path, body string) (int, string) {
		var r *http.Request
		if body != "" {
			r = httptest.NewRequest(method, path, strings.NewReader(body))
			r.Header.Set("Content-Type", "application/json")
		} else {
			r = httptest.NewRequest(method, path, nil)
		}
		r.Header.Set("Authorization", tok)
		w := httptest.NewRecorder()
		router.ServeHTTP(w, r)
		return w.Code, w.Body.String()
	}

	base := "/api/config/" + node + "/hsi"
	full := `{"user_id":"1001","vlan_id":"100","account_name":"test@example.com","password":"p","dhcp_addr_pool":"192.168.1.10~192.168.1.200","dhcp_subnet":"255.255.255.0","dhcp_gateway":"192.168.1.1"}`

	// Create.
	if code, body := do(http.MethodPost, base, full); code != http.StatusOK {
		t.Fatalf("create: got %d (%s), want 200", code, body)
	}
	// Duplicate create -> 409.
	if code, body := do(http.MethodPost, base, full); code != http.StatusConflict {
		t.Fatalf("duplicate create: got %d (%s), want 409", code, body)
	}
	// List includes 1001.
	if code, body := do(http.MethodGet, base+"/users", ""); code != http.StatusOK || !strings.Contains(body, "1001") {
		t.Fatalf("list users: got %d (%s), want 200 containing 1001", code, body)
	}
	// Get 1001.
	if code, body := do(http.MethodGet, base+"/1001", ""); code != http.StatusOK || !strings.Contains(body, "test@example.com") {
		t.Fatalf("get 1001: got %d (%s), want 200 with account_name", code, body)
	}
	// Update 1001 -> vlan 200.
	upd := `{"user_id":"1001","vlan_id":"200","account_name":"u@example.com","password":"p","dhcp_addr_pool":"10.0.1.50~10.0.1.150","dhcp_subnet":"255.0.0.0","dhcp_gateway":"10.0.1.1"}`
	if code, body := do(http.MethodPut, base+"/1001", upd); code != http.StatusOK {
		t.Fatalf("update 1001: got %d (%s), want 200", code, body)
	}
	// Missing required field -> 400.
	missing := `{"user_id":"1500","vlan_id":"150","account_name":"","password":"p","dhcp_addr_pool":"192.168.1.10~192.168.1.200","dhcp_subnet":"255.255.255.0","dhcp_gateway":"192.168.1.1"}`
	if code, body := do(http.MethodPost, base, missing); code != http.StatusBadRequest {
		t.Fatalf("missing field: got %d (%s), want 400", code, body)
	}
	// Duplicate VLAN 200 (taken by 1001) -> 409 conflict.
	dupVlan := `{"user_id":"1002","vlan_id":"200","account_name":"b@example.com","password":"p","dhcp_addr_pool":"192.168.1.10~192.168.1.200","dhcp_subnet":"255.255.255.0","dhcp_gateway":"192.168.1.1"}`
	if code, body := do(http.MethodPost, base, dupVlan); code != http.StatusConflict {
		t.Fatalf("dup vlan: got %d (%s), want 409", code, body)
	}
	// Delete 1001.
	if code, body := do(http.MethodDelete, base+"/1001", ""); code != http.StatusOK {
		t.Fatalf("delete 1001: got %d (%s), want 200", code, body)
	}
	// List no longer includes 1001.
	if code, body := do(http.MethodGet, base+"/users", ""); code != http.StatusOK || strings.Contains(body, "1001") {
		t.Fatalf("after delete: got %d (%s), want 200 without 1001", code, body)
	}
}
