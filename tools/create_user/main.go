package main

import (
	"context"
	"fmt"
	"os"
	"strings"
	"time"

	clientv3 "go.etcd.io/etcd/client/v3"
	"golang.org/x/crypto/bcrypt"
)

func main() {
	// Get etcd endpoints from environment variable, default to localhost:2379
	endpoints := os.Getenv("ETCD_ENDPOINTS")
	if endpoints == "" {
		endpoints = "127.0.0.1:2379"
	}

	// Support multiple endpoints separated by comma
	endpointList := strings.Split(endpoints, ",")
	for i, endpoint := range endpointList {
		endpointList[i] = strings.TrimSpace(endpoint)
	}

	cli, err := clientv3.New(clientv3.Config{Endpoints: endpointList, DialTimeout: 5 * time.Second})
	if err != nil {
		panic(err)
	}
	defer cli.Close()

	pw := "admin"
	hash, _ := bcrypt.GenerateFromPassword([]byte(pw), bcrypt.DefaultCost)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	_, err = cli.Put(ctx, "users/admin", string(hash))
	if err != nil {
		panic(err)
	}
	fmt.Println("created user admin with password 'admin'")
}
