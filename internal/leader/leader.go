// Package leader provides single-leader election for the controller's singleton
// background workers (the etcd->PostgreSQL projection, stale-node eviction, and
// per-node stats scraping). With more than one controller replica, every replica
// serves REST/gRPC and runs the Kafka consumer (one consumer group balances
// partitions), but only the elected leader runs the singleton workers — otherwise
// they would double-write the projection tables.
//
// Election uses etcd's concurrency API rather than a Kubernetes Lease: the
// controller already depends on etcd and cannot run without it, so this needs no
// extra dependency, no RBAC, and behaves identically in-cluster and locally. A
// single instance simply wins immediately.
package leader

import (
	"context"
	"os"
	"time"

	"github.com/sirupsen/logrus"
	clientv3 "go.etcd.io/etcd/client/v3"
	"go.etcd.io/etcd/client/v3/concurrency"
)

const (
	// sessionTTL is the etcd lease TTL backing the election. On ungraceful exit
	// (crash, network partition) leadership is released after at most this long.
	sessionTTL = 15 // seconds
	// retryBackoff is how long Run waits before retrying after a transient
	// session/campaign error.
	retryBackoff = 3 * time.Second
)

// Identity returns a stable identity for this process used as the election
// value: the pod name when running in Kubernetes, otherwise the hostname.
func Identity() string {
	if n := os.Getenv("POD_NAME"); n != "" {
		return n
	}
	if h, err := os.Hostname(); err == nil && h != "" {
		return h
	}
	return "unknown"
}

// Run campaigns for leadership on the given etcd prefix and, each time this
// instance becomes leader, invokes onElected with a context that is cancelled
// when leadership is lost (session expiry or etcd outage). onElected should start
// the leader-only workers bound to that context and return promptly; Run then
// blocks until leadership is lost and campaigns again. Run blocks until ctx is
// cancelled.
func Run(ctx context.Context, cli *clientv3.Client, prefix string, onElected func(leaderCtx context.Context)) {
	id := Identity()
	for ctx.Err() == nil {
		if !runOnce(ctx, cli, prefix, id, onElected) {
			sleep(ctx, retryBackoff)
		}
	}
}

// runOnce performs a single session+campaign+lead cycle. It returns true if it
// led for a while (so the caller can re-campaign immediately) and false on a
// transient error (so the caller backs off).
func runOnce(ctx context.Context, cli *clientv3.Client, prefix, id string, onElected func(context.Context)) bool {
	session, err := concurrency.NewSession(cli, concurrency.WithTTL(sessionTTL), concurrency.WithContext(ctx))
	if err != nil {
		logrus.WithError(err).Warn("leader: failed to create etcd session")
		return false
	}
	defer session.Close()

	election := concurrency.NewElection(session, prefix)

	// Campaign blocks until this instance is elected or ctx is cancelled.
	if err := election.Campaign(ctx, id); err != nil {
		if ctx.Err() == nil {
			logrus.WithError(err).Warn("leader: campaign failed")
		}
		return false
	}

	logrus.Infof("leader: acquired leadership (id=%s)", id)

	leaderCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	// Leadership ends when the session lease expires or the parent ctx is done.
	go func() {
		select {
		case <-session.Done():
		case <-ctx.Done():
		}
		cancel()
	}()

	onElected(leaderCtx)
	<-leaderCtx.Done()

	logrus.Warnf("leader: lost leadership (id=%s)", id)
	// Best-effort resign so a standby can take over promptly instead of waiting
	// for the lease TTL. Uses a fresh context since leaderCtx is already done.
	resignCtx, resignCancel := context.WithTimeout(context.Background(), 5*time.Second)
	if err := election.Resign(resignCtx); err != nil {
		logrus.WithError(err).Debug("leader: resign failed (lease will expire)")
	}
	resignCancel()
	return true
}

func sleep(ctx context.Context, d time.Duration) {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
	case <-t.C:
	}
}
