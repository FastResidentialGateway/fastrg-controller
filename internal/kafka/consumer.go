// Package kafka consumes node events from Kafka and projects them into the
// controller's PostgreSQL tables (plan B-6). It is the single writer of
// pppoe_status and node_events, replacing the old etcd failed_events path.
//
// Delivery is at-least-once: an offset is committed only after its event has
// been durably written, and the DB writes are idempotent (event_time guards /
// unique dedup key), so redelivery is safe.
package kafka

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"reflect"
	"strconv"
	"strings"
	"time"

	"fastrg-controller/internal/db"
	"fastrg-controller/internal/storage"
	"fastrg-controller/internal/validation"
	eventsv1 "fastrg-controller/proto/eventsv1"

	"github.com/jackc/pgx/v5/pgconn"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
	"github.com/segmentio/kafka-go"
	"github.com/sirupsen/logrus"
	"google.golang.org/protobuf/proto"
)

const (
	defaultTopic             = "fastrg.node.events"
	defaultGroupID           = "fastrg-controller"
	retryInterval            = 2 * time.Second
	messageHandleTimeout     = 5 * time.Second
	infraRetryInitialBackoff = 2 * time.Second
	infraRetryMaxBackoff     = 30 * time.Second
	stallErrorThreshold      = 60 * time.Second
)

var (
	errRollbackSuperseded = errors.New("rollback skipped because the current config supersedes the failed version")
	errOfflineEditNoop    = errors.New("offline edit is already reflected in etcd")
	errOfflineEditDiscard = errors.New("offline edit discarded in favor of controller state")

	kafkaConsumerStallSeconds = promauto.NewGauge(prometheus.GaugeOpts{
		Name: "fastrg_kafka_consumer_stall_seconds",
		Help: "Seconds the Kafka consumer has continuously retried its current message because infrastructure is unavailable.",
	})
	kafkaConsumerInfraRetriesTotal = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "fastrg_kafka_consumer_infra_retries_total",
		Help: "Total Kafka message retries caused by unavailable infrastructure.",
	}, []string{"source"})
)

type infraRetryBackoff struct {
	current time.Duration
}

func newInfraRetryBackoff() *infraRetryBackoff {
	return &infraRetryBackoff{current: infraRetryInitialBackoff}
}

func (b *infraRetryBackoff) Next() time.Duration {
	delay := b.current
	b.current = min(b.current*2, infraRetryMaxBackoff)
	return delay
}

func (b *infraRetryBackoff) Reset() {
	b.current = infraRetryInitialBackoff
}

type consumerStall struct {
	startedAt time.Time
}

func (s *consumerStall) retry(source string, now time.Time) time.Duration {
	if s.startedAt.IsZero() {
		s.startedAt = now
	}
	elapsed := now.Sub(s.startedAt)
	if elapsed < 0 {
		elapsed = 0
	}
	kafkaConsumerStallSeconds.Set(elapsed.Seconds())
	kafkaConsumerInfraRetriesTotal.WithLabelValues(source).Inc()
	return elapsed
}

func (s *consumerStall) reset() {
	s.startedAt = time.Time{}
	kafkaConsumerStallSeconds.Set(0)
}

func stallLogLevel(elapsed time.Duration) logrus.Level {
	if elapsed >= stallErrorThreshold {
		return logrus.ErrorLevel
	}
	return logrus.WarnLevel
}

type rollbackSkipError struct {
	reason string
	warn   bool
}

func (e *rollbackSkipError) Error() string {
	return fmt.Sprintf("%s: %s", errRollbackSuperseded, e.reason)
}

func (e *rollbackSkipError) Unwrap() error {
	return errRollbackSuperseded
}

func skipRollback(reason string, warn bool) error {
	return &rollbackSkipError{reason: reason, warn: warn}
}

// rollbackUpdatedBy marks the metadata.updatedBy of a config that the automatic
// apply-failure rollback path rewrote, so an operator can tell a rollback write
// apart from a user or node write. See docs/contracts/resource-version.md §2.3
// and §8-7.
const rollbackUpdatedBy = "controller-rollback"

// offlineEditUpdatedBy marks metadata freshly stamped by the controller when
// it accepts a node's offline-edit proposal. Node metadata is never copied.
const offlineEditUpdatedBy = "node-offline-edit"

// configMetadata is the metadata envelope shared by the three config key
// families (docs/contracts/resource-version.md §2). Only the fields the
// rollback path reads or re-stamps are modelled here.
type configMetadata struct {
	Node            string `json:"node"`
	ResourceVersion string `json:"resourceVersion"`
	UpdatedBy       string `json:"updatedBy"`
	UpdatedAt       string `json:"updatedAt"`
}

func configResourceVersion(value []byte) (uint64, error) {
	var envelope struct {
		Metadata *struct {
			ResourceVersion string `json:"resourceVersion"`
		} `json:"metadata"`
	}
	if err := json.Unmarshal(value, &envelope); err != nil {
		return 0, fmt.Errorf("decode config JSON: %w", err)
	}
	if envelope.Metadata == nil || envelope.Metadata.ResourceVersion == "" {
		return 0, errors.New("metadata.resourceVersion is missing")
	}

	rv, err := strconv.ParseUint(envelope.Metadata.ResourceVersion, 10, 64)
	if err != nil {
		return 0, fmt.Errorf("parse metadata.resourceVersion %q: %w", envelope.Metadata.ResourceVersion, err)
	}
	return rv, nil
}

