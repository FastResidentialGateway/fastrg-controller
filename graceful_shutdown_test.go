package main

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"fmt"
	"math/big"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"
)

// TestGracefulShutdown is a black-box test: it builds the controller binary,
// launches it against a throwaway etcd (TEST_ETCD_ENDPOINTS), waits for the
// HTTPS health endpoint, sends SIGTERM, and asserts the process performs an
// ordered shutdown — exiting within the deadline with code 0 and logging
// "shutdown complete". Skipped when TEST_ETCD_ENDPOINTS is unset so `go test`
// stays green without an external etcd.
func TestGracefulShutdown(t *testing.T) {
	etcdEndpoints := os.Getenv("TEST_ETCD_ENDPOINTS")
	if etcdEndpoints == "" {
		t.Skip("TEST_ETCD_ENDPOINTS not set; skipping graceful shutdown e2e")
	}

	tmp := t.TempDir()

	// Build the controller binary.
	bin := filepath.Join(tmp, "controller")
	build := exec.Command("go", "build", "-o", bin, ".")
	build.Env = os.Environ()
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("go build failed: %v\n%s", err, out)
	}

	// Generate a self-signed cert/key pair into the temp dir.
	certFile := filepath.Join(tmp, "server.crt")
	keyFile := filepath.Join(tmp, "server.key")
	writeSelfSignedCert(t, certFile, keyFile)

	httpsPort := freePort(t)
	httpPort := freePort(t)
	grpcPort := freePort(t)
	logPort := freePort(t)

	cmd := exec.Command(bin)
	cmd.Env = append(os.Environ(),
		"ETCD_ENDPOINTS="+etcdEndpoints,
		"HTTPS_PORT="+httpsPort,
		"HTTP_REDIRECT_PORT="+httpPort,
		"GRPC_PORT="+grpcPort,
		"LOG_HTTPS_PORT="+logPort,
		"CERT_FILE="+certFile,
		"KEY_FILE="+keyFile,
		// Ensure no PostgreSQL/Kafka subsystems start (etcd-only mode).
		"DATABASE_URL=",
		"POSTGRES_HOST=",
		"KAFKA_BROKERS=",
	)

	// Capture stderr (logs are mirrored there) so we can assert on log lines.
	var logBuf syncBuffer
	cmd.Stdout = &logBuf
	cmd.Stderr = &logBuf

	if err := cmd.Start(); err != nil {
		t.Fatalf("failed to start controller: %v", err)
	}
	// Safety net: kill if the test fails before we send SIGTERM.
	started := true
	defer func() {
		if started && cmd.Process != nil {
			_ = cmd.Process.Kill()
		}
	}()

	client := &http.Client{
		Timeout: 2 * time.Second,
		Transport: &http.Transport{
			TLSClientConfig: &tls.Config{InsecureSkipVerify: true},
		},
	}
	healthURL := fmt.Sprintf("https://127.0.0.1:%s/api/health", httpsPort)

	if !waitForHealth(t, client, healthURL, 30*time.Second) {
		t.Fatalf("controller did not become healthy in time; logs:\n%s", logBuf.String())
	}

	// Send SIGTERM and wait for the process to exit.
	if err := cmd.Process.Signal(syscall.SIGTERM); err != nil {
		t.Fatalf("failed to send SIGTERM: %v", err)
	}

	waitErr := make(chan error, 1)
	go func() { waitErr <- cmd.Wait() }()

	select {
	case err := <-waitErr:
		started = false
		if err != nil {
			t.Fatalf("controller exited with error (expected clean exit): %v\nlogs:\n%s", err, logBuf.String())
		}
	case <-time.After(10 * time.Second):
		t.Fatalf("controller did not exit within 10s of SIGTERM; logs:\n%s", logBuf.String())
	}

	if !strings.Contains(logBuf.String(), "shutdown complete") {
		t.Fatalf("expected 'shutdown complete' in logs; got:\n%s", logBuf.String())
	}
}

func waitForHealth(t *testing.T, client *http.Client, url string, timeout time.Duration) bool {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		resp, err := client.Get(url)
		if err == nil {
			_ = resp.Body.Close()
			if resp.StatusCode == http.StatusOK {
				return true
			}
		}
		time.Sleep(200 * time.Millisecond)
	}
	return false
}

func freePort(t *testing.T) string {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("failed to reserve free port: %v", err)
	}
	defer l.Close()
	_, port, err := net.SplitHostPort(l.Addr().String())
	if err != nil {
		t.Fatalf("failed to parse port: %v", err)
	}
	return port
}

func writeSelfSignedCert(t *testing.T, certFile, keyFile string) {
	t.Helper()
	priv, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("failed to generate key: %v", err)
	}
	tmpl := x509.Certificate{
		SerialNumber: big.NewInt(1),
		Subject:      pkix.Name{CommonName: "localhost"},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(24 * time.Hour),
		KeyUsage:     x509.KeyUsageDigitalSignature | x509.KeyUsageKeyEncipherment,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		DNSNames:     []string{"localhost"},
		IPAddresses:  []net.IP{net.ParseIP("127.0.0.1")},
	}
	der, err := x509.CreateCertificate(rand.Reader, &tmpl, &tmpl, &priv.PublicKey, priv)
	if err != nil {
		t.Fatalf("failed to create certificate: %v", err)
	}
	certOut, err := os.Create(certFile)
	if err != nil {
		t.Fatalf("failed to create cert file: %v", err)
	}
	if err := pem.Encode(certOut, &pem.Block{Type: "CERTIFICATE", Bytes: der}); err != nil {
		t.Fatalf("failed to encode cert: %v", err)
	}
	certOut.Close()

	keyBytes, err := x509.MarshalECPrivateKey(priv)
	if err != nil {
		t.Fatalf("failed to marshal key: %v", err)
	}
	keyOut, err := os.Create(keyFile)
	if err != nil {
		t.Fatalf("failed to create key file: %v", err)
	}
	if err := pem.Encode(keyOut, &pem.Block{Type: "EC PRIVATE KEY", Bytes: keyBytes}); err != nil {
		t.Fatalf("failed to encode key: %v", err)
	}
	keyOut.Close()
}

// syncBuffer is a goroutine-safe buffer for capturing child process output.
type syncBuffer struct {
	mu  sync.Mutex
	buf []byte
}

func (b *syncBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.buf = append(b.buf, p...)
	return len(p), nil
}

func (b *syncBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return string(b.buf)
}
