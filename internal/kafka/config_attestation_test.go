package kafka

import (
	"context"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"fastrg-controller/internal/db"
	"fastrg-controller/internal/storage"

	"github.com/jackc/pgx/v5/pgxpool"
	"google.golang.org/protobuf/proto"
)

// attestationEnv is a consumer wired to a real etcd and PostgreSQL. These cases
// are about which config version a CONFIG_APPLY_OK confirms, so they need etcd
// to actually hold more than one version of the key.
type attestationEnv struct {
	ctx      context.Context
	consumer *Consumer
	etcd     *storage.EtcdClient
	pool     *pgxpool.Pool
	node     string
	user     string
	key      string
}

func newAttestationEnv(t *testing.T, name string) *attestationEnv {
	t.Helper()
	dsn := os.Getenv("TEST_DATABASE_URL")
	etcdEndpoints := os.Getenv("TEST_ETCD_ENDPOINTS")
	if dsn == "" || etcdEndpoints == "" {
		t.Skip("TEST_DATABASE_URL / TEST_ETCD_ENDPOINTS not set; skipping config attestation test")
	}

	ctx, cancel := context.WithCancel(context.Background())
	scopedDSN, dropSchema := createTask12KafkaSchema(t, ctx, dsn, "attestation")

	t.Setenv("ETCD_ENDPOINTS", etcdEndpoints)
	etcd, err := storage.NewEtcdClient()
	if err != nil {
		cancel()
		dropSchema()
		t.Fatalf("etcd connect: %v", err)
	}

	database, err := db.New(ctx, scopedDSN)
	if err != nil {
		etcd.Close()
		cancel()
		dropSchema()
		t.Fatalf("db: %v", err)
	}
	pool, err := pgxpool.New(ctx, scopedDSN)
	if err != nil {
		database.Close()
		etcd.Close()
		cancel()
		dropSchema()
		t.Fatalf("pool: %v", err)
	}

	node := fmt.Sprintf("attest-%s-%d", name, time.Now().UnixNano())
	user := "7"
	env := &attestationEnv{
		ctx:      ctx,
		consumer: &Consumer{db: database, etcd: etcd},
		etcd:     etcd,
		pool:     pool,
		node:     node,
		user:     user,
		key:      fmt.Sprintf("configs/%s/hsi/%s", node, user),
	}

	t.Cleanup(func() {
		etcd.Client().Delete(context.Background(), env.key)
		pool.Close()
		database.Close()
		etcd.Close()
		cancel()
		dropSchema()
	})
	return env
}

// putConfig writes one version of the subscriber's config and returns the
// ModRevision that write produced.
func (e *attestationEnv) putConfig(t *testing.T, resourceVersion string) int64 {
	t.Helper()
	value := fmt.Sprintf(
		`{"config":{"user_id":%q,"desire_status":"connect","account_name":"acct-v%s"},`+
			`"metadata":{"node":%q,"resourceVersion":%q,"updatedBy":"admin","updatedAt":"2026-06-10T05:07:07Z"}}`,
		e.user, resourceVersion, e.node, resourceVersion,
	)
	resp, err := e.etcd.Client().Put(e.ctx, e.key, value)
	if err != nil {
		t.Fatalf("put config v%s: %v", resourceVersion, err)
	}
	// For a Put, the header revision is the ModRevision the key just got.
	return resp.Header.Revision
}

// currentRow reads back what the consumer recorded as confirmed.
func (e *attestationEnv) currentRow(t *testing.T) (modRevision int64, config string) {
	t.Helper()
	err := e.pool.QueryRow(e.ctx,
		`SELECT mod_revision, config::text FROM hsi_config_current WHERE node_uuid = $1 AND user_id = $2`,
		e.node, e.user,
	).Scan(&modRevision, &config)
	if err != nil {
		t.Fatalf("read hsi_config_current: %v", err)
	}
	return modRevision, config
}

// attestedApplyEvent builds a CONFIG_APPLY_OK carrying the node's attestation of
// which revision it applied. attested == 0 means an older node that cannot say.
func attestedApplyEvent(t *testing.T, node, user string, attested, timestamp int64) []byte {
	t.Helper()
	event := configApplyEvent(node, user, true, timestamp)
	event.GetConfigApplyResult().AppliedModRevision = attested
	value, err := proto.Marshal(event)
	if err != nil {
		t.Fatalf("marshal attested apply event: %v", err)
	}
	return value
}