// rollbackConfigValue builds the value the rollback CAS commits: the payload of
// the last successful config (its "config" object) re-wrapped with freshly
// stamped metadata. Per docs/contracts/resource-version.md §2.3 and §8-7,
// rollback restores payload ONLY; metadata is never copied from the old
// snapshot but re-stamped so the resourceVersion chain and updatedAt advance
// (rv = curRV+1, updatedAt = now, updatedBy = rollback marker) rather than
// regressing, which task-33's offline-edit arbitration relies on.
//
// curRV is the already-parsed resourceVersion of current (validated by the
// caller). now is read here rather than passed in, matching every other CAS
// mutate closure in this repo, which timestamp inside the closure; only the
// committed attempt's value survives, so a retry simply re-stamps a coherent,
// later updatedAt with no cross-field inconsistency.
func rollbackConfigValue(prevJSON, current []byte, curRV uint64) ([]byte, error) {
	var prev struct {
		Config json.RawMessage `json:"config"`
	}
	if err := json.Unmarshal(prevJSON, &prev); err != nil {
		return nil, fmt.Errorf("decode last successful config: %w", err)
	}
	if len(prev.Config) == 0 {
		return nil, errors.New("last successful config has no payload")
	}

	// node is metadata identity, not payload; carry it from the live value.
	var cur struct {
		Metadata struct {
			Node string `json:"node"`
		} `json:"metadata"`
	}
	if err := json.Unmarshal(current, &cur); err != nil {
		return nil, fmt.Errorf("decode current metadata: %w", err)
	}

	envelope := struct {
		Config   json.RawMessage `json:"config"`
		Metadata configMetadata  `json:"metadata"`
	}{
		Config: prev.Config,
		Metadata: configMetadata{
			Node:            cur.Metadata.Node,
			ResourceVersion: strconv.FormatUint(curRV+1, 10),
			UpdatedBy:       rollbackUpdatedBy,
			UpdatedAt:       time.Now().UTC().Format(time.RFC3339),
		},
	}
	return json.Marshal(envelope)
}

// rollbackGuardInputs carries the event version markers evaluated against the
// value and ModRevision from the same CAS read. The optional wrapper argument
// lets existing resourceVersion-only tests exercise the empty-applied-version
// fallback without changing their call sites.
type rollbackGuardInputs struct {
	currentModRevision int64
	appliedRV          string
	correlationID      string
}

// guardedRollbackMutation decides whether the failed version is still the
// current etcd value. It must run inside storage.CASWithRevision so a
// concurrent write causes a retry with the newest value and ModRevision and
// therefore a fresh guard evaluation.
//
// When it does roll back, it restores only the last successful payload and
// re-stamps metadata (see rollbackConfigValue): the write must not regress the
// resourceVersion chain or updatedAt back to the old snapshot's values.
//
// applied_resource_version is the primary guard. When correlation_id contains
// a valid etcd ModRevision it is an additional generation check that closes
// the delete-and-recreate corner where resourceVersion resets to 1. An empty
// applied_resource_version uses the transitional curRV==okRV+1 guard.
func guardedRollbackMutation(
	prevConfig *db.HSIConfigRow,
	current []byte,
	inputs ...rollbackGuardInputs,
) (storage.CASResult, error) {
	var guard rollbackGuardInputs
	if len(inputs) > 0 {
		guard = inputs[0]
	}

	if current == nil {
		return storage.CASResult{}, skipRollback("config key no longer exists", false)
	}

	curRV, err := configResourceVersion(current)
	if err != nil {
		return storage.CASResult{}, skipRollback("cannot establish current resourceVersion: "+err.Error(), true)
	}

	if guard.appliedRV == "" {
		var okRV uint64
		if prevConfig != nil {
			okRV, err = configResourceVersion(prevConfig.ConfigJSON)
			if err != nil {
				return storage.CASResult{}, skipRollback("cannot establish last successful resourceVersion: "+err.Error(), true)
			}
		}

		// Written this way instead of comparing with okRV+1 so MaxUint64 cannot
		// wrap around and accidentally authorize a rollback.
		if curRV > okRV && curRV-okRV == 1 {
			// The transitional guard passed; continue to the common rollback
			// write path below.
		} else {
			return storage.CASResult{}, skipRollback(
				fmt.Sprintf("current resourceVersion %d is not the failed successor of last successful resourceVersion %d", curRV, okRV),
				false,
			)
		}
	} else {
		appliedRV, err := strconv.ParseUint(guard.appliedRV, 10, 64)
		if err != nil {
			return storage.CASResult{}, skipRollback(
				fmt.Sprintf("cannot parse applied_resource_version %q: %v", guard.appliedRV, err),
				true,
			)
		}
		if curRV != appliedRV {
			return storage.CASResult{}, skipRollback(
				fmt.Sprintf("current resourceVersion %d no longer matches applied resourceVersion %d", curRV, appliedRV),
				false,
			)
		}

		if guard.correlationID != "" {
			appliedModRevision, parseErr := strconv.ParseInt(guard.correlationID, 10, 64)
			if parseErr == nil && appliedModRevision != guard.currentModRevision {
				return storage.CASResult{}, skipRollback(
					fmt.Sprintf(
						"current ModRevision %d no longer matches applied ModRevision %d; a recreate or later write replaced the failed value",
						guard.currentModRevision,
						appliedModRevision,
					),
					false,
				)
			}
		}
	}

	if prevConfig == nil {
		return storage.CASResult{Delete: true}, nil
	}
	value, err := rollbackConfigValue(prevConfig.ConfigJSON, current, curRV)
	if err != nil {
		return storage.CASResult{}, skipRollback("cannot rebuild rollback config: "+err.Error(), true)
	}
	return storage.CASResult{Value: value}, nil
}

type offlineEditProposal struct {
	nodeID           string
	userID           string
	key              string
	kind             eventsv1.OfflineEditKind
	payloadField     string
	payload          json.RawMessage
	canonicalPayload any
	resourceVersion  string
	editedAt         time.Time
	editSummary      string
	deleted          bool
}

type offlineEditDiscardError struct {
	reason string
	warn   bool
}

