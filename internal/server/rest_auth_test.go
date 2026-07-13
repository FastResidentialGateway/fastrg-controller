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

// TestAuthMiddlewareBlacklist exercises the four middleware outcomes: missing
// header, invalid token, valid token, and blacklisted token.
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
}
