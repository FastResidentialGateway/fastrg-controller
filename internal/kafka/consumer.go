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
	"strconv"
	"strings"
	"time"

	"fastrg-controller/internal/db"
	"fastrg-controller/internal/storage"
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

// guardedRollbackMutation decides whether the current etcd value is still the
// single write immediately following the last node-confirmed version. It must
// run inside storage.CAS so a concurrent write causes a retry with the newest
// value and therefore a fresh guard evaluation.
//
// This is a transitional resourceVersion-only guard. A delete followed by a
// recreate can reset resourceVersion to 1 and is indistinguishable from the
// original first failed create; the event schema needs an applied revision to
// close that remaining cross-repository race.
func guardedRollbackMutation(prevConfig *db.HSIConfigRow, current []byte) (storage.CASResult, error) {
	if current == nil {
		return storage.CASResult{}, skipRollback("config key no longer exists", false)
	}

	curRV, err := configResourceVersion(current)
	if err != nil {
		return storage.CASResult{}, skipRollback("cannot establish current resourceVersion: "+err.Error(), true)
	}

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
		if prevConfig == nil {
			return storage.CASResult{Delete: true}, nil
		}
		return storage.CASResult{Value: prevConfig.ConfigJSON}, nil
	}

	return storage.CASResult{}, skipRollback(
		fmt.Sprintf("current resourceVersion %d is not the failed successor of last successful resourceVersion %d", curRV, okRV),
		false,
	)
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
				rbErr := c.etcd.CAS(ctx, etcdKey, func(current []byte) (storage.CASResult, error) {
					return guardedRollbackMutation(prevConfig, current)
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