func (e *offlineEditDiscardError) Error() string {
	return fmt.Sprintf("%s: %s", errOfflineEditDiscard, e.reason)
}

func (e *offlineEditDiscardError) Unwrap() error { return errOfflineEditDiscard }

func discardOfflineEdit(reason string, warn bool) error {
	return &offlineEditDiscardError{reason: reason, warn: warn}
}

func offlineEditKindSpec(kind eventsv1.OfflineEditKind, nodeID, userID string) (key, payloadField string, err error) {
	switch kind {
	case eventsv1.OfflineEditKind_OFFLINE_EDIT_KIND_HSI_CONFIG:
		return fmt.Sprintf("configs/%s/hsi/%s", nodeID, userID), "config", nil
	case eventsv1.OfflineEditKind_OFFLINE_EDIT_KIND_DNS_RECORDS:
		return fmt.Sprintf("configs/%s/dns/%s", nodeID, userID), "records", nil
	case eventsv1.OfflineEditKind_OFFLINE_EDIT_KIND_SUBSCRIBER_COUNT:
		return fmt.Sprintf("user_counts/%s/", nodeID), "subscriber_count", nil
	default:
		return "", "", fmt.Errorf("unknown offline edit kind %d", kind)
	}
}

func decodeCanonicalJSON(raw []byte) (any, error) {
	var value any
	if err := json.Unmarshal(raw, &value); err != nil {
		return nil, err
	}
	return value, nil
}

func parseOfflineEditEnvelope(configJSON, payloadField string) (json.RawMessage, any, string, error) {
	var envelope map[string]json.RawMessage
	if err := json.Unmarshal([]byte(configJSON), &envelope); err != nil {
		return nil, nil, "", fmt.Errorf("decode config_json: %w", err)
	}
	if envelope == nil {
		return nil, nil, "", errors.New("config_json must be a JSON object")
	}

	payload, ok := envelope[payloadField]
	if !ok {
		return nil, nil, "", fmt.Errorf("config_json has no %q payload", payloadField)
	}
	canonical, err := decodeCanonicalJSON(payload)
	if err != nil {
		return nil, nil, "", fmt.Errorf("decode %s payload: %w", payloadField, err)
	}

	var metadata struct {
		ResourceVersion string `json:"resourceVersion"`
	}
	metadataJSON, ok := envelope["metadata"]
	if !ok {
		return nil, nil, "", errors.New("config_json has no metadata")
	}
	if err := json.Unmarshal(metadataJSON, &metadata); err != nil {
		return nil, nil, "", fmt.Errorf("decode config_json metadata: %w", err)
	}
	return payload, canonical, metadata.ResourceVersion, nil
}

// prepareOfflineEdit performs poison-message validation and derives the etcd
// target solely from the NodeEvent envelope. It deliberately does not parse a
// tombstone's empty config_json.
func prepareOfflineEdit(ev *eventsv1.NodeEvent, edit *eventsv1.ConfigOfflineEdit) (offlineEditProposal, error) {
	if ev.GetType() != eventsv1.EventType_EVENT_TYPE_CONFIG_OFFLINE_EDIT {
		return offlineEditProposal{}, fmt.Errorf("offline edit payload has event type %s", ev.GetType())
	}
	if err := validation.ValidateNodeID(ev.GetNodeUuid()); err != nil {
		return offlineEditProposal{}, fmt.Errorf("invalid node_uuid: %w", err)
	}

	key, payloadField, err := offlineEditKindSpec(edit.GetKind(), ev.GetNodeUuid(), ev.GetUserId())
	if err != nil {
		return offlineEditProposal{}, err
	}
	if edit.GetKind() == eventsv1.OfflineEditKind_OFFLINE_EDIT_KIND_SUBSCRIBER_COUNT {
		if ev.GetUserId() != "0" {
			return offlineEditProposal{}, errors.New("subscriber-count offline edit requires user_id \"0\"")
		}
	} else if err := validation.ValidateUserID(ev.GetUserId()); err != nil {
		return offlineEditProposal{}, fmt.Errorf("invalid user_id: %w", err)
	}

	proposal := offlineEditProposal{
		nodeID:          ev.GetNodeUuid(),
		userID:          ev.GetUserId(),
		key:             key,
		kind:            edit.GetKind(),
		payloadField:    payloadField,
		resourceVersion: edit.GetResourceVersion(),
		editedAt:        time.Unix(edit.GetEditedAt(), 0).UTC(),
		editSummary:     edit.GetEditSummary(),
		deleted:         edit.GetDeleted(),
	}

	if proposal.deleted {
		if edit.GetConfigJson() != "" {
			return offlineEditProposal{}, errors.New("tombstone config_json must be empty")
		}
		if proposal.kind != eventsv1.OfflineEditKind_OFFLINE_EDIT_KIND_HSI_CONFIG {
			return offlineEditProposal{}, errors.New("tombstones are valid only for HSI_CONFIG")
		}
		return proposal, nil
	}

	payload, canonical, metadataRV, err := parseOfflineEditEnvelope(edit.GetConfigJson(), payloadField)
	if err != nil {
		return offlineEditProposal{}, err
	}
	if edit.GetResourceVersion() != metadataRV {
		return offlineEditProposal{}, fmt.Errorf(
			"resource_version %q does not match config_json metadata.resourceVersion %q",
			edit.GetResourceVersion(), metadataRV,
		)
	}
	proposal.payload = append(json.RawMessage(nil), payload...)
	proposal.canonicalPayload = canonical
	return proposal, nil
}

