package server

import (
	"net"
	"strings"
	"testing"
	"time"
)

// TestGrpcStartListenFailureReturnsError verifies that when the bind address is
// already taken, Start returns a non-nil error (mentioning the listen failure)
// instead of panicking on a nil listener inside Serve.
func TestGrpcStartListenFailureReturnsError(t *testing.T) {
	// Occupy a port so the subsequent Start(addr) bind fails.
	occupier, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("failed to reserve a port: %v", err)
	}
	defer occupier.Close()

	addr := occupier.Addr().String()

	s := &GrpcServer{}
	err = s.Start(addr, &ConfigGrpcServer{})
	if err == nil {
		t.Fatal("expected Start to return an error when the port is already in use, got nil")
	}
	if !strings.Contains(err.Error(), "listen") {
		t.Fatalf("expected error to mention listen failure, got: %v", err)
	}
}

// TestGrpcStartGracefulStopReturnsNil verifies the normal path: Start serves on
// a free port and, after Stop() (GracefulStop), returns nil and unblocks.
func TestGrpcStartGracefulStopReturnsNil(t *testing.T) {
	// Grab a free port, then release it so Start can bind it.
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("failed to reserve a port: %v", err)
	}
	addr := l.Addr().String()
	l.Close()

	s := &GrpcServer{}
	done := make(chan error, 1)
	go func() {
		done <- s.Start(addr, &ConfigGrpcServer{})
	}()

	// Wait until the server is actually accepting connections.
	if !waitForTCP(addr, 3*time.Second) {
		t.Fatalf("gRPC server did not start listening on %s in time", addr)
	}

	s.Stop()

	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("expected Start to return nil after GracefulStop, got: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Start did not return within 5s after Stop()")
	}
}

func waitForTCP(addr string, timeout time.Duration) bool {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		conn, err := net.DialTimeout("tcp", addr, 200*time.Millisecond)
		if err == nil {
			conn.Close()
			return true
		}
		time.Sleep(20 * time.Millisecond)
	}
	return false
}
