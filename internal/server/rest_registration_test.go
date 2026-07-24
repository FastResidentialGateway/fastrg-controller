package server

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"golang.org/x/crypto/bcrypt"
)

// TestOperatorProvisionedUserFlow verifies that public registration is absent
// while authenticated operators can still provision accounts that can log in.
func TestOperatorProvisionedUserFlow(t *testing.T) {
	etcd := serverTestEtcd(t)
	rs := &RestServer{etcd: etcd, jwtSecret: []byte("operator-test-secret-1234567890abcdef")}

	gin.SetMode(gin.TestMode)
	router := rs.newRouter()

	suffix := time.Now().UnixNano()
	operator := fmt.Sprintf("reg-operator-%d", suffix)
	newUser := fmt.Sprintf("reg-user-%d", suffix)
	const operatorPassword = "operator-password"
	const newUserPassword = "new-user-password"

	ctx := context.Background()
	operatorKey := "users/" + operator
	newUserKey := "users/" + newUser
	t.Cleanup(func() {
		_, _ = etcd.Client().Delete(ctx, operatorKey)
		_, _ = etcd.Client().Delete(ctx, newUserKey)
	})

	hash, err := bcrypt.GenerateFromPassword([]byte(operatorPassword), bcrypt.DefaultCost)
	if err != nil {
		t.Fatalf("hash operator password: %v", err)
	}
	if _, err := etcd.Client().Put(ctx, operatorKey, string(hash)); err != nil {
		t.Fatalf("seed operator: %v", err)
	}

	do := func(path, body, token string) *httptest.ResponseRecorder {
		t.Helper()
		req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		if token != "" {
			req.Header.Set("Authorization", token)
		}
		w := httptest.NewRecorder()
		router.ServeHTTP(w, req)
		return w
	}

	newUserBody := fmt.Sprintf(`{"username":%q,"password":%q}`, newUser, newUserPassword)
	if w := do("/api/register", newUserBody, ""); w.Code != http.StatusNotFound {
		t.Fatalf("public register: got %d (%s), want 404", w.Code, w.Body.String())
	}
	if w := do("/api/users", newUserBody, ""); w.Code != http.StatusUnauthorized {
		t.Fatalf("unauthenticated add user: got %d (%s), want 401", w.Code, w.Body.String())
	}

	operatorLogin := fmt.Sprintf(`{"username":%q,"password":%q}`, operator, operatorPassword)
	w := do("/api/login", operatorLogin, "")
	if w.Code != http.StatusOK {
		t.Fatalf("operator login: got %d (%s), want 200", w.Code, w.Body.String())
	}
	var operatorSession LoginResponse
	if err := json.Unmarshal(w.Body.Bytes(), &operatorSession); err != nil {
		t.Fatalf("decode operator login response: %v", err)
	}
	if operatorSession.Token == "" {
		t.Fatal("operator login returned an empty token")
	}

	if w := do("/api/users", newUserBody, operatorSession.Token); w.Code != http.StatusOK {
		t.Fatalf("authenticated add user: got %d (%s), want 200", w.Code, w.Body.String())
	}

	newUserLogin := fmt.Sprintf(`{"username":%q,"password":%q}`, newUser, newUserPassword)
	w = do("/api/login", newUserLogin, "")
	if w.Code != http.StatusOK {
		t.Fatalf("new user login: got %d (%s), want 200", w.Code, w.Body.String())
	}
	var newUserSession LoginResponse
	if err := json.Unmarshal(w.Body.Bytes(), &newUserSession); err != nil {
		t.Fatalf("decode new user login response: %v", err)
	}
	if newUserSession.Token == "" {
		t.Fatal("new user login returned an empty token")
	}
}
