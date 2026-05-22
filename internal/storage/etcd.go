package storage

import (
	"context"
	"encoding/json"
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

// FailedEvent represents a failed event from a node instance
type FailedEvent struct {
	EventType       string `json:"event_type"`
	OriginalKey     string `json:"original_key"`
	NodeID          string `json:"node_id"`
	UserID          string `json:"user_id"`
	ErrorReasonCode int    `json:"error_reason_code"`
	ErrorReasonName string `json:"error_reason_name"`
	ErrorDetail     string `json:"error_detail"`
	Timestamp       int64  `json:"timestamp"`
	Timezone        string `json:"timezone,omitempty"`
	OriginalValue   string `json:"original_value,omitempty"`
}

// FailedEventHandler is a callback function for handling failed events
type FailedEventHandler func(event *FailedEvent, key string, eventType string)

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

// WatchFailedEvents watches for failed events with the prefix "failed_events/"
// Calls the handler for each event (PUT, DELETE, etc.)
func (e *EtcdClient) WatchFailedEvents(ctx context.Context, handler FailedEventHandler) error {
	watchChan := e.client.Watch(ctx, "failed_events/", clientv3.WithPrefix())

	logrus.Info("Started watching failed_events/")

	for watchResp := range watchChan {
		if watchResp.Err() != nil {
			logrus.WithError(watchResp.Err()).Error("Watch error on failed_events/")
			return watchResp.Err()
		}

		for _, event := range watchResp.Events {
			key := string(event.Kv.Key)
			value := string(event.Kv.Value)

			var eventTypeStr string
			switch event.Type {
			case clientv3.EventTypePut:
				eventTypeStr = "PUT"
			case clientv3.EventTypeDelete:
				eventTypeStr = "DELETE"
			default:
				eventTypeStr = "UNKNOWN"
			}

			logrus.WithFields(logrus.Fields{
				"key":       key,
				"eventType": eventTypeStr,
			}).Debug("Received failed event")

			// Parse the failed event JSON
			var failedEvent FailedEvent
			if err := json.Unmarshal([]byte(value), &failedEvent); err != nil {
				logrus.WithError(err).WithFields(logrus.Fields{
					"key":   key,
					"value": value,
				}).Warn("Failed to parse failed event JSON")
				continue
			}

			// Call the handler
			if handler != nil {
				handler(&failedEvent, key, eventTypeStr)
			}
		}
	}

	return nil
}

func (e *EtcdClient) processFailedEvent(event *FailedEvent, key string, eventType string) {
	logrus.WithFields(logrus.Fields{
		"event_type":   event.EventType,
		"node_id":      event.NodeID,
		"user_id":      event.UserID,
		"error_code":   event.ErrorReasonCode,
		"error_name":   event.ErrorReasonName,
		"error_detail": event.ErrorDetail,
		"key":          key,
	}).Warn("Failed event detected from node")

	// Store in etcd for web frontend access
	// Key format: failed_events_history/{node_id}/{timestamp}
	historyKey := fmt.Sprintf("failed_events_history/%s/%d", event.NodeID, event.Timestamp)
	eventJSON, err := json.Marshal(event)
	if err != nil {
		logrus.WithError(err).Error("Failed to marshal failed event for history")
		return
	}

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	// Store with 7 days TTL (604800 seconds)
	lease, err := e.client.Grant(ctx, 604800)
	if err != nil {
		logrus.WithError(err).Error("Failed to create lease for failed event history")
		return
	}

	_, err = e.client.Put(ctx, historyKey, string(eventJSON), clientv3.WithLease(lease.ID))
	if err != nil {
		logrus.WithError(err).Error("Failed to store failed event history")
		return
	}

	logrus.WithField("history_key", historyKey).Debug("Stored failed event in history")
}

func StartFailedEventsWatcher(etcd *EtcdClient) context.CancelFunc {
	// Start failed events watcher
	ctx, cancelWatcher := context.WithCancel(context.Background())
	go func() {
		if err := etcd.WatchFailedEvents(ctx, etcd.processFailedEvent); err != nil {
			if ctx.Err() != context.Canceled {
				logrus.WithError(err).Error("Failed events watcher stopped with error")
			}
		}
	}()
	logrus.Info("Failed events watcher started")

	return cancelWatcher
}
