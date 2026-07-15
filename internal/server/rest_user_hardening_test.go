package server

import (
	"bytes"
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/golang-jwt/jwt/v5"
	clientv3 "go.etcd.io/etcd/client/v3"
	"golang.org/x/crypto/bcrypt"
)

func TestValidateTokenAcceptsOnlyHS256(t *testing.T) {
	secret := []byte("task-17-unit-test-secret-1234567890")
	rs := &RestServer{jwtSecret: secret}

	hs256Token, err := rs.generateToken("alice")
	if err != nil {
		t.Fatalf("generate HS256 token: %v", err)
	}
	parsed, err := rs.validateToken(hs256Token)
	if err != nil || !parsed.Valid {
		t.Fatalf("validate HS256 token: token valid = %v, err = %v", parsed != nil && parsed.Valid, err)
	}

	// Hand-crafted unsigned JWT with {"alg":"none","typ":"JWT"}.
	unsignedToken := "eyJhbGciOiJub25lIiwidHlwIjoiSldUIn0.eyJ1c2VybmFtZSI6ImFsaWNlIn0."
	if _, err := rs.validateToken(unsignedToken); err == nil {
		t.Fatal("validateToken accepted an unsigned alg=none token")
	}

	hs384 := jwt.NewWithClaims(jwt.SigningMethodHS384, jwt.MapClaims{"username": "alice"})
	hs384Token, err := hs384.SignedString(secret)
	if err != nil {
		t.Fatalf("sign HS384 token: %v", err)
	}
	if _, err := rs.validateToken(hs384Token); err == nil {
		t.Fatal("validateToken accepted an HS384 token")
	}
}

func TestDummyPasswordHashUsesDefaultCost(t *testing.T) {
	cost, err := bcrypt.Cost([]byte(dummyPasswordHash))
	if err != nil {
		t.Fatalf("dummy password hash is not valid bcrypt: %v", err)
	}
	if cost != bcrypt.DefaultCost {
		t.Fatalf("dummy password hash cost = %d, want %d", cost, bcrypt.DefaultCost)
	}
}

func TestAddUserHardening(t *testing.T) {
	etcd := serverTestEtcd(t)
	rs := &RestServer{etcd: etcd, jwtSecret: []byte("task-17-add-user-secret-1234567890")}
	gin.SetMode(gin.TestMode)
	router := rs.newRouter()

	token, err := rs.generateToken("task-17-operator")
	if err != nil {
		t.Fatalf("generate operator token: %v", err)
	}
	username := fmt.Sprintf("task17-add-%d", time.Now().UnixNano())
	key := "users/" + username
	t.Cleanup(func() {
		_, _ = etcd.Client().Delete(context.Background(), key)
	})

	emptyPassword := fmt.Sprintf(`{"username":%q,"password":""}`, username)
	w := userHardeningRequest(t, router, http.MethodPost, "/api/users", emptyPassword, token)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("empty password: got %d (%s), want 400", w.Code, w.Body.String())
	}
	resp, err := etcd.Client().Get(context.Background(), key)
	if err != nil {
		t.Fatalf("read empty-password user key: %v", err)
	}
	if len(resp.Kvs) != 0 {
		t.Fatal("empty-password request created a user")
	}

	const originalPassword = "original-password"
	createBody := fmt.Sprintf(`{"username":%q,"password":%q}`, username, originalPassword)
	w = userHardeningRequest(t, router, http.MethodPost, "/api/users", createBody, token)
	if w.Code != http.StatusOK {
		t.Fatalf("create user: got %d (%s), want 200", w.Code, w.Body.String())
	}
	before, err := etcd.Client().Get(context.Background(), key)
	if err != nil {
		t.Fatalf("read created user: %v", err)
	}
	if len(before.Kvs) != 1 {
		t.Fatalf("read created user: got %d values, want 1", len(before.Kvs))
	}

	duplicateBody := fmt.Sprintf(`{"username":%q,"password":"replacement-password"}`, username)
	w = userHardeningRequest(t, router, http.MethodPost, "/api/users", duplicateBody, token)
	if w.Code != http.StatusConflict {
		t.Fatalf("duplicate user: got %d (%s), want 409", w.Code, w.Body.String())
	}
	after, err := etcd.Client().Get(context.Background(), key)
	if err != nil {
		t.Fatalf("read duplicate user: %v", err)
	}
	if len(after.Kvs) != 1 {
		t.Fatalf("read duplicate user: got %d values, want 1", len(after.Kvs))
	}
	if !bytes.Equal(before.Kvs[0].Value, after.Kvs[0].Value) {
		t.Fatal("duplicate user request replaced the stored password hash")
	}

	loginBody := fmt.Sprintf(`{"username":%q,"password":%q}`, username, originalPassword)
	w = userHardeningRequest(t, router, http.MethodPost, "/api/login", loginBody, "")
	if w.Code != http.StatusOK {
		t.Fatalf("login with original password: got %d (%s), want 200", w.Code, w.Body.String())
	}
}

