package db

import (
	"context"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
	"github.com/sirupsen/logrus"
)

// metricsSampleInterval is how often the database health metrics are refreshed.
const metricsSampleInterval = 30 * time.Second

var (
	dbUp = promauto.NewGauge(prometheus.GaugeOpts{
		Name: "fastrg_db_up",
		Help: "1 when the last PostgreSQL ping succeeded, 0 when it failed.",
	})
	dbErrorsTotal = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "fastrg_db_errors_total",
		Help: "Total failed database operations, labelled by the repository operation that failed.",
	}, []string{"op"})
	pppoeStatusRows = promauto.NewGaugeVec(prometheus.GaugeOpts{
		Name: "fastrg_pppoe_status_rows",
		Help: "Rows in pppoe_status per node. Compare it with the node's subscriber count to see whether the table lost rows.",
	}, []string{"node_uuid"})
)

// observe counts a failed database operation and returns err unchanged, so call
// sites stay a single return statement.
func observe(op string, err error) error {
	if err != nil {
		dbErrorsTotal.WithLabelValues(op).Inc()
	}
	return err
}

// Ping reports whether PostgreSQL is currently reachable.
func (d *DB) Ping(ctx context.Context) error {
	return observe("ping", d.pool.Ping(ctx))
}

// pinger and statusRowCounter are the two database calls the sampler makes.
type pinger interface {
	Ping(context.Context) error
}

type statusRowCounter interface {
	CountPPPoEStatusByNode(context.Context) (map[string]int64, error)
}

// RunMetricsSampler refreshes the database health metrics every
// metricsSampleInterval until ctx is cancelled. It only observes: an emptied
// pppoe_status shows up in the metrics, and repairing it is the Kafka
// consumer's republish path, not this loop's job.
func (d *DB) RunMetricsSampler(ctx context.Context) {
	ticker := time.NewTicker(metricsSampleInterval)
	defer ticker.Stop()

	sampleMetrics(ctx, d, d)
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
		sampleMetrics(ctx, d, d)
	}
}

// sampleMetrics records one round of database health metrics.
func sampleMetrics(ctx context.Context, p pinger, counter statusRowCounter) {
	if err := p.Ping(ctx); err != nil {
		dbUp.Set(0)
		logrus.WithError(err).Warn("db: ping failed")
		return
	}
	dbUp.Set(1)

	counts, err := counter.CountPPPoEStatusByNode(ctx)
	if err != nil {
		logrus.WithError(err).Warn("db: counting pppoe_status rows failed")
		return
	}
	// Reset first so a node whose rows all disappeared stops reporting its old
	// count instead of looking healthy forever.
	pppoeStatusRows.Reset()
	for nodeUUID, rows := range counts {
		pppoeStatusRows.WithLabelValues(nodeUUID).Set(float64(rows))
	}
}
