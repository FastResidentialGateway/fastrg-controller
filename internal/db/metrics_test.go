package db

import (
	"context"
	"errors"
	"testing"

	"github.com/prometheus/client_golang/prometheus/testutil"
)

// fakeDatabase stands in for the pool: it answers the ping and the row count
// the metrics sampler asks for.
type fakeDatabase struct {
	pingErr  error
	counts   map[string]int64
	countErr error
}

func (f *fakeDatabase) Ping(context.Context) error { return f.pingErr }

func (f *fakeDatabase) CountPPPoEStatusByNode(context.Context) (map[string]int64, error) {
	return f.counts, f.countErr
}

// TestSampleMetricsReportsDatabaseUp: a reachable database reports up=1 and one
// gauge per node; an unreachable one reports up=0.
func TestSampleMetricsReportsDatabaseUp(t *testing.T) {
	pppoeStatusRows.Reset()
	t.Cleanup(func() {
		pppoeStatusRows.Reset()
		dbUp.Set(0)
	})

	reachable := &fakeDatabase{counts: map[string]int64{"node-a": 2, "node-b": 5}}
	sampleMetrics(context.Background(), reachable, reachable)
	if got := testutil.ToFloat64(dbUp); got != 1 {
		t.Fatalf("fastrg_db_up = %v, want 1", got)
	}
	if got := testutil.ToFloat64(pppoeStatusRows.WithLabelValues("node-a")); got != 2 {
		t.Fatalf("node-a rows = %v, want 2", got)
	}
	if got := testutil.ToFloat64(pppoeStatusRows.WithLabelValues("node-b")); got != 5 {
		t.Fatalf("node-b rows = %v, want 5", got)
	}

	unreachable := &fakeDatabase{pingErr: errors.New("connection refused")}
	sampleMetrics(context.Background(), unreachable, unreachable)
	if got := testutil.ToFloat64(dbUp); got != 0 {
		t.Fatalf("fastrg_db_up after failed ping = %v, want 0", got)
	}
}

// TestSampleMetricsDropsVanishedNodes: a node whose rows are gone stops being
// reported instead of keeping its last count forever.
func TestSampleMetricsDropsVanishedNodes(t *testing.T) {
	pppoeStatusRows.Reset()
	t.Cleanup(func() {
		pppoeStatusRows.Reset()
		dbUp.Set(0)
	})

	full := &fakeDatabase{counts: map[string]int64{"node-a": 2, "node-b": 5}}
	sampleMetrics(context.Background(), full, full)

	emptied := &fakeDatabase{counts: map[string]int64{"node-b": 5}}
	sampleMetrics(context.Background(), emptied, emptied)

	if got := testutil.CollectAndCount(pppoeStatusRows); got != 1 {
		t.Fatalf("reported nodes = %d, want only node-b", got)
	}
	if got := testutil.ToFloat64(pppoeStatusRows.WithLabelValues("node-b")); got != 5 {
		t.Fatalf("node-b rows = %v, want 5", got)
	}
}

// TestObserveCountsFailedOperations: only failures are counted, under the
// operation's own label.
func TestObserveCountsFailedOperations(t *testing.T) {
	const op = "test_operation"
	before := testutil.ToFloat64(dbErrorsTotal.WithLabelValues(op))

	if err := observe(op, nil); err != nil {
		t.Fatalf("observe(nil) = %v, want nil", err)
	}
	if got := testutil.ToFloat64(dbErrorsTotal.WithLabelValues(op)); got != before {
		t.Fatalf("error counter after success = %v, want %v", got, before)
	}

	wantErr := errors.New("write failed")
	if err := observe(op, wantErr); !errors.Is(err, wantErr) {
		t.Fatalf("observe returned %v, want the original error", err)
	}
	if got := testutil.ToFloat64(dbErrorsTotal.WithLabelValues(op)); got != before+1 {
		t.Fatalf("error counter after failure = %v, want %v", got, before+1)
	}
}