func TestDeleteUserHardening(t *testing.T) {
	etcd := serverTestEtcd(t)
	ctx := context.Background()
	countResp, err := etcd.Client().Get(ctx, "users/", clientv3.WithPrefix(), clientv3.WithCountOnly())
	if err != nil {
		t.Fatalf("count pre-existing users: %v", err)
	}
	if countResp.Count != 0 {
		t.Fatalf("delete-last-user test requires a clean throwaway etcd; found %d existing users", countResp.Count)
	}

	rs := &RestServer{etcd: etcd, jwtSecret: []byte("task-17-delete-user-secret-123456")}
	gin.SetMode(gin.TestMode)
	router := rs.newRouter()
	token, err := rs.generateToken("task-17-operator")
	if err != nil {
		t.Fatalf("generate operator token: %v", err)
	}

	username := fmt.Sprintf("task17-last-%d", time.Now().UnixNano())
	key := "users/" + username
	hash, err := bcrypt.GenerateFromPassword([]byte("last-user-password"), bcrypt.DefaultCost)
	if err != nil {
		t.Fatalf("hash last user password: %v", err)
	}
	if _, err := etcd.Client().Put(ctx, key, string(hash)); err != nil {
		t.Fatalf("seed last user: %v", err)
	}
	t.Cleanup(func() {
		_, _ = etcd.Client().Delete(context.Background(), key)
	})

	missingPath := fmt.Sprintf("/api/users/%s-missing", username)
	w := userHardeningRequest(t, router, http.MethodDelete, missingPath, "", token)
	if w.Code != http.StatusNotFound {
		t.Fatalf("delete missing user: got %d (%s), want 404", w.Code, w.Body.String())
	}

	w = userHardeningRequest(t, router, http.MethodDelete, "/api/users/"+username, "", token)
	if w.Code != http.StatusConflict {
		t.Fatalf("delete last user: got %d (%s), want 409", w.Code, w.Body.String())
	}
	resp, err := etcd.Client().Get(ctx, key)
	if err != nil {
		t.Fatalf("read last user after rejected delete: %v", err)
	}
	if len(resp.Kvs) != 1 {
		t.Fatal("last user was deleted despite the conflict response")
	}
}

func TestLoginUsesUniformInvalidCredentialsResponse(t *testing.T) {
	etcd := serverTestEtcd(t)
	rs := &RestServer{etcd: etcd, jwtSecret: []byte("task-17-login-secret-1234567890")}
	gin.SetMode(gin.TestMode)
	router := rs.newRouter()

	username := fmt.Sprintf("task17-login-%d", time.Now().UnixNano())
	key := "users/" + username
	hash, err := bcrypt.GenerateFromPassword([]byte("correct-password"), bcrypt.DefaultCost)
	if err != nil {
		t.Fatalf("hash login password: %v", err)
	}
	if _, err := etcd.Client().Put(context.Background(), key, string(hash)); err != nil {
		t.Fatalf("seed login user: %v", err)
	}
	t.Cleanup(func() {
		_, _ = etcd.Client().Delete(context.Background(), key)
	})

	wrongPassword := fmt.Sprintf(`{"username":%q,"password":"wrong-password"}`, username)
	wrong := userHardeningRequest(t, router, http.MethodPost, "/api/login", wrongPassword, "")
	missingUser := fmt.Sprintf(`{"username":%q,"password":"wrong-password"}`, username+"-missing")
	missing := userHardeningRequest(t, router, http.MethodPost, "/api/login", missingUser, "")

	if wrong.Code != http.StatusUnauthorized || missing.Code != http.StatusUnauthorized {
		t.Fatalf("invalid login statuses: wrong password = %d, missing user = %d; want both 401", wrong.Code, missing.Code)
	}
	if !bytes.Equal(wrong.Body.Bytes(), missing.Body.Bytes()) {
		t.Fatalf("invalid login bodies differ: wrong password = %q, missing user = %q", wrong.Body.Bytes(), missing.Body.Bytes())
	}
}

func userHardeningRequest(t *testing.T, router http.Handler, method, path, body, token string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(method, path, bytes.NewBufferString(body))
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	if token != "" {
		req.Header.Set("Authorization", token)
	}
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)
	return w
}
