package db

import (
	"context"
	"io"
	"net"
	"net/url"
	"os"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestConnectLoopRecoversWhenPostgreSQLBecomesReachable(t *testing.T) {
	baseDSN := os.Getenv("TEST_DATABASE_URL")
	if baseDSN == "" {
		t.Skip("TEST_DATABASE_URL not set; skipping PostgreSQL reconnect integration test")
	}

	rejector := startRejectingTCPServer(t)
	proxyAddr := rejector.listener.Addr().String()
	proxyDSN := replaceDSNHost(t, baseDSN, proxyAddr)
	proxyBackend := postgresBackendFromDSN(t, baseDSN)

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	ready := make(chan *DB, 2)
	var readyCalls atomic.Int32
	go func() {
		defer close(done)
		ConnectLoop(ctx, proxyDSN, func(database *DB) {
			readyCalls.Add(1)
			queryCtx, queryCancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer queryCancel()
			var one int
			if err := database.pool.QueryRow(queryCtx, "SELECT 1").Scan(&one); err != nil {
				t.Errorf("query through recovered connection: %v", err)
			} else if one != 1 {
				t.Errorf("SELECT 1 = %d, want 1", one)
			}
			ready <- database
		})
	}()

	select {
	case <-rejector.firstAccept:
	case <-time.After(10 * time.Second):
		t.Fatal("ConnectLoop did not attempt a connection to the rejecting server")
	}
	rejector.Close()
	proxy := startTCPProxy(t, proxyAddr, proxyBackend)

	select {
	case database := <-ready:
		if database == nil {
			t.Fatal("onReady received a nil database")
		}
	case <-time.After(30 * time.Second):
		t.Fatal("onReady was not called after the proxy became reachable")
	}

	select {
	case <-done:
		t.Fatal("ConnectLoop returned before its context was cancelled")
	default:
	}

	cancel()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("ConnectLoop did not stop after context cancellation")
	}
	proxy.Close()

	if got := readyCalls.Load(); got != 1 {
		t.Fatalf("onReady called %d times, want exactly once", got)
	}
	if got := rejector.accepts.Load(); got < 1 {
		t.Fatalf("failed connection attempts = %d, want at least one", got)
	}
}

func TestPostgresBackendFromDSN(t *testing.T) {
	tests := []struct {
		name string
		dsn  string
		want string
	}{
		{
			name: "explicit port",
			dsn:  "postgres://fastrg:fastrg@localhost:15432/fastrg?sslmode=disable",
			want: "localhost:15432",
		},
		{
			name: "default PostgreSQL port",
			dsn:  "postgresql://fastrg:fastrg@postgres/fastrg?sslmode=disable",
			want: "postgres:5432",
		},
		{
			name: "IPv6 default PostgreSQL port",
			dsn:  "postgres://fastrg:fastrg@[::1]/fastrg?sslmode=disable",
			want: "[::1]:5432",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := postgresBackendFromDSN(t, tt.dsn); got != tt.want {
				t.Fatalf("postgresBackendFromDSN() = %q, want %q", got, tt.want)
			}
		})
	}
}

type rejectingTCPServer struct {
	listener    net.Listener
	firstAccept chan struct{}
	closed      chan struct{}
	accepts     atomic.Int32
	once        sync.Once
	wg          sync.WaitGroup
}

func startRejectingTCPServer(t *testing.T) *rejectingTCPServer {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("start rejecting TCP server: %v", err)
	}

	server := &rejectingTCPServer{
		listener:    listener,
		firstAccept: make(chan struct{}),
		closed:      make(chan struct{}),
	}
	server.wg.Add(1)
	go server.acceptLoop(t)
	t.Cleanup(server.Close)
	return server
}

func (s *rejectingTCPServer) acceptLoop(t *testing.T) {
	defer s.wg.Done()
	for {
		conn, err := s.listener.Accept()
		if err != nil {
			select {
			case <-s.closed:
				return
			default:
				t.Errorf("rejecting server accept: %v", err)
				return
			}
		}
		_ = conn.Close()
		if s.accepts.Add(1) == 1 {
			close(s.firstAccept)
		}
	}
}

func (s *rejectingTCPServer) Close() {
	s.once.Do(func() {
		close(s.closed)
		_ = s.listener.Close()
		s.wg.Wait()
	})
}

func replaceDSNHost(t *testing.T, dsn, host string) string {
	t.Helper()
	parsed, err := url.Parse(dsn)
	if err != nil {
		t.Fatalf("parse TEST_DATABASE_URL: %v", err)
	}
	if parsed.Scheme != "postgres" && parsed.Scheme != "postgresql" {
		t.Fatalf("TEST_DATABASE_URL must be a PostgreSQL URL, got scheme %q", parsed.Scheme)
	}
	parsed.Host = host
	return parsed.String()
}

func postgresBackendFromDSN(t *testing.T, dsn string) string {
	t.Helper()
	parsed, err := url.Parse(dsn)
	if err != nil {
		t.Fatalf("parse TEST_DATABASE_URL backend: %v", err)
	}
	if parsed.Scheme != "postgres" && parsed.Scheme != "postgresql" {
		t.Fatalf("TEST_DATABASE_URL must be a PostgreSQL URL, got scheme %q", parsed.Scheme)
	}
	host := parsed.Hostname()
	if host == "" {
		t.Fatal("TEST_DATABASE_URL must include a PostgreSQL hostname")
	}
	port := parsed.Port()
	if port == "" {
		port = "5432"
	}
	return net.JoinHostPort(host, port)
}

type tcpProxy struct {
	listener net.Listener
	backend  string
	closed   chan struct{}
	once     sync.Once
	wg       sync.WaitGroup
}

func startTCPProxy(t *testing.T, addr, backend string) *tcpProxy {
	t.Helper()
	listener, err := net.Listen("tcp", addr)
	if err != nil {
		t.Fatalf("start delayed TCP proxy: %v", err)
	}
	p := &tcpProxy{
		listener: listener,
		backend:  backend,
		closed:   make(chan struct{}),
	}
	p.wg.Add(1)
	go p.acceptLoop(t)
	t.Cleanup(p.Close)
	return p
}

func (p *tcpProxy) acceptLoop(t *testing.T) {
	defer p.wg.Done()
	for {
		client, err := p.listener.Accept()
		if err != nil {
			select {
			case <-p.closed:
				return
			default:
				t.Errorf("proxy accept: %v", err)
				return
			}
		}
		p.wg.Add(1)
		go p.forward(t, client)
	}
}

func (p *tcpProxy) forward(t *testing.T, client net.Conn) {
	defer p.wg.Done()
	backend, err := net.DialTimeout("tcp", p.backend, 5*time.Second)
	if err != nil {
		_ = client.Close()
		t.Errorf("proxy connect to %s: %v", p.backend, err)
		return
	}

	done := make(chan struct{}, 2)
	copyConn := func(dst, src net.Conn) {
		_, _ = io.Copy(dst, src)
		done <- struct{}{}
	}
	go copyConn(backend, client)
	go copyConn(client, backend)
	<-done
	_ = client.Close()
	_ = backend.Close()
	<-done
}

func (p *tcpProxy) Close() {
	p.once.Do(func() {
		close(p.closed)
		_ = p.listener.Close()
		p.wg.Wait()
	})
}
