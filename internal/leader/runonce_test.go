package leader

import (
	"context"
	"os"
	"strconv"
	"testing"
	"time"

	clientv3 "go.etcd.io/etcd/client/v3"
)

// TestRunOnceAcquiresAndReleases: a lone candidate campaigns successfully
// (onElected fires), and cancelling the context releases leadership so runOnce
// returns true. Skipped without a real etcd.
func TestRunOnceAcquiresAndReleases(t *testing.T) {
	eps := os.Getenv("TEST_ETCD_ENDPOINTS")
	if eps == "" {
		t.Skip("TEST_ETCD_ENDPOINTS not set; skipping leader election test")
	}
	cli, err := clientv3.New(clientv3.Config{Endpoints: []string{eps}, DialTimeout: 5 * time.Second})
	if err != nil {
		t.Fatalf("etcd: %v", err)
	}
	defer cli.Close()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	prefix := "/test-election/" + strconv.FormatInt(time.Now().UnixNano(), 10)

	elected := make(chan struct{})
	done := make(chan bool, 1)
	go func() {
		done <- runOnce(ctx, cli, prefix, "candidate-1", func(context.Context) {
			close(elected)
		})
	}()

	select {
	case <-elected:
	case <-time.After(10 * time.Second):
		t.Fatal("never became leader")
	}

	// Ending the parent context releases leadership; runOnce returns true.
	cancel()
	select {
	case ok := <-done:
		if !ok {
			t.Fatal("runOnce returned false after being elected")
		}
	case <-time.After(10 * time.Second):
		t.Fatal("runOnce did not return after ctx cancel")
	}
}
