package server

import (
	"context"
	"encoding/base64"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"

	"fastrg-controller/internal/storage"

	"github.com/gin-gonic/gin"
)

func resetJWTSecret(t *testing.T, etcd *storage.EtcdClient) {
	t.Helper()
	ctx := context.Background()
	if _, err := etcd.Client().Delete(ctx, jwtSecretEtcdKey); err != nil {
		t.Fatalf("delete JWT secret before test: %v", err)
	}
	t.Cleanup(func() {
		if _, err := etcd.Client().Delete(context.Background(), jwtSecretEtcdKey); err != nil {
			t.Errorf("delete JWT secret after test: %v", err)
		}
	})
}

func TestResolveJWTSecretPrefersEnvironment(t *testing.T) {
	etcd := serverTestEtcd(t)
	resetJWTSecret(t, etcd)
	t.Setenv("JWT_SECRET", "environment-secret")

	secret, err := ResolveJWTSecret(context.Background(), etcd)
	if err != nil {
		t.Fatalf("ResolveJWTSecret: %v", err)
	}
	if got := string(secret); got != "environment-secret" {
		t.Fatalf("secret = %q, want environment-secret", got)
	}

	resp, err := etcd.Client().Get(context.Background(), jwtSecretEtcdKey)
	if err != nil {
		t.Fatalf("get JWT secret: %v", err)
	}
	if len(resp.Kvs) != 0 {
		t.Fatalf("JWT secret key was written despite JWT_SECRET being set")
	}
}

func TestResolveJWTSecretPersistsAcrossReplicas(t *testing.T) {
	etcd := serverTestEtcd(t)
	resetJWTSecret(t, etcd)
	t.Setenv("JWT_SECRET", "")

	first, err := ResolveJWTSecret(context.Background(), etcd)
	if err != nil {
		t.Fatalf("first ResolveJWTSecret: %v", err)
	}
	second, err := ResolveJWTSecret(context.Background(), etcd)
	if err != nil {
		t.Fatalf("second ResolveJWTSecret: %v", err)
	}
	if string(first) != string(second) {
		t.Fatal("replicas resolved different secrets")
	}

	raw, err := base64.StdEncoding.DecodeString(string(first))
	if err != nil {
		t.Fatalf("stored secret is not base64: %v", err)
	}
	if len(raw) != 32 {
		t.Fatalf("decoded secret length = %d, want 32", len(raw))
	}
	resp, err := etcd.Client().Get(context.Background(), jwtSecretEtcdKey)
	if err != nil {
		t.Fatalf("get stored JWT secret: %v", err)
	}
	if len(resp.Kvs) != 1 || string(resp.Kvs[0].Value) != string(first) {
		t.Fatalf("stored JWT secret does not match resolved value")
	}
	if resp.Kvs[0].Version != 1 {
		t.Fatalf("stored JWT secret version = %d, want 1 create", resp.Kvs[0].Version)
	}
}

func TestResolveJWTSecretConcurrentReplicasConverge(t *testing.T) {
	etcd := serverTestEtcd(t)
	resetJWTSecret(t, etcd)
	t.Setenv("JWT_SECRET", "")

	const replicas = 8
	secrets := make([][]byte, replicas)
	errs := make([]error, replicas)
	start := make(chan struct{})
	var wg sync.WaitGroup
	for i := range replicas {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			secrets[i], errs[i] = ResolveJWTSecret(context.Background(), etcd)
		}()
	}
	close(start)
	wg.Wait()

	for i, err := range errs {
		if err != nil {
			t.Fatalf("replica %d ResolveJWTSecret: %v", i, err)
		}
		if string(secrets[i]) != string(secrets[0]) {
			t.Fatalf("replica %d resolved a different secret", i)
		}
	}
	resp, err := etcd.Client().Get(context.Background(), jwtSecretEtcdKey)
	if err != nil {
		t.Fatalf("get converged JWT secret: %v", err)
	}
	if len(resp.Kvs) != 1 || string(resp.Kvs[0].Value) != string(secrets[0]) {
		t.Fatalf("etcd value does not match the converged secret")
	}
	if resp.Kvs[0].Version != 1 {
		t.Fatalf("converged JWT secret version = %d, want exactly one create", resp.Kvs[0].Version)
	}
}

func TestResolvedJWTSecretWorksAcrossReplicas(t *testing.T) {
	etcd := serverTestEtcd(t)
	resetJWTSecret(t, etcd)
	t.Setenv("JWT_SECRET", "")

	secretA, err := ResolveJWTSecret(context.Background(), etcd)
	if err != nil {
		t.Fatalf("replica A ResolveJWTSecret: %v", err)
	}
	secretB, err := ResolveJWTSecret(context.Background(), etcd)
	if err != nil {
		t.Fatalf("replica B ResolveJWTSecret: %v", err)
	}
	replicaA := NewRestServer(etcd, nil, nil, secretA)
	replicaB := NewRestServer(etcd, nil, nil, secretB)
	token, err := replicaA.generateToken("cross-replica-user")
	if err != nil {
		t.Fatalf("replica A generateToken: %v", err)
	}

	gin.SetMode(gin.TestMode)
	router := gin.New()
	router.GET("/protected", replicaB.AuthMiddlewareWithBlacklist(), func(c *gin.Context) {
		c.Status(http.StatusOK)
	})
	req := httptest.NewRequest(http.MethodGet, "/protected", nil)
	req.Header.Set("Authorization", token)
	response := httptest.NewRecorder()
	router.ServeHTTP(response, req)
	if response.Code != http.StatusOK {
		t.Fatalf("replica B rejected replica A token: status = %d, body = %s", response.Code, response.Body.String())
	}
}