// TestAttestedRevisionIsRecordedVerbatim is the case the field exists for: the
// controller pushed a newer config after the node reported, and the node's own
// attestation — not etcd's latest — is what gets recorded as confirmed, content
// included.
func TestAttestedRevisionIsRecordedVerbatim(t *testing.T) {
	env := newAttestationEnv(t, "verbatim")

	firstRevision := env.putConfig(t, "1")
	secondRevision := env.putConfig(t, "2")
	if secondRevision <= firstRevision {
		t.Fatalf("second put revision %d did not advance past %d", secondRevision, firstRevision)
	}

	// The node reports it applied the FIRST version, while etcd already holds
	// the second.
	value := attestedApplyEvent(t, env.node, env.user, firstRevision, time.Now().Unix())
	if err := env.consumer.handle(env.ctx, value); err != nil {
		t.Fatalf("handle attested apply: %v", err)
	}

	gotRevision, gotConfig := env.currentRow(t)
	if gotRevision != firstRevision {
		t.Fatalf("mod_revision = %d, want the attested %d (not etcd's latest %d)",
			gotRevision, firstRevision, secondRevision)
	}
	if !strings.Contains(gotConfig, `"acct-v1"`) {
		t.Fatalf("stored config is not the attested version's content: %s", gotConfig)
	}
	if strings.Contains(gotConfig, `"acct-v2"`) {
		t.Fatalf("stored config carries the newer, unconfirmed version: %s", gotConfig)
	}
}

// TestOutOfOrderAttestationIsRejected: Kafka is at-least-once and only ordered
// per partition, so an older attestation can arrive after a newer one. The row's
// mod_revision guard has to drop it rather than walk the confirmed version back.
func TestOutOfOrderAttestationIsRejected(t *testing.T) {
	env := newAttestationEnv(t, "outoforder")

	firstRevision := env.putConfig(t, "1")
	secondRevision := env.putConfig(t, "2")

	newer := attestedApplyEvent(t, env.node, env.user, secondRevision, time.Now().Unix())
	if err := env.consumer.handle(env.ctx, newer); err != nil {
		t.Fatalf("handle newer attestation: %v", err)
	}
	if got, _ := env.currentRow(t); got != secondRevision {
		t.Fatalf("mod_revision = %d, want %d before the stale replay", got, secondRevision)
	}

	// The older attestation now turns up late.
	older := attestedApplyEvent(t, env.node, env.user, firstRevision, time.Now().Unix()+1)
	if err := env.consumer.handle(env.ctx, older); err != nil {
		t.Fatalf("handle older attestation: %v", err)
	}

	gotRevision, gotConfig := env.currentRow(t)
	if gotRevision != secondRevision {
		t.Fatalf("mod_revision = %d, want it to stay at %d — a late older attestation must not win",
			gotRevision, secondRevision)
	}
	if !strings.Contains(gotConfig, `"acct-v2"`) {
		t.Fatalf("stored config was rolled back to the older version: %s", gotConfig)
	}
}

// TestUnattestedApplyUsesCurrentValue: a node too old to attest sends 0, and the
// controller falls back to reading whatever etcd currently holds — the behaviour
// that existed before the field.
func TestUnattestedApplyUsesCurrentValue(t *testing.T) {
	env := newAttestationEnv(t, "unattested")

	env.putConfig(t, "1")
	secondRevision := env.putConfig(t, "2")

	value := attestedApplyEvent(t, env.node, env.user, 0, time.Now().Unix())
	if err := env.consumer.handle(env.ctx, value); err != nil {
		t.Fatalf("handle unattested apply: %v", err)
	}

	gotRevision, gotConfig := env.currentRow(t)
	if gotRevision != secondRevision {
		t.Fatalf("mod_revision = %d, want etcd's current %d for an unattested apply", gotRevision, secondRevision)
	}
	if !strings.Contains(gotConfig, `"acct-v2"`) {
		t.Fatalf("stored config is not etcd's current content: %s", gotConfig)
	}
}

// TestAttestedRevisionWithNoConfigInEtcd: the config was deleted before the
// event was processed, so there is nothing to record — and that must not be
// mistaken for a write failure.
func TestAttestedRevisionWithNoConfigInEtcd(t *testing.T) {
	env := newAttestationEnv(t, "missing")

	revision := env.putConfig(t, "1")
	if _, err := env.etcd.Client().Delete(env.ctx, env.key); err != nil {
		t.Fatalf("delete config: %v", err)
	}

	// Attest a revision from before the delete; the key is gone at HEAD but the
	// attested revision still reads back, so this is a normal confirmation.
	value := attestedApplyEvent(t, env.node, env.user, revision, time.Now().Unix())
	if err := env.consumer.handle(env.ctx, value); err != nil {
		t.Fatalf("handle attested apply after delete: %v", err)
	}

	gotRevision, _ := env.currentRow(t)
	if gotRevision != revision {
		t.Fatalf("mod_revision = %d, want the attested %d", gotRevision, revision)
	}
}
