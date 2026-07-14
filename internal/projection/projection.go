// Package projection watches the etcd configs/ prefix and appends config
// changes to hsi_config_history as an audit log while maintaining a watch
// checkpoint. The Kafka consumer manages hsi_config_current from node-confirmed
// apply results. REST and gRPC handlers write config only to etcd.
package projection

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"time"

	"fastrg-controller/internal/db"
	"fastrg-controller/internal/storage"

	"github.com/sirupsen/logrus"
)

const watcherName = "configs"

// retryBackoff is how long Run waits before restarting after a transient watch
// or DB error.
const retryBackoff = 2 * time.Second

// Projection wires the etcd config watch to the PostgreSQL projection tables.
type Projection struct {
	etcd *storage.EtcdClient
	db   *db.DB
}

func New(etcd *storage.EtcdClient, database *db.DB) *Projection {
	return &Projection{etcd: etcd, db: database}
}

// hsiEnvelope is the subset of the stored HSI config value the projection needs
// as queryable columns; the full value is also stored verbatim as JSONB.
type hsiEnvelope struct {
	Config struct {
		DesireStatus string `json:"desire_status"`
	} `json:"config"`
	Metadata struct {
		ResourceVersion string `json:"resourceVersion"`
		UpdatedBy       string `json:"updatedBy"`
		UpdatedAt       string `json:"updatedAt"`
	} `json:"metadata"`
}

// Run blocks, projecting config changes until ctx is cancelled. It performs an
// initial reconcile (fresh start) or resumes from the stored checkpoint, and
// re-reconciles on etcd compaction.
func (p *Projection) Run(ctx context.Context) {
	for ctx.Err() == nil {
		rev, ok, err := p.db.GetWatchProgress(ctx, watcherName)
		if err != nil {
			logrus.WithError(err).Error("projection: failed to read watch progress")
			p.sleep(ctx)
			continue
		}

		// Fresh start: full reconcile to seed the tables before watching.
		if !ok {
			if rev, err = p.reconcile(ctx); err != nil {
				logrus.WithError(err).Error("projection: initial reconcile failed")
				p.sleep(ctx)
				continue
			}
		}

		err = p.etcd.WatchConfigs(ctx, rev, p.handleEvent)
		switch {
		case ctx.Err() != nil:
			return
		case errors.Is(err, storage.ErrCompacted):
			logrus.Warn("projection: watch revision compacted, re-listing and reconciling")
			if _, err = p.reconcile(ctx); err != nil {
				logrus.WithError(err).Error("projection: reconcile after compaction failed")
				p.sleep(ctx)
			}
		case err != nil:
			logrus.WithError(err).Error("projection: watch stopped, retrying")
			p.sleep(ctx)
		}
	}
}

// reconcile lists every config in etcd, upserts current state, removes
// current-state rows that no longer exist, and advances the checkpoint to the
// list revision. It does not append history (history captures the live event
// stream); upserts are idempotent via the mod_revision guard. Returns the
// revision the watch should resume from.
func (p *Projection) reconcile(ctx context.Context) (int64, error) {
	events, rev, err := p.etcd.ListConfigs(ctx)
	if err != nil {
		return 0, err
	}

	// Reconcile: list all etcd configs and record them in history (but NOT in current).
	// hsi_config_current is now managed only by CONFIG_APPLY_OK handlers, so projection
	// doesn't touch it. This ensures hsi_config_current only holds "node-confirmed-success" configs.
	for _, ev := range events {
		node, user, ok := parseHSIKey(ev.Key)
		if !ok {
			continue
		}
		row := buildRow(node, user, ev.Value, ev.ModRevision)
		row.Action = db.ActionUpsert
		if err := p.db.AppendHistory(ctx, row); err != nil {
			return 0, err
		}
	}

	if err := p.db.SetWatchProgress(ctx, watcherName, rev); err != nil {
		return 0, err
	}
	logrus.Infof("projection: reconciled %d configs at revision %d", len(events), rev)
	return rev, nil
}

// handleEvent appends one live watch event to history and advances the
// checkpoint. Duplicate history events are ignored by the idempotency key.
func (p *Projection) handleEvent(ctx context.Context, ev storage.ConfigEvent) error {
	node, user, ok := parseHSIKey(ev.Key)
	if !ok {
		// Non-HSI config key (dns, user_count): advance checkpoint, skip.
		return p.db.SetWatchProgress(ctx, watcherName, ev.ModRevision)
	}

	// handleEvent only appends to history. hsi_config_current is now managed
	// exclusively by CONFIG_APPLY_OK handlers in kafka/consumer.go
	if ev.IsDelete {
		if err := p.db.AppendHistory(ctx, db.HSIConfigRow{
			NodeUUID:    node,
			UserID:      user,
			Action:      db.ActionDelete,
			ModRevision: ev.ModRevision,
		}); err != nil {
			return err
		}
		return p.db.SetWatchProgress(ctx, watcherName, ev.ModRevision)
	}

	row := buildRow(node, user, ev.Value, ev.ModRevision)
	row.Action = db.ActionUpsert
	if err := p.db.AppendHistory(ctx, row); err != nil {
		return err
	}
	return p.db.SetWatchProgress(ctx, watcherName, ev.ModRevision)
}

func (p *Projection) sleep(ctx context.Context) {
	select {
	case <-ctx.Done():
	case <-time.After(retryBackoff):
	}
}

// buildRow extracts the queryable columns from an HSI config value and keeps the
// full value as JSONB.
func buildRow(node, user string, value []byte, modRevision int64) db.HSIConfigRow {
	// Guard: an empty or non-JSON value would cause PostgreSQL JSONB to reject
	// the row ("invalid input syntax for type json"), breaking the entire watch
	// loop. Store null instead so the projection can advance past the bad key.
	configJSON := value
	if len(value) == 0 || !json.Valid(value) {
		configJSON = []byte("null")
	}
	row := db.HSIConfigRow{
		NodeUUID:     node,
		UserID:       user,
		ConfigJSON:   configJSON,
		DesireStatus: "disconnect",
		ModRevision:  modRevision,
	}

	var env hsiEnvelope
	if err := json.Unmarshal(value, &env); err == nil {
		if env.Config.DesireStatus != "" {
			row.DesireStatus = env.Config.DesireStatus
		}
		row.ResourceVersion = env.Metadata.ResourceVersion
		row.UpdatedBy = env.Metadata.UpdatedBy
		if t, err := time.Parse(time.RFC3339, env.Metadata.UpdatedAt); err == nil {
			row.UpdatedAt = &t
		}
	}
	return row
}

// parseHSIKey matches configs/{node_uuid}/hsi/{user_id} and returns its node
// and user ids. Other configs/ keys (dns, user_count) return ok=false.
func parseHSIKey(key string) (node, user string, ok bool) {
	parts := strings.Split(key, "/")
	if len(parts) != 4 || parts[0] != "configs" || parts[2] != "hsi" {
		return "", "", false
	}
	return parts[1], parts[3], true
}
