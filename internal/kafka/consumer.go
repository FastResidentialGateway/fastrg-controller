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
	"errors"
	"fmt"
	"os"
	"strings"
	"time"

	"fastrg-controller/internal/db"
	"fastrg-controller/internal/storage"
	eventsv1 "fastrg-controller/proto/eventsv1"

	"github.com/jackc/pgx/v5/pgconn"
	"github.com/segmentio/kafka-go"
	"github.com/sirupsen/logrus"
	"google.golang.org/protobuf/proto"
)

const (
	defaultTopic   = "fastrg.node.events"
	defaultGroupID = "fastrg-controller"
	retryBackoff   = 2 * time.Second
)

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

		// Retry the same message with exponential backoff. If the DB is
		// unavailable, keep retrying this message without committing the offset.
		// If PostgreSQL is reachable and returned a SQL error, persist the
		// message to DLQ before committing the offset.
		const maxRetries = 5
		backoff := 100 * time.Millisecond
		failed := false
		for attempt := 1; ctx.Err() == nil; {
			if err := c.handle(ctx, m.Value); err != nil {
				failed = true

				if isDatabaseUnavailable(err) {
					logrus.WithError(err).Warn("kafka: database unavailable, retrying same message")
					c.sleep(ctx)
					continue
				}

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
			failed = false
			break
		}

		// If still failed after retries, send to DLQ (database persistent queue).
		if failed && ctx.Err() == nil {
			logrus.Errorf("kafka: message failed after %d retries, sending to DLQ", maxRetries)
			c.waitAndSendToDLQ(ctx, m)
		}
		if ctx.Err() != nil {
			return
		}

		if err := c.reader.CommitMessages(ctx, m); err != nil {
			logrus.WithError(err).Error("kafka: commit failed")
		}
	}
}

// isDatabaseUnavailable distinguishes transient connectivity/pool failures from
// SQL errors returned by a reachable PostgreSQL server. Reachable SQL errors can
// still be dead-lettered into kafka_dlq; unavailable DB errors cannot, because
// kafka_dlq is stored in the same PostgreSQL instance.
func isDatabaseUnavailable(err error) bool {
	if err == nil {
		return false
	}

	var dbErr databaseOperationError
	if !errors.As(err, &dbErr) {
		return false
	}

	var pgErr *pgconn.PgError
	return !errors.As(err, &pgErr)
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

// waitForTopicReady blocks until the Kafka topic has at least 1 partition or
// ctx is cancelled.  This prevents the consumer group from joining before the
// topic exists, which would assign 0 partitions and silently drop all messages.
func (c *Consumer) waitForTopicReady(ctx context.Context, brokers []string, topic string) {
	for ctx.Err() == nil {
		conn, err := kafka.DialContext(ctx, "tcp", brokers[0])
		if err != nil {
			logrus.WithError(err).Warn("kafka: waiting for broker before joining consumer group")
			c.sleep(ctx)
			continue
		}
		partitions, err := conn.ReadPartitions(topic)
		conn.Close()
		if err == nil && len(partitions) > 0 {
			logrus.Infof("kafka: topic %q ready (%d partition(s)), joining consumer group", topic, len(partitions))
			return
		}
		logrus.Warnf("kafka: topic %q not yet available (partitions=%d), waiting", topic, len(partitions))
		c.sleep(ctx)
	}
}

func (c *Consumer) sleep(ctx context.Context) {
	select {
	case <-ctx.Done():
	case <-time.After(retryBackoff):
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
		_, err := c.db.InsertNodeEvent(ctx, db.NodeEventRow{
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
					return err
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
					if err := c.db.UpsertCurrent(ctx, row); err != nil {
						logrus.WithError(err).Error("kafka: failed to update hsi_config_current after CONFIG_APPLY_OK")
						return wrapDatabaseError(err)
					}
					// Record this success in history. Database operation is idempotent
					// (AppendHistoryWithStatus uses ON CONFLICT to avoid duplicates if
					// consumer retries after database restart).
					if err := c.db.AppendHistoryWithStatus(ctx, row, "success"); err != nil {
						logrus.WithError(err).Error("kafka: failed to record success in history after CONFIG_APPLY_OK")
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
			} else if prevConfig != nil {
				// Restore the last successful config to etcd using CAS
				etcdKey := fmt.Sprintf("configs/%s/hsi/%s", ev.GetNodeUuid(), ev.GetUserId())
				if rbErr := c.etcd.CAS(ctx, etcdKey, func(current []byte) (storage.CASResult, error) {
					// Always overwrite with the last successful version (ignore current revision)
					return storage.CASResult{Value: []byte(prevConfig.ConfigJSON)}, nil
				}); rbErr != nil {
					logrus.WithError(rbErr).Error("kafka: failed to restore config to etcd")
					return rbErr
				}
				logrus.Infof("kafka: config rolled back to last successful version in etcd for node=%s user=%s",
					ev.GetNodeUuid(), ev.GetUserId())
			} else {
				// No successful previous version: delete from etcd
				if rbErr := c.etcd.CAS(ctx, fmt.Sprintf("configs/%s/hsi/%s", ev.GetNodeUuid(), ev.GetUserId()),
					func(current []byte) (storage.CASResult, error) {
						return storage.CASResult{Delete: true}, nil
					}); rbErr != nil {
					logrus.WithError(rbErr).Error("kafka: failed to delete invalid config from etcd")
					return rbErr
				}
				logrus.Infof("kafka: invalid config deleted from etcd for node=%s user=%s (no successful version)",
					ev.GetNodeUuid(), ev.GetUserId())
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
		_, err := c.db.InsertNodeEvent(ctx, db.NodeEventRow{
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
