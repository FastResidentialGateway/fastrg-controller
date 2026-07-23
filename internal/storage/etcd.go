package storage

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/sirupsen/logrus"
	clientv3 "go.etcd.io/etcd/client/v3"
)

type EtcdClient struct {
	client *clientv3.Client
}

// CAS parameters. These must stay in sync with docs/contracts/cas-convention.md
// and the C side (fastrg-node), so both implementations behave identically.
const (
	casMaxRetries     = 5
	casInitialBackoff = 50 * time.Millisecond
)

// ErrCASConflict is returned when a CAS operation still conflicts after the
// maximum number of retries.
var ErrCASConflict = errors.New("cas: exceeded max retries")

// ErrCompacted indicates the requested watch start revision has been compacted
// away by etcd. The caller should re-list the prefix and resume from the
// current store revision.
var ErrCompacted = errors.New("etcd watch revision compacted")

// ConfigEvent is one observed change under the configs/ prefix.
type ConfigEvent struct {
	Key         string
	Value       []byte // nil for a delete
	ModRevision int64
	IsDelete    bool
}

// ConfigEventHandler processes a single config event. Returning an error aborts
// the watch so the caller can back off and resume.
type ConfigEventHandler func(ctx context.Context, ev ConfigEvent) error

// ListConfigs returns every key under the configs/ prefix as put-events, plus
// the store revision the snapshot was taken at. Use that revision as the watch
// start point so no change is missed between the list and the watch.
func (e *EtcdClient) ListConfigs(ctx context.Context) ([]ConfigEvent, int64, error) {
	resp, err := e.client.Get(ctx, "configs/", clientv3.WithPrefix())
	if err != nil {
		return nil, 0, err
	}
	events := make([]ConfigEvent, 0, len(resp.Kvs))
	for _, kv := range resp.Kvs {
		events = append(events, ConfigEvent{
			Key:         string(kv.Key),
			Value:       kv.Value,
			ModRevision: kv.ModRevision,
		})
	}
	return events, resp.Header.Revision, nil
}

// WatchConfigs watches the configs/ prefix, calling handler for each event. It
// starts just after fromRev (or at the current revision when fromRev == 0).
// Returns ErrCompacted when the start revision was compacted, nil on ctx
// cancellation, or the first handler/watch error.
func (e *EtcdClient) WatchConfigs(ctx context.Context, fromRev int64, handler ConfigEventHandler) error {
	// Per-call context ensures the etcd watch is cancelled when this call returns,
	// preventing watch accumulation when the caller restarts the watch in a loop.
	watchCtx, cancel := context.WithCancel(ctx)
	defer cancel()

	opts := []clientv3.OpOption{clientv3.WithPrefix()}
	if fromRev > 0 {
		opts = append(opts, clientv3.WithRev(fromRev+1))
	}

	watchChan := e.client.Watch(watchCtx, "configs/", opts...)
	logrus.Infof("Started watching configs/ from revision %d", fromRev)

	for watchResp := range watchChan {
		if watchResp.CompactRevision != 0 {
			return ErrCompacted
		}
		if err := watchResp.Err(); err != nil {
			return err
		}

		for _, event := range watchResp.Events {
			ev := ConfigEvent{
				Key:         string(event.Kv.Key),
				ModRevision: event.Kv.ModRevision,
				IsDelete:    event.Type == clientv3.EventTypeDelete,
			}
			if !ev.IsDelete {
				ev.Value = event.Kv.Value
			}
			if err := handler(ctx, ev); err != nil {
				return err
			}
		}
	}
	return ctx.Err()
}

// CASResult is what a CASMutateFunc asks CAS to commit for a key.
type CASResult struct {
	Value  []byte // value to put when Delete is false
	Delete bool   // when true, the key is deleted instead of put
}

// CASMutateFunc inspects the current value of a key (nil when the key does not
// exist) and returns the next state to commit. Returning an error aborts the
// CAS without any write — use it for not-found / already-exists / validation
// conditions, which the caller can then map to the right HTTP status.
type CASMutateFunc func(current []byte) (CASResult, error)

// CAS performs a compare-and-swap on key, following the project CAS convention
// (docs/contracts/cas-convention.md): read value + ModRevision, run mutate,
// then commit the put/delete inside a Txn guarded by that revision. If a
// concurrent write landed in between, the Txn fails and the whole read-modify
// cycle retries with exponential backoff, up to casMaxRetries.
func (e *EtcdClient) CAS(ctx context.Context, key string, mutate CASMutateFunc) error {
	return e.CASWithRevision(ctx, key, func(current []byte, _ int64) (CASResult, error) {
		return mutate(current)
	})
}

// CASWithRevision performs the same compare-and-swap as CAS, while also
// passing the ModRevision read for the current value to mutate. A missing key
// is represented by current == nil and modRevision == 0. On a conflict both
// values are re-read before mutate is called again.
func (e *EtcdClient) CASWithRevision(
	ctx context.Context,
	key string,
	mutate func(current []byte, modRevision int64) (CASResult, error),
) error {
	backoff := casInitialBackoff
	for attempt := 0; attempt < casMaxRetries; attempt++ {
		resp, err := e.client.Get(ctx, key)
		if err != nil {
			return err
		}

		var (
			current     []byte
			modRevision int64
		)
		if len(resp.Kvs) > 0 {
			current = resp.Kvs[0].Value
			modRevision = resp.Kvs[0].ModRevision
		}

		result, err := mutate(current, modRevision)
		if err != nil {
			return err
		}

		// Guard the write on the exact revision we read: Version==0 for a key we
		// believe is absent (create), ModRevision match for an existing key.
		var guard clientv3.Cmp
		if modRevision == 0 {
			guard = clientv3.Compare(clientv3.Version(key), "=", 0)
		} else {
			guard = clientv3.Compare(clientv3.ModRevision(key), "=", modRevision)
		}

		var op clientv3.Op
		if result.Delete {
			op = clientv3.OpDelete(key)
		} else {
			op = clientv3.OpPut(key, string(result.Value))
		}

		txnResp, err := e.client.Txn(ctx).If(guard).Then(op).Commit()
		if err != nil {
			return err
		}
		if txnResp.Succeeded {
			return nil
		}

		// Conflict: someone wrote between our Get and Txn. Back off and retry.
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(backoff):
		}
		backoff *= 2
	}
	return fmt.Errorf("%w for key %s", ErrCASConflict, key)
}

func NewEtcdClient() (*EtcdClient, error) {
	// Get etcd endpoints from environment variable, default to localhost:2379
	endpoints := os.Getenv("ETCD_ENDPOINTS")
	if endpoints == "" {
		endpoints = "localhost:2379"
	}

	// Support multiple endpoints separated by comma
	endpointList := strings.Split(endpoints, ",")
	for i, endpoint := range endpointList {
		endpointList[i] = strings.TrimSpace(endpoint)
	}

	cli, err := clientv3.New(clientv3.Config{
		Endpoints:   endpointList,
		DialTimeout: 5 * time.Second,
	})
	if err != nil {
		return nil, err
	}
	return &EtcdClient{client: cli}, nil
}

func (e *EtcdClient) Client() *clientv3.Client {
	return e.client
}

func (e *EtcdClient) Close() {
	e.client.Close()
}