func currentOfflineEditPayload(current []byte, payloadField string) (any, error) {
	var envelope map[string]json.RawMessage
	if err := json.Unmarshal(current, &envelope); err != nil {
		return nil, fmt.Errorf("decode current JSON: %w", err)
	}
	if envelope == nil {
		return nil, errors.New("current value must be a JSON object")
	}
	payload, ok := envelope[payloadField]
	if !ok {
		return nil, fmt.Errorf("current value has no %q payload", payloadField)
	}
	canonical, err := decodeCanonicalJSON(payload)
	if err != nil {
		return nil, fmt.Errorf("decode current %s payload: %w", payloadField, err)
	}
	return canonical, nil
}

func currentOfflineEditMetadata(current []byte) (uint64, time.Time, error) {
	var envelope map[string]json.RawMessage
	if err := json.Unmarshal(current, &envelope); err != nil {
		return 0, time.Time{}, fmt.Errorf("decode current JSON: %w", err)
	}
	metadataJSON, ok := envelope["metadata"]
	if !ok {
		return 0, time.Time{}, errors.New("current value has no metadata")
	}
	var metadata configMetadata
	if err := json.Unmarshal(metadataJSON, &metadata); err != nil {
		return 0, time.Time{}, fmt.Errorf("decode current metadata: %w", err)
	}
	rv, err := strconv.ParseUint(metadata.ResourceVersion, 10, 64)
	if err != nil {
		return 0, time.Time{}, fmt.Errorf("parse current metadata.resourceVersion %q: %w", metadata.ResourceVersion, err)
	}
	updatedAt, err := time.Parse(time.RFC3339, metadata.UpdatedAt)
	if err != nil {
		return 0, time.Time{}, fmt.Errorf("parse current metadata.updatedAt %q: %w", metadata.UpdatedAt, err)
	}
	if _, offset := updatedAt.Zone(); offset != 0 {
		return 0, time.Time{}, fmt.Errorf("current metadata.updatedAt %q is not UTC", metadata.UpdatedAt)
	}
	return rv, updatedAt, nil
}

// offlineEditMutation is the pure arbitration decision run inside EtcdClient.CAS.
// It must use current on every invocation: a CAS conflict causes a fresh read,
// re-evaluation, and (when accepted) a freshly stamped value based on that
// newest current state.
func offlineEditMutation(proposal offlineEditProposal, current []byte, now time.Time) (storage.CASResult, error) {
	if current == nil {
		if proposal.deleted {
			return storage.CASResult{}, errOfflineEditNoop
		}
		return storage.CASResult{}, discardOfflineEdit("target key no longer exists", false)
	}

	if !proposal.deleted {
		currentPayload, err := currentOfflineEditPayload(current, proposal.payloadField)
		if err != nil {
			return storage.CASResult{}, discardOfflineEdit("cannot establish current payload: "+err.Error(), true)
		}
		if reflect.DeepEqual(proposal.canonicalPayload, currentPayload) {
			return storage.CASResult{}, errOfflineEditNoop
		}
	}

	currentRV, currentUpdatedAt, err := currentOfflineEditMetadata(current)
	if err != nil {
		return storage.CASResult{}, discardOfflineEdit("cannot establish current metadata: "+err.Error(), true)
	}

	nodeRV, err := strconv.ParseUint(proposal.resourceVersion, 10, 64)
	if err != nil {
		return storage.CASResult{}, discardOfflineEdit(
			fmt.Sprintf("cannot parse node resource_version %q: %v", proposal.resourceVersion, err), true,
		)
	}

	if proposal.deleted {
		if nodeRV < currentRV {
			return storage.CASResult{}, discardOfflineEdit(
				fmt.Sprintf("node resource_version %d is behind current resourceVersion %d", nodeRV, currentRV), false,
			)
		}
		if !proposal.editedAt.After(currentUpdatedAt) {
			return storage.CASResult{}, discardOfflineEdit(
				fmt.Sprintf("node edited_at %s is not later than current updatedAt %s", proposal.editedAt.Format(time.RFC3339), currentUpdatedAt.Format(time.RFC3339)), false,
			)
		}
		return storage.CASResult{Delete: true}, nil
	}

	if nodeRV <= currentRV {
		return storage.CASResult{}, discardOfflineEdit(
			fmt.Sprintf("node resource_version %d does not exceed current resourceVersion %d", nodeRV, currentRV), false,
		)
	}
	if !proposal.editedAt.After(currentUpdatedAt) {
		return storage.CASResult{}, discardOfflineEdit(
			fmt.Sprintf("node edited_at %s is not later than current updatedAt %s", proposal.editedAt.Format(time.RFC3339), currentUpdatedAt.Format(time.RFC3339)), false,
		)
	}
	value, err := json.Marshal(map[string]any{
		proposal.payloadField: json.RawMessage(proposal.payload),
		"metadata": configMetadata{
			Node:            proposal.nodeID,
			ResourceVersion: strconv.FormatUint(currentRV+1, 10),
			UpdatedBy:       offlineEditUpdatedBy,
			UpdatedAt:       now.UTC().Format(time.RFC3339),
		},
	})
	if err != nil {
		return storage.CASResult{}, fmt.Errorf("encode accepted offline edit: %w", err)
	}
	return storage.CASResult{Value: value}, nil
}

// Consumer reads NodeEvent messages from Kafka and writes them to PostgreSQL.
type Consumer struct {
	reader *kafka.Reader
	db     *db.DB
	etcd   *storage.EtcdClient
}

// Brokers returns the configured Kafka broker list, or nil when Kafka is not
// configured (KAFKA_BROKERS unset), in which case the consumer is not started.
func Brokers() []string {
	v := os.Getenv("KAFKA_BROKERS")
	if v == "" {
		return nil
	}
	parts := strings.Split(v, ",")
	for i := range parts {
		parts[i] = strings.TrimSpace(parts[i])
	}
	return parts
}

