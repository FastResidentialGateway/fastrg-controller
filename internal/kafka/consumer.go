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
	"os"
	"strings"
	"time"

	"fastrg-controller/internal/db"
	eventsv1 "fastrg-controller/proto/eventsv1"

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
func NewConsumer(brokers []string, database *db.DB) *Consumer {
	reader := kafka.NewReader(kafka.ReaderConfig{
		Brokers:  brokers,
		Topic:    envOr("KAFKA_TOPIC", defaultTopic),
		GroupID:  envOr("KAFKA_GROUP", defaultGroupID),
		MinBytes: 1,
		MaxBytes: 10e6,
	})
	return &Consumer{reader: reader, db: database}
}

// Run consumes until ctx is cancelled, then closes the reader.
func (c *Consumer) Run(ctx context.Context) {
	logrus.Info("Started Kafka consumer for node events")
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

		// Retry the same message until it is durably handled, so we never commit
		// past an event we failed to persist. Handling is idempotent.
		for ctx.Err() == nil {
			if err := c.handle(ctx, m.Value); err != nil {
				logrus.WithError(err).Error("kafka: handle failed, retrying message")
				c.sleep(ctx)
				continue
			}
			break
		}
		if ctx.Err() != nil {
			return
		}

		if err := c.reader.CommitMessages(ctx, m); err != nil {
			logrus.WithError(err).Error("kafka: commit failed")
		}
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
		return c.db.UpsertPPPoEStatus(ctx, db.PPPoEStatusRow{
			NodeUUID:     ev.GetNodeUuid(),
			UserID:       ev.GetUserId(),
			Phase:        phaseString(p.PppoeStateChange.GetPhase()),
			HSIIPv4:      p.PppoeStateChange.GetHsiIpv4(),
			HSIIPv4GW:    p.PppoeStateChange.GetHsiIpv4Gw(),
			ErrorMessage: p.PppoeStateChange.GetErrorMessage(),
			EventTime:    eventTime,
		})

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
			return err
		}

		// If config apply failed, automatically rollback to the last successful version
		// to prevent invalid config from remaining in etcd.
		if !success {
			logrus.Warnf("kafka: config apply failed for node=%s user=%s, rolling back to last successful version",
				ev.GetNodeUuid(), ev.GetUserId())
			if rbErr := c.db.RollbackToLastSuccessful(ctx, ev.GetNodeUuid(), ev.GetUserId(),
				p.ConfigApplyResult.GetErrorMessage()); rbErr != nil {
				logrus.WithError(rbErr).Error("kafka: rollback failed")
				return rbErr
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
		return err

	default:
		logrus.WithField("type", ev.GetType()).Warn("kafka: event with no/unknown payload, skipping")
		return nil
	}
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
