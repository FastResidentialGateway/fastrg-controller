package server

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
)

func TestRESTPathIDValidationMiddleware(t *testing.T) {
	gin.SetMode(gin.TestMode)
	router := (&RestServer{}).newRouter()

	tests := []struct {
		name       string
		path       string
		wantStatus int
	}{
		// With gin's default routing (UseRawPath off), an encoded slash is
		// decoded before route matching, adds a path segment, and never
		// reaches a single-segment param route: 404, not 400.
		{name: "escaped slash in node ID", path: "/api/config/a%2Fb/hsi/1001", wantStatus: http.StatusNotFound},
		{name: "non-numeric user ID", path: "/api/config/node1/hsi/abc", wantStatus: http.StatusBadRequest},
		{name: "space in node ID", path: "/api/config/a%20b/hsi/1001", wantStatus: http.StatusBadRequest},
		{name: "dot-dot node ID", path: "/api/config/../hsi/1001", wantStatus: http.StatusBadRequest},
		{name: "invalid unregister node ID", path: "/api/nodes/bad%2Fnode", wantStatus: http.StatusNotFound},
		{name: "valid IDs reach auth", path: "/api/config/test-node-001/hsi/1001", wantStatus: http.StatusUnauthorized},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, tt.path, nil)
			if strings.HasPrefix(tt.path, "/api/nodes/") {
				req.Method = http.MethodDelete
			}
			resp := httptest.NewRecorder()
			router.ServeHTTP(resp, req)
			if resp.Code != tt.wantStatus {
				t.Fatalf("%s returned %d (%s), want %d", tt.path, resp.Code, resp.Body.String(), tt.wantStatus)
			}
		})
	}
}

func TestRESTBodyIDValidationBeforeStorage(t *testing.T) {
	gin.SetMode(gin.TestMode)
	router := gin.New()
	rs := &RestServer{}
	router.POST("/api/config/:nodeId/hsi", rs.CreateHSIConfig)
	router.POST("/api/pppoe/dial", rs.DialPPPoE)

	tests := []struct {
		name string
		path string
		body string
	}{
		{
			name: "create rejects non-numeric body user ID",
			path: "/api/config/node1/hsi",
			body: `{"user_id":"abc","vlan_id":"100","account_name":"user@example.com","password":"pw","dhcp_addr_pool":"192.0.2.2-192.0.2.10","dhcp_subnet":"255.255.255.0","dhcp_gateway":"192.0.2.1"}`,
		},
		{
			name: "dial rejects unsafe body node ID",
			path: "/api/pppoe/dial",
			body: `{"node_id":"bad/node","user_id":"1001"}`,
		},
		{
			name: "dial rejects non-numeric body user ID",
			path: "/api/pppoe/dial",
			body: `{"node_id":"node1","user_id":"abc"}`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodPost, tt.path, strings.NewReader(tt.body))
			req.Header.Set("Content-Type", "application/json")
			resp := httptest.NewRecorder()
			router.ServeHTTP(resp, req)
			if resp.Code != http.StatusBadRequest {
				t.Fatalf("returned %d (%s), want 400", resp.Code, resp.Body.String())
			}
		})
	}
}