func envOr(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

// NewConsumer builds a consumer reading from KAFKA_TOPIC (default
// fastrg.node.events) in consumer group KAFKA_GROUP (default fastrg-controller).
func NewConsumer(brokers []string, database *db.DB, etcdClient *storage.EtcdClient) *Consumer {
	reader := kafka.NewReader(kafka.ReaderConfig{
		Brokers:  brokers,
		Topic:    envOr("KAFKA_TOPIC", defaultTopic),
		GroupID:  envOr("KAFKA_GROUP", defaultGroupID),
		MinBytes: 1,
		MaxBytes: 10e6,
	})
	return &Consumer{reader: reader, db: database, etcd: etcdClient}
}

// Run consumes until ctx is cancelled, then closes the reader.
func (c *Consumer) Run(ctx context.Context) {
	logrus.Info("Started Kafka consumer for node events")
	defer kafkaConsumerStallSeconds.Set(0)

	// Close the reader built at construction time and wait until the topic has
	// at least one partition before re-joining the consumer group.  If the
	// controller starts before the node has produced its first Kafka event the
	// topic may not exist yet; kafka-go would join the group with 0 assigned
	// partitions and silently receive nothing until process restart.
	cfg := c.reader.Config()
	c.reader.Close()
	c.waitForTopicReady(ctx, cfg.Brokers, cfg.Topic)
	if ctx.Err() != nil {
		return
	}
	c.reader = kafka.NewReader(cfg)
	defer c.reader.Close()

	for ctx.Err() == nil {
		m, err := c.reader.FetchMessage(ctx)
		if err != nil {
			if ctx.Err() != nil {
				return
			}
			logrus.WithError(err).Error("kafka: fetch failed, retrying")
			c.sleep(ctx)
			continue
		}

		// Retry the same message with exponential backoff. If the DB or etcd is
		// unavailable, keep retrying this message without committing the offset.
		// If the failure is eligible for dead-lettering, persist the message to
		// DLQ before committing the offset.
		const maxRetries = 5
		backoff := 100 * time.Millisecond
		infraBackoff := newInfraRetryBackoff()
		stall := consumerStall{}
		failed := false
		for attempt := 1; ctx.Err() == nil; {
			// Bound each attempt so clients that wait for an unavailable backend to
			// reconnect return control to this retry loop. The outer consumer context
			// still governs shutdown and no offset is committed on a timeout.
			attemptCtx, cancelAttempt := context.WithTimeout(ctx, messageHandleTimeout)
			err := c.handle(attemptCtx, m.Value)
			cancelAttempt()
			if err != nil {
				failed = true

				if isDatabaseUnavailable(err) || isEtcdUnavailable(err) {
					source := "etcd"
					if isDatabaseUnavailable(err) {
						source = "database"
					}
					elapsed := stall.retry(source, time.Now())
					entry := logrus.WithError(err).WithFields(logrus.Fields{
						"source":        source,
						"stall_seconds": elapsed.Seconds(),
						"topic":         m.Topic,
						"partition":     m.Partition,
						"offset":        m.Offset,
					})
					if stallLogLevel(elapsed) == logrus.ErrorLevel {
						entry.Error("kafka: infrastructure unavailable, retrying same message")
					} else {
						entry.Warn("kafka: infrastructure unavailable, retrying same message")
					}
					c.sleepFor(ctx, infraBackoff.Next())
					continue
				}

				stall.reset()
				infraBackoff.Reset()
				logrus.WithError(err).Warnf("kafka: handle failed (attempt %d/%d), backing off %v",
					attempt, maxRetries, backoff)
				if attempt >= maxRetries {
					break
				}
				select {
				case <-ctx.Done():
					return
				case <-time.After(backoff):
					backoff = time.Duration(float64(backoff) * 1.5) // exponential backoff
					attempt++
					continue
				}
			}
			// Success
			stall.reset()
			infraBackoff.Reset()
			failed = false
			break
		}

		// If still failed after retries, send to DLQ (database persistent queue).
		if failed && ctx.Err() == nil {
			logrus.Errorf("kafka: message failed after %d retries, sending to DLQ", maxRetries)
			c.waitAndSendToDLQ(ctx, m)
			stall.reset()
		}
		if ctx.Err() != nil {
			return
		}

		if err := c.reader.CommitMessages(ctx, m); err != nil {
			logrus.WithError(err).Error("kafka: commit failed")
		}
	}
}

// isDatabaseUnavailable distinguishes transient connectivity, resource, and
// transaction failures from permanent SQL errors. Permanent SQL errors can be
// dead-lettered into kafka_dlq; transient DB errors cannot, because kafka_dlq is
// stored in the same PostgreSQL instance.
func isDatabaseUnavailable(err error) bool {
	if err == nil {
		return false
	}

	var dbErr databaseOperationError
	if !errors.As(err, &dbErr) {
		return false
	}

	var pgErr *pgconn.PgError
	if !errors.As(err, &pgErr) {
		return true
	}

	return isTransientSQLState(pgErr.Code)
}

func isTransientSQLState(code string) bool {
	if code == "40001" || code == "40P01" {
		return true
	}

	return strings.HasPrefix(code, "08") ||
		strings.HasPrefix(code, "53") ||
		strings.HasPrefix(code, "57")
}

type databaseOperationError struct {
	err error
}

func (e databaseOperationError) Error() string {
	return e.err.Error()
}

func (e databaseOperationError) Unwrap() error {
	return e.err
}

func wrapDatabaseError(err error) error {
	if err == nil {
		return nil
	}
	return databaseOperationError{err: err}
}

// isEtcdUnavailable identifies failures from etcd operations. All etcd
// failures exposed by handle are retried because a later attempt may succeed
// and the handler's database writes and guarded CAS are idempotent.
func isEtcdUnavailable(err error) bool {
	if err == nil {
		return false
	}

	var etcdErr etcdOperationError
	return errors.As(err, &etcdErr)
}

type etcdOperationError struct {
	err error
}

func (e etcdOperationError) Error() string {
	return e.err.Error()
}

func (e etcdOperationError) Unwrap() error {
	return e.err
}

func wrapEtcdError(err error) error {
	if err == nil {
		return nil
	}
	return etcdOperationError{err: err}
}

// waitForTopicReady blocks until the Kafka topic has at least 1 partition or
// ctx is cancelled.  This prevents the consumer group from joining before the
// topic exists, which would assign 0 partitions and silently drop all messages.
func (c *Consumer) waitForTopicReady(ctx context.Context, brokers []string, topic string) {
	for ctx.Err() == nil {
		var lastErr error
		lastPartitionCount := 0
		for _, broker := range brokers {
			conn, err := kafka.DialContext(ctx, "tcp", broker)
			if err != nil {
				lastErr = err
				continue
			}
			partitions, err := conn.ReadPartitions(topic)
			conn.Close()
			if err == nil && len(partitions) > 0 {
				logrus.Infof("kafka: topic %q ready (%d partition(s)), joining consumer group", topic, len(partitions))
				return
			}
			lastErr = err
			lastPartitionCount = len(partitions)
		}
		if lastErr != nil {
			logrus.WithError(lastErr).Warn("kafka: no broker could report a ready topic before joining consumer group")
		} else {
			logrus.Warnf("kafka: topic %q not yet available (partitions=%d), waiting", topic, lastPartitionCount)
		}
		c.sleep(ctx)
	}
}

func (c *Consumer) sleep(ctx context.Context) {
	c.sleepFor(ctx, retryInterval)
}

func (c *Consumer) sleepFor(ctx context.Context, delay time.Duration) {
	select {
	case <-ctx.Done():
	case <-time.After(delay):
	}
}

// handle decodes one NodeEvent and writes it to the appropriate table.
func (c *Consumer) handle(ctx context.Context, value []byte) error {
	var ev eventsv1.NodeEvent
	if err := proto.Unmarshal(value, &ev); err != nil {
		// A malformed message will never decode; log and drop it (return nil so
		// the offset is committed) rather than blocking the partition forever.
		logrus.WithError(err).Warn("kafka: dropping undecodable message")
		return nil
	}

	eventTime := time.Unix(ev.GetTimestamp(), 0).UTC()

	switch p := ev.GetPayload().(type) {
	case *eventsv1.NodeEvent_ConfigOfflineEdit:
		return c.handleConfigOfflineEdit(ctx, &ev, p.ConfigOfflineEdit, eventTime)

	case *eventsv1.NodeEvent_PppoeStateChange:
		return wrapDatabaseError(c.db.UpsertPPPoEStatus(ctx, db.PPPoEStatusRow{
			NodeUUID:     ev.GetNodeUuid(),
			UserID:       ev.GetUserId(),
			Phase:        phaseString(p.PppoeStateChange.GetPhase()),
			HSIIPv4:      p.PppoeStateChange.GetHsiIpv4(),
			HSIIPv4GW:    p.PppoeStateChange.GetHsiIpv4Gw(),
			ErrorMessage: p.PppoeStateChange.GetErrorMessage(),
			EventTime:    eventTime,
		}))

	case *eventsv1.NodeEvent_ConfigApplyResult:
		success := p.ConfigApplyResult.GetSuccess()
		inserted, err := c.db.InsertNodeEvent(ctx, db.NodeEventRow{
			NodeUUID:      ev.GetNodeUuid(),
			UserID:        ev.GetUserId(),
			EventType:     eventTypeString(ev.GetType()),
			Action:        p.ConfigApplyResult.GetAction(),
			Success:       &success,
			ErrorCode:     p.ConfigApplyResult.GetErrorCode(),
			ErrorMessage:  p.ConfigApplyResult.GetErrorMessage(),
			CorrelationID: ev.GetCorrelationId(),
			EventTime:     eventTime,
		})
		if err != nil {
			return wrapDatabaseError(err)
		}
		if !inserted {
			logrus.WithFields(logrus.Fields{
				"node":           ev.GetNodeUuid(),
				"user":           ev.GetUserId(),
				"event_type":     eventTypeString(ev.GetType()),
				"correlation_id": ev.GetCorrelationId(),
			}).Info("kafka: duplicate node event ignored")
		}

		// If config apply succeeded, update hsi_config_current to mark this version
		// as "node-confirmed-success". hsi_config_current is now the source of truth
		// for "what config has the node successfully applied", NOT "etcd's latest config".
		if success {
			// Update hsi_config_current only when etcd is configured; in unit tests
			// the client may be nil (etcd not needed for projection-only testing).
			if c.etcd == nil {
				logrus.Warnf("kafka: etcd not configured, skipping hsi_config_current update for node=%s user=%s",
					ev.GetNodeUuid(), ev.GetUserId())
			} else {
				etcdKey := fmt.Sprintf("configs/%s/hsi/%s", ev.GetNodeUuid(), ev.GetUserId())
				resp, err := c.etcd.Client().Get(ctx, etcdKey)
				if err != nil {
					logrus.WithError(err).Error("kafka: failed to read current config from etcd after CONFIG_APPLY_OK")
					return wrapEtcdError(err)
				}
				if len(resp.Kvs) > 0 {
					kv := resp.Kvs[0]
					row := db.HSIConfigRow{
						NodeUUID:        ev.GetNodeUuid(),
						UserID:          ev.GetUserId(),
						ConfigJSON:      kv.Value,
						ModRevision:     kv.ModRevision,
						ResourceVersion: "",
						UpdatedBy:       "node",
						UpdatedAt:       &eventTime,
						Action:          db.ActionUpsert,
						DesireStatus:    "",
					}
					// Update current state and record success atomically. The operation
					// remains idempotent when the consumer retries the same event.
					if err := c.db.UpsertCurrentWithHistory(ctx, row, "success"); err != nil {
						logrus.WithError(err).Error("kafka: failed to update current config and success history after CONFIG_APPLY_OK")
						return wrapDatabaseError(err)
					}
					logrus.Infof("kafka: config apply succeeded for node=%s user=%s, updated hsi_config_current",
						ev.GetNodeUuid(), ev.GetUserId())
				} else {
					logrus.Warnf("kafka: CONFIG_APPLY_OK for node=%s user=%s but config not found in etcd",
						ev.GetNodeUuid(), ev.GetUserId())
				}
			} // end c.etcd != nil
		}

		// If config apply failed, automatically rollback to the last successful version
		// to prevent invalid config from remaining in etcd.
		if !success {
			logrus.Warnf("kafka: config apply failed for node=%s user=%s, rolling back to last successful version",
				ev.GetNodeUuid(), ev.GetUserId())

			// Find the last successful config from DB history
			prevConfig, err := c.db.GetLastSuccessfulConfig(ctx, ev.GetNodeUuid(), ev.GetUserId())
			if err != nil {
				logrus.WithError(err).Error("kafka: failed to query last successful config")
				return wrapDatabaseError(err)
			}

			// Skip etcd rollback when etcd is not configured (e.g. unit tests).
			if c.etcd == nil {
				logrus.Warnf("kafka: etcd not configured, skipping rollback write for node=%s user=%s",
					ev.GetNodeUuid(), ev.GetUserId())
			} else {
				etcdKey := fmt.Sprintf("configs/%s/hsi/%s", ev.GetNodeUuid(), ev.GetUserId())
				rbErr := c.etcd.CASWithRevision(ctx, etcdKey, func(current []byte, modRevision int64) (storage.CASResult, error) {
					return guardedRollbackMutation(prevConfig, current, rollbackGuardInputs{
						currentModRevision: modRevision,
						appliedRV:          p.ConfigApplyResult.GetAppliedResourceVersion(),
						correlationID:      ev.GetCorrelationId(),
					})
				})
				if rbErr != nil {
					if !errors.Is(rbErr, errRollbackSuperseded) {
						logrus.WithError(rbErr).Error("kafka: failed to roll back config in etcd")
						return wrapEtcdError(rbErr)
					}

					entry := logrus.WithError(rbErr).WithFields(logrus.Fields{
						"node": ev.GetNodeUuid(),
						"user": ev.GetUserId(),
					})
					var skipErr *rollbackSkipError
					if errors.As(rbErr, &skipErr) && skipErr.warn {
						entry.Warn("kafka: skipped config rollback because resourceVersion metadata is unusable")
					}
					entry.Info("kafka: skipped config rollback because the failed version is no longer current")
				} else if prevConfig == nil {
					logrus.Infof("kafka: invalid config deleted from etcd for node=%s user=%s (no successful version)",
						ev.GetNodeUuid(), ev.GetUserId())
				} else {
					logrus.Infof("kafka: config rolled back to last successful version in etcd for node=%s user=%s",
						ev.GetNodeUuid(), ev.GetUserId())
				}
			}

			// Record the failure in DB history
			if rbErr := c.db.RollbackToLastSuccessful(ctx, ev.GetNodeUuid(), ev.GetUserId(),
				p.ConfigApplyResult.GetErrorMessage()); rbErr != nil {
				logrus.WithError(rbErr).Error("kafka: failed to record rollback in DB")
				return wrapDatabaseError(rbErr)
			}
		}

		return nil

	case *eventsv1.NodeEvent_RuntimeError:
		inserted, err := c.db.InsertNodeEvent(ctx, db.NodeEventRow{
			NodeUUID:      ev.GetNodeUuid(),
			UserID:        ev.GetUserId(),
			EventType:     eventTypeString(ev.GetType()),
			Module:        p.RuntimeError.GetModule(),
			ErrorCode:     p.RuntimeError.GetErrorCode(),
			ErrorMessage:  p.RuntimeError.GetErrorMessage(),
			Context:       p.RuntimeError.GetContext(),
			CorrelationID: ev.GetCorrelationId(),
			EventTime:     eventTime,
		})
		if err == nil && !inserted {
			logrus.WithFields(logrus.Fields{
				"node":           ev.GetNodeUuid(),
				"user":           ev.GetUserId(),
				"event_type":     eventTypeString(ev.GetType()),
				"correlation_id": ev.GetCorrelationId(),
			}).Info("kafka: duplicate node event ignored")
		}
		return wrapDatabaseError(err)

	default:
		logrus.WithField("type", ev.GetType()).Warn("kafka: event with no/unknown payload, skipping")
		return nil
	}
}

func (c *Consumer) handleConfigOfflineEdit(
	ctx context.Context,
	ev *eventsv1.NodeEvent,
	edit *eventsv1.ConfigOfflineEdit,
	eventTime time.Time,
) error {
	proposal, err := prepareOfflineEdit(ev, edit)
	if err != nil {
		return fmt.Errorf("poison ConfigOfflineEdit: %w", err)
	}
	if c.etcd == nil {
		return wrapEtcdError(errors.New("etcd client is not configured for ConfigOfflineEdit"))
	}

	casErr := c.etcd.CAS(ctx, proposal.key, func(current []byte) (storage.CASResult, error) {
		return offlineEditMutation(proposal, current, time.Now())
	})
	switch {
	case casErr == nil:
		if proposal.deleted {
			if err := storage.DeleteSubscriberDNS(ctx, c.etcd, proposal.nodeID, proposal.userID); err != nil {
				return wrapEtcdError(err)
			}
			logrus.WithFields(logrus.Fields{"node": proposal.nodeID, "user": proposal.userID}).Info(
				"kafka: accepted offline HSI tombstone and cascaded DNS deletion",
			)
		} else {
			logrus.WithFields(logrus.Fields{
				"node": proposal.nodeID,
				"user": proposal.userID,
				"kind": offlineEditKindString(proposal.kind),
			}).Info("kafka: accepted offline config edit")
		}
		return nil

	case errors.Is(casErr, errOfflineEditNoop):
		// A replayed tombstone still repairs a DNS orphan left by a crash after
		// the HSI delete but before the original cascade completed.
		if proposal.deleted {
			if err := storage.DeleteSubscriberDNS(ctx, c.etcd, proposal.nodeID, proposal.userID); err != nil {
				return wrapEtcdError(err)
			}
		}
		logrus.WithFields(logrus.Fields{
			"node": proposal.nodeID,
			"user": proposal.userID,
			"kind": offlineEditKindString(proposal.kind),
		}).Info("kafka: offline edit replay is already reflected in etcd")
		return nil

	case errors.Is(casErr, errOfflineEditDiscard):
		var discardErr *offlineEditDiscardError
		if !errors.As(casErr, &discardErr) {
			return fmt.Errorf("offline edit discard has no reason: %w", casErr)
		}
		return c.recordOfflineEditDiscard(ctx, proposal, eventTime, discardErr)

	case errors.Is(casErr, storage.ErrCASConflict):
		// A reachable etcd that repeatedly conflicts is not an infrastructure
		// outage. Leave this unwrapped so the normal bounded retries send the
		// poison/stuck message to kafka_dlq and advance the partition.
		return casErr

	default:
		return wrapEtcdError(casErr)
	}
}

func (c *Consumer) recordOfflineEditDiscard(
	ctx context.Context,
	proposal offlineEditProposal,
	eventTime time.Time,
	discardErr *offlineEditDiscardError,
) error {
	if c.db == nil {
		return wrapDatabaseError(errors.New("database is not configured for offline-edit audit events"))
	}
	contextJSON, err := json.Marshal(map[string]string{
		"kind":         offlineEditKindString(proposal.kind),
		"edit_summary": proposal.editSummary,
		"reason":       discardErr.reason,
	})
	if err != nil {
		return fmt.Errorf("encode offline-edit discard context: %w", err)
	}
	success := false
	inserted, err := c.db.InsertNodeEvent(ctx, db.NodeEventRow{
		NodeUUID:     proposal.nodeID,
		UserID:       proposal.userID,
		EventType:    "CONFIG_OFFLINE_EDIT",
		Action:       "discard",
		Success:      &success,
		Module:       offlineEditKindString(proposal.kind),
		ErrorCode:    "OFFLINE_EDIT_DISCARDED",
		ErrorMessage: discardErr.reason,
		Context:      string(contextJSON),
		// Deliberately empty: the arbitration result and existing event-time
		// uniqueness key make at-least-once replay idempotent without requiring
		// producers to invent a correlation ID for a cumulative offline period.
		CorrelationID: "",
		EventTime:     eventTime,
	})
	if err != nil {
		return wrapDatabaseError(err)
	}

	entry := logrus.WithFields(logrus.Fields{
		"node":         proposal.nodeID,
		"user":         proposal.userID,
		"kind":         offlineEditKindString(proposal.kind),
		"edit_summary": proposal.editSummary,
		"reason":       discardErr.reason,
		"inserted":     inserted,
	})
	if discardErr.warn {
		entry.Warn("kafka: discarded offline edit because current arbitration metadata is unusable")
	} else {
		entry.Info("kafka: discarded offline edit in favor of controller state")
	}
	return nil
}

func offlineEditKindString(kind eventsv1.OfflineEditKind) string {
	return strings.TrimPrefix(kind.String(), "OFFLINE_EDIT_KIND_")
}

func (c *Consumer) waitAndSendToDLQ(ctx context.Context, m kafka.Message) {
	for ctx.Err() == nil {
		if err := c.sendToDLQ(ctx, m); err != nil {
			logrus.WithError(err).Error("kafka: failed to send message to DLQ, retrying")
			c.sleep(ctx)
			continue
		}
		return
	}
}

// sendToDLQ records a failed message to the database for human investigation.
// The caller commits the Kafka offset only after this succeeds.
func (c *Consumer) sendToDLQ(ctx context.Context, m kafka.Message) error {
	if c.db == nil {
		// No database configured, can't send to DLQ. Just log and move on.
		logrus.Warnf("kafka: cannot send to DLQ - database not configured. "+
			"Topic=%s Partition=%d Offset=%d", m.Topic, m.Partition, m.Offset)
		return nil
	}

	dlqID, err := c.db.SendToDLQ(ctx, m.Topic, m.Partition, m.Offset, m.Value,
		"Failed after max retries while handling Kafka message")
	if err != nil {
		return err
	}

	logrus.Warnf("kafka: message sent to DLQ (dlq_id=%d topic=%s partition=%d offset=%d)",
		dlqID, m.Topic, m.Partition, m.Offset)
	return nil
}

// phaseString maps the PPPoEPhase enum to the stored phase string.
func phaseString(p eventsv1.PPPoEPhase) string {
	switch p {
	case eventsv1.PPPoEPhase_PPPOE_PHASE_CONNECTING:
		return "connecting"
	case eventsv1.PPPoEPhase_PPPOE_PHASE_CONNECTED:
		return "connected"
	case eventsv1.PPPoEPhase_PPPOE_PHASE_DISCONNECTING:
		return "disconnecting"
	case eventsv1.PPPoEPhase_PPPOE_PHASE_DISCONNECTED:
		return "disconnected"
	default:
		return "unspecified"
	}
}

// eventTypeString maps the EventType enum to the stored event_type string,
// stripping the EVENT_TYPE_ prefix.
func eventTypeString(t eventsv1.EventType) string {
	name := t.String()
	return strings.TrimPrefix(name, "EVENT_TYPE_")
}
