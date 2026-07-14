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

	proxyAddr := reserveTCPAddress(t)
	proxyDSN := replaceDSNHost(t, baseDSN, proxyAddr)

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

	// Leave the proxy port closed long enough for the first connection attempt
	// to fail, then begin forwarding to the throwaway PostgreSQL instance.
	time.Sleep(time.Second)
	proxy := startTCPProxy(t, proxyAddr, "localhost:15432")

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
}

func reserveTCPAddress(t *testing.T) string {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("reserve proxy address: %v", err)
	}
	addr := listener.Addr().String()
	if err := listener.Close(); err != nil {
		t.Fatalf("release proxy address: %v", err)
	}
	return addr
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
