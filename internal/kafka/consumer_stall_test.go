package kafka

import (
	"context"
	"net"
	"os"
	"strconv"
	"testing"
	"time"

	"fastrg-controller/internal/db"
	"fastrg-controller/internal/storage"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/prometheus/client_golang/prometheus/testutil"
	"github.com/sirupsen/logrus"
)

func TestStallLogLevel(t *testing.T) {
	tests := []struct {
		name    string
		elapsed time.Duration
		want    logrus.Level
	}{
		{name: "just started", elapsed: 0, want: logrus.WarnLevel},
		{name: "below threshold", elapsed: stallErrorThreshold - time.Nanosecond, want: logrus.WarnLevel},
		{name: "at threshold", elapsed: stallErrorThreshold, want: logrus.ErrorLevel},
		{name: "above threshold", elapsed: stallErrorThreshold + time.Second, want: logrus.ErrorLevel},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := stallLogLevel(tt.elapsed); got != tt.want {
				t.Fatalf("stallLogLevel(%v) = %v, want %v", tt.elapsed, got, tt.want)
			}
		})
	}
}

func TestInfraRetryBackoffSequenceAndReset(t *testing.T) {
	backoff := newInfraRetryBackoff()
	want := []time.Duration{
		2 * time.Second,
		4 * time.Second,
		8 * time.Second,
		16 * time.Second,
		30 * time.Second,
		30 * time.Second,
	}
	for i, wantDelay := range want {
		if got := backoff.Next(); got != wantDelay {
			t.Fatalf("backoff delay %d = %v, want %v", i, got, wantDelay)
		}
	}

	backoff.Reset()
	if got := backoff.Next(); got != infraRetryInitialBackoff {
		t.Fatalf("backoff after reset = %v, want %v", got, infraRetryInitialBackoff)
	}
}

func TestConsumerStallMetrics(t *testing.T) {
	kafkaConsumerStallSeconds.Set(0)
	t.Cleanup(func() { kafkaConsumerStallSeconds.Set(0) })

	databaseBefore := testutil.ToFloat64(kafkaConsumerInfraRetriesTotal.WithLabelValues("database"))
	etcdBefore := testutil.ToFloat64(kafkaConsumerInfraRetriesTotal.WithLabelValues("etcd"))

	stall := consumerStall{}
	startedAt := time.Date(2026, time.July, 14, 0, 0, 0, 0, time.UTC)
	if elapsed := stall.retry("database", startedAt); elapsed != 0 {
		t.Fatalf("initial stall elapsed = %v, want 0", elapsed)
	}
	if got := testutil.ToFloat64(kafkaConsumerInfraRetriesTotal.WithLabelValues("database")); got != databaseBefore+1 {
		t.Fatalf("database retry counter = %v, want %v", got, databaseBefore+1)
	}

	if elapsed := stall.retry("etcd", startedAt.Add(3*time.Second)); elapsed != 3*time.Second {
		t.Fatalf("continued stall elapsed = %v, want 3s", elapsed)
	}
	if got := testutil.ToFloat64(kafkaConsumerStallSeconds); got != 3 {
		t.Fatalf("stall gauge = %v, want 3", got)
	}
	if got := testutil.ToFloat64(kafkaConsumerInfraRetriesTotal.WithLabelValues("etcd")); got != etcdBefore+1 {
		t.Fatalf("etcd retry counter = %v, want %v", got, etcdBefore+1)
	}

	stall.reset()
	if got := testutil.ToFloat64(kafkaConsumerStallSeconds); got != 0 {
		t.Fatalf("stall gauge after reset = %v, want 0", got)
	}
}

// TestConsumerStallMetricsEndToEnd verifies that a real Kafka consumer blocked
// on a real etcd client pointed at a confirmed-refused endpoint exposes a stall
// and clears it when the consumer is stopped.
func TestConsumerStallMetricsEndToEnd(t *testing.T) {
	brokers := os.Getenv("TEST_KAFKA_BROKERS")
	dsn := os.Getenv("TEST_DATABASE_URL")
	if brokers == "" || dsn == "" {
		t.Skip("TEST_KAFKA_BROKERS / TEST_DATABASE_URL not set; skipping stall metrics Kafka e2e")
	}

	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("reserve refused etcd endpoint: %v", err)
	}
	refusedEtcdEndpoint := listener.Addr().String()
	if err := listener.Close(); err != nil {
		t.Fatalf("close refused etcd endpoint listener: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	database, err := db.New(ctx, dsn)
	if err != nil {
		cancel()
		t.Fatalf("db: %v", err)
	}
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		database.Close()
		cancel()
		t.Fatalf("assert pool: %v", err)
	}

	t.Setenv("ETCD_ENDPOINTS", refusedEtcdEndpoint)
	etcd, err := storage.NewEtcdClient()
	if err != nil {
		pool.Close()
		database.Close()
		cancel()
		t.Fatalf("etcd client: %v", err)
	}

	var done chan struct{}
	t.Cleanup(func() {
		cancel()
		if done != nil {
			select {
			case <-done:
			case <-time.After(10 * time.Second):
				t.Errorf("timed out stopping Kafka consumer during cleanup")
			}
		}
		etcd.Close()
		pool.Close()
		database.Close()
		kafkaConsumerStallSeconds.Set(0)
	})

	suffix := strconv.FormatInt(time.Now().UnixNano(), 10)
	topic := "fastrg.node.events.stall-metrics." + suffix
	t.Setenv("KAFKA_TOPIC", topic)
	t.Setenv("KAFKA_GROUP", "fastrg-controller-stall-metrics."+suffix)
	event := configApplyEvent("stall-metrics-"+suffix, "711", false, time.Now().Unix())
	event.CorrelationId = "stall-metrics-" + suffix
	produceRollbackEvents(t, ctx, brokers, topic, event)

	kafkaConsumerStallSeconds.Set(0)
	etcdRetriesBefore := testutil.ToFloat64(kafkaConsumerInfraRetriesTotal.WithLabelValues("etcd"))

	done = make(chan struct{})
	consumer := NewConsumer([]string{brokers}, database, etcd)
	go func() {
		defer close(done)
		consumer.Run(ctx)
	}()

	waitFor(t, 25*time.Second, func() bool {
		return testutil.ToFloat64(kafkaConsumerStallSeconds) > 0 &&
			testutil.ToFloat64(kafkaConsumerInfraRetriesTotal.WithLabelValues("etcd")) > etcdRetriesBefore
	}, "stall gauge and etcd retry counter to increase")

	cancel()
	select {
	case <-done:
		done = nil
	case <-time.After(10 * time.Second):
		t.Fatal("timed out stopping Kafka consumer")
	}
	if got := testutil.ToFloat64(kafkaConsumerStallSeconds); got != 0 {
		t.Fatalf("stall gauge after consumer stop = %v, want 0", got)
	}
}
