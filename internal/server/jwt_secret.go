package server

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"fmt"
	"os"

	"fastrg-controller/internal/storage"

	"github.com/sirupsen/logrus"
	clientv3 "go.etcd.io/etcd/client/v3"
)

const jwtSecretEtcdKey = "auth/jwt_secret"

// ResolveJWTSecret returns the process's JWT signing secret. JWT_SECRET takes
// precedence; otherwise the first replica atomically creates a cluster-shared
// secret in etcd and every replica uses the stored value.
func ResolveJWTSecret(ctx context.Context, etcd *storage.EtcdClient) ([]byte, error) {
	if secret := os.Getenv("JWT_SECRET"); secret != "" {
		return []byte(secret), nil
	}
	if etcd == nil {
		return nil, fmt.Errorf("resolve JWT secret: etcd client is nil")
	}

	resp, err := etcd.Client().Get(ctx, jwtSecretEtcdKey)
	if err != nil {
		return nil, fmt.Errorf("read JWT secret from etcd: %w", err)
	}
	if len(resp.Kvs) > 0 {
		return append([]byte(nil), resp.Kvs[0].Value...), nil
	}

	randomBytes := make([]byte, 32)
	if _, err := rand.Read(randomBytes); err != nil {
		return nil, fmt.Errorf("generate JWT secret: %w", err)
	}
	candidate := []byte(base64.StdEncoding.EncodeToString(randomBytes))

	txnResp, err := etcd.Client().Txn(ctx).
		If(clientv3.Compare(clientv3.Version(jwtSecretEtcdKey), "=", 0)).
		Then(clientv3.OpPut(jwtSecretEtcdKey, string(candidate))).
		Commit()
	if err != nil {
		return nil, fmt.Errorf("store JWT secret in etcd: %w", err)
	}
	if txnResp.Succeeded {
		logrus.Infof("Generated cluster-shared JWT secret in etcd key %s; set JWT_SECRET explicitly in production to keep it out of etcd", jwtSecretEtcdKey)
		return candidate, nil
	}

	// Another replica won the create-if-absent transaction. Adopt its value
	// instead of overwriting it so all replicas converge on one signing key.
	resp, err = etcd.Client().Get(ctx, jwtSecretEtcdKey)
	if err != nil {
		return nil, fmt.Errorf("read concurrently created JWT secret from etcd: %w", err)
	}
	if len(resp.Kvs) == 0 {
		return nil, fmt.Errorf("JWT secret disappeared after concurrent creation")
	}
	return append([]byte(nil), resp.Kvs[0].Value...), nil
}
