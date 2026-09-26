package server

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"

	"fastrg-controller/internal/storage"

	"github.com/gin-gonic/gin"
)

// serverTestEtcd returns an etcd client against TEST_ETCD_ENDPOINTS (skips the
// test when unset). Shared by the REST/gRPC integration tests in this package.
func serverTestEtcd(t *testing.T) *storage.EtcdClient {
	t.Helper()
	eps := os.Getenv("TEST_ETCD_ENDPOINTS")
	if eps == "" {
		t.Skip("TEST_ETCD_ENDPOINTS not set; skipping etcd-backed server test")
	}
	t.Setenv("ETCD_ENDPOINTS", eps)
	etcd, err := storage.NewEtcdClient()
	if err != nil {
		t.Fatalf("etcd connect: %v", err)
	}
	t.Cleanup(func() { etcd.Close() })
	return etcd
}

// TestJWTRoundtrip: a generated token validates back to its username.
func TestJWTRoundtrip(t *testing.T) {
	rs := &RestServer{jwtSecret: []byte("unit-test-secret-abcdefghijklmnop")}
	tok, err := rs.generateToken("alice")
	if err != nil {
		t.Fatalf("generateToken: %v", err)
	}
	user, err := rs.getUserFromToken(tok)
	if err != nil {
		t.Fatalf("getUserFromToken: %v", err)
	}
	if user != "alice" {
		t.Fatalf("username = %q, want alice", user)
	}
}

// TestGetUserFromTokenRejectsGarbage: a non-token string is rejected.
func TestGetUserFromTokenRejectsGarbage(t *testing.T) {
	rs := &RestServer{jwtSecret: []byte("unit-test-secret-abcdefghijklmnop")}
	if _, err := rs.getUserFromToken("not-a-real-token"); err == nil {
		t.Fatal("expected error for garbage token")
	}
}

// TestGetUserFromTokenRejectsWrongSecret: a token signed with a different
// secret fails signature verification.
func TestGetUserFromTokenRejectsWrongSecret(t *testing.T) {
	signer := &RestServer{jwtSecret: []byte("secret-A-aaaaaaaaaaaaaaaaaaaaaaaa")}
	verifier := &RestServer{jwtSecret: []byte("secret-B-bbbbbbbbbbbbbbbbbbbbbbbb")}
	tok, err := signer.generateToken("bob")
	if err != nil {
		t.Fatalf("generateToken: %v", err)
	}
	if _, err := verifier.getUserFromToken(tok); err == nil {
		t.Fatal("expected error for token signed with a different secret")
	}
}

// TestAuthMiddlewareBlacklist exercises the middleware outcomes: missing
// header, invalid token, valid token, blacklisted token, and a "Bearer "
// prefix sharing the raw token's blacklist entry.
func TestAuthMiddlewareBlacklist(t *testing.T) {
	etcd := serverTestEtcd(t)
	rs := &RestServer{etcd: etcd, jwtSecret: []byte("mw-test-secret-1234567890abcdef")}

	gin.SetMode(gin.TestMode)
	router := gin.New()
	router.GET("/protected", rs.AuthMiddlewareWithBlacklist(), func(c *gin.Context) {
		c.JSON(http.StatusOK, gin.H{"ok": true})
	})

	do := func(auth string) int {
		req := httptest.NewRequest(http.MethodGet, "/protected", nil)
		if auth != "" {
			req.Header.Set("Authorization", auth)
		}
		w := httptest.NewRecorder()
		router.ServeHTTP(w, req)
		return w.Code
	}

	if code := do(""); code != http.StatusUnauthorized {
		t.Errorf("missing header: got %d, want 401", code)
	}
	if code := do("garbage.token.value"); code != http.StatusUnauthorized {
		t.Errorf("invalid token: got %d, want 401", code)
	}

	tok, err := rs.generateToken("carol")
	if err != nil {
		t.Fatalf("generateToken: %v", err)
	}
	if code := do(tok); code != http.StatusOK {
		t.Errorf("valid token: got %d, want 200", code)
	}

	// Blacklist the token, then it must be rejected.
	ctx := context.Background()
	blacklistKey := fmt.Sprintf("token_blacklist/%s", tok)
	if _, err := etcd.Client().Put(ctx, blacklistKey, "revoked"); err != nil {
		t.Fatalf("blacklist put: %v", err)
	}
	t.Cleanup(func() { etcd.Client().Delete(ctx, blacklistKey) })
	if code := do(tok); code != http.StatusUnauthorized {
		t.Errorf("blacklisted token: got %d, want 401", code)
	}

	// A "Bearer " prefix is accepted and shares the raw token's blacklist entry.
	bearerTok, err := rs.generateToken("dave")
	if err != nil {
		t.Fatalf("generateToken: %v", err)
	}
	if code := do("Bearer " + bearerTok); code != http.StatusOK {
		t.Errorf("Bearer token: got %d, want 200", code)
	}
	if code := do("bearer " + bearerTok); code != http.StatusOK {
		t.Errorf("lowercase bearer token: got %d, want 200", code)
	}
	if code := do("Bearer " + tok); code != http.StatusUnauthorized {
		t.Errorf("raw-blacklisted token sent with Bearer: got %d, want 401", code)
	}

	// Logout with a "Bearer " prefix revokes both spellings of the token.
	logoutRouter := gin.New()
	logoutRouter.POST("/logout", rs.Logout)
	logoutReq := httptest.NewRequest(http.MethodPost, "/logout", nil)
	logoutReq.Header.Set("Authorization", "Bearer "+bearerTok)
	logoutResp := httptest.NewRecorder()
	logoutRouter.ServeHTTP(logoutResp, logoutReq)
	t.Cleanup(func() { etcd.Client().Delete(ctx, fmt.Sprintf("token_blacklist/%s", bearerTok)) })
	if logoutResp.Code != http.StatusOK {
		t.Fatalf("logout with Bearer: got %d (%s), want 200", logoutResp.Code, logoutResp.Body.String())
	}
	if code := do(bearerTok); code != http.StatusUnauthorized {
		t.Errorf("raw token after Bearer logout: got %d, want 401", code)
	}
	if code := do("Bearer " + bearerTok); code != http.StatusUnauthorized {
		t.Errorf("Bearer token after Bearer logout: got %d, want 401", code)
	}
}

// TestBearerToken: the "Bearer " scheme is stripped case-insensitively and
// anything else is returned unchanged.
func TestBearerToken(t *testing.T) {
	cases := []struct {
		header string
		want   string
	}{
		{"abc.def.ghi", "abc.def.ghi"},
		{"Bearer abc.def.ghi", "abc.def.ghi"},
		{"bearer abc.def.ghi", "abc.def.ghi"},
		{"BEARER abc.def.ghi", "abc.def.ghi"},
		{"Bearer   abc.def.ghi  ", "abc.def.ghi"},
		{"Bearer ", ""},
		{"Bearer", "Bearer"},
		{"Bearerabc.def.ghi", "Bearerabc.def.ghi"},
		{"Basic abc.def.ghi", "Basic abc.def.ghi"},
		{"", ""},
	}
	for _, tc := range cases {
		if got := bearerToken(tc.header); got != tc.want {
			t.Errorf("bearerToken(%q) = %q, want %q", tc.header, got, tc.want)
		}
	}
}
