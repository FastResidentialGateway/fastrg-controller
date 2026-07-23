package kafka

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"testing"
	"time"

	"fastrg-controller/internal/storage"
	eventsv1 "fastrg-controller/proto/eventsv1"

	"google.golang.org/protobuf/proto"
)

const (
	offlineCurrentTime = "2026-07-22T10:00:00Z"
	offlineLaterUnix   = int64(1784714460) // 2026-07-22T10:01:00Z
)

func offlineContentEvent(kind eventsv1.OfflineEditKind, node, user, rv, configJSON string) (*eventsv1.NodeEvent, *eventsv1.ConfigOfflineEdit) {
	edit := &eventsv1.ConfigOfflineEdit{
		Kind:            kind,
		ConfigJson:      configJSON,
		ResourceVersion: rv,
		EditedAt:        offlineLaterUnix,
		EditSummary:     "cumulative offline edit",
	}
	ev := &eventsv1.NodeEvent{
		NodeUuid: node,
		UserId:   user,
		Type:     eventsv1.EventType_EVENT_TYPE_CONFIG_OFFLINE_EDIT,
		Payload:  &eventsv1.NodeEvent_ConfigOfflineEdit{ConfigOfflineEdit: edit},
	}
	return ev, edit
}

func preparedHSIProposal(t *testing.T, rv, payload string) offlineEditProposal {
	t.Helper()
	configJSON := `{"metadata":{"node":"untrusted","updatedAt":"1999-01-01T00:00:00Z","resourceVersion":"` + rv + `"},"config":` + payload + `}`
	ev, edit := offlineContentEvent(eventsv1.OfflineEditKind_OFFLINE_EDIT_KIND_HSI_CONFIG, "node-1", "7", rv, configJSON)
	proposal, err := prepareOfflineEdit(ev, edit)
	if err != nil {
		t.Fatalf("prepareOfflineEdit: %v", err)
	}
	return proposal
}

func currentHSI(rv, updatedAt, payload string) []byte {
	return []byte(`{"config":` + payload + `,"metadata":{"node":"node-1","resourceVersion":"` + rv + `","updatedBy":"controller","updatedAt":"` + updatedAt + `"}}`)
}

func requireOfflineDiscard(t *testing.T, err error, wantWarn bool) *offlineEditDiscardError {
	t.Helper()
	if !errors.Is(err, errOfflineEditDiscard) {
		t.Fatalf("error = %v, want errOfflineEditDiscard", err)
	}
	var discard *offlineEditDiscardError
	if !errors.As(err, &discard) {
		t.Fatalf("error %T does not expose offlineEditDiscardError", err)
	}
	if discard.warn != wantWarn {
		t.Fatalf("discard.warn = %v, want %v (reason=%q)", discard.warn, wantWarn, discard.reason)
	}
	return discard
}

func TestOfflineContentEditArbitration(t *testing.T) {
	now := time.Date(2026, 7, 22, 11, 0, 0, 0, time.UTC)
	tests := []struct {
		name       string
		proposalRV string
		editedAt   int64
		current    []byte
		wantNoop   bool
		wantApply  bool
		wantWarn   bool
	}{
		{
			name:       "canonical payload equal is no-op before malformed metadata",
			proposalRV: "bad-node-rv",
			current:    []byte(`{"metadata":{"resourceVersion":"bad","updatedAt":"bad"},"config":{"nested":{"a":1,"b":2},"enabled":true}}`),
			wantNoop:   true,
		},
		{
			name:       "resource version passes timestamp guard fails",
			proposalRV: "6", editedAt: time.Date(2026, 7, 22, 9, 59, 59, 0, time.UTC).Unix(),
			current: currentHSI("5", offlineCurrentTime, `{"different":true}`),
		},
		{
			name:       "timestamp passes resource version guard fails",
			proposalRV: "5", editedAt: offlineLaterUnix,
			current: currentHSI("5", offlineCurrentTime, `{"different":true}`),
		},
		{
			name:       "both guards pass",
			proposalRV: "6", editedAt: offlineLaterUnix,
			current:   currentHSI("5", offlineCurrentTime, `{"different":true}`),
			wantApply: true,
		},
		{
			name:       "missing current key is discarded",
			proposalRV: "6", editedAt: offlineLaterUnix, current: nil,
		},
		{
			name:       "bad node resource version fails closed",
			proposalRV: "bad", editedAt: offlineLaterUnix,
			current:  currentHSI("5", offlineCurrentTime, `{"different":true}`),
			wantWarn: true,
		},
		{
			name:       "bad current resource version fails closed",
			proposalRV: "6", editedAt: offlineLaterUnix,
			current:  currentHSI("bad", offlineCurrentTime, `{"different":true}`),
			wantWarn: true,
		},
		{
			name:       "bad current updatedAt fails closed",
			proposalRV: "6", editedAt: offlineLaterUnix,
			current:  currentHSI("5", "not-a-time", `{"different":true}`),
			wantWarn: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			proposal := preparedHSIProposal(t, tt.proposalRV, `{"enabled":true,"nested":{"b":2,"a":1}}`)
			proposal.editedAt = time.Unix(tt.editedAt, 0).UTC()
			result, err := offlineEditMutation(proposal, tt.current, now)
			switch {
			case tt.wantNoop:
				if !errors.Is(err, errOfflineEditNoop) {
					t.Fatalf("error = %v, want errOfflineEditNoop", err)
				}
				if len(result.Value) != 0 || result.Delete {
					t.Fatalf("no-op returned a write: %+v", result)
				}
			case tt.wantApply:
				if err != nil {
					t.Fatalf("offlineEditMutation: %v", err)
				}
				var got struct {
					Config   any            `json:"config"`
					Metadata configMetadata `json:"metadata"`
				}
				if err := json.Unmarshal(result.Value, &got); err != nil {
					t.Fatalf("decode applied value: %v", err)
				}
				var wantPayload any
				if err := json.Unmarshal([]byte(`{"enabled":true,"nested":{"b":2,"a":1}}`), &wantPayload); err != nil {
					t.Fatal(err)
				}
				if !reflect.DeepEqual(got.Config, wantPayload) {
					t.Fatalf("payload = %#v, want %#v", got.Config, wantPayload)
				}
				if got.Metadata.Node != "node-1" || got.Metadata.ResourceVersion != "6" ||
					got.Metadata.UpdatedBy != offlineEditUpdatedBy || got.Metadata.UpdatedAt != now.Format(time.RFC3339) {
					t.Fatalf("metadata was not freshly stamped from current/envelope: %+v", got.Metadata)
				}
			default:
				requireOfflineDiscard(t, err, tt.wantWarn)
				if len(result.Value) != 0 || result.Delete {
					t.Fatalf("discard returned a write: %+v", result)
				}
			}
		})
	}
}

func TestOfflineTombstoneArbitration(t *testing.T) {
	now := time.Date(2026, 7, 22, 11, 0, 0, 0, time.UTC)
	base := preparedHSIProposal(t, "5", `{"user_id":"7"}`)
	base.deleted = true
	base.payload = nil
	base.canonicalPayload = nil

	tests := []struct {
		name       string
		nodeRV     string
		editedAt   int64
		current    []byte
		wantNoop   bool
		wantDelete bool
		wantWarn   bool
	}{
		{name: "missing current is idempotent no-op", nodeRV: "5", editedAt: offlineLaterUnix, current: nil, wantNoop: true},
		{name: "equal resource version and later timestamp deletes", nodeRV: "5", editedAt: offlineLaterUnix, current: currentHSI("5", offlineCurrentTime, `{}`), wantDelete: true},
		{name: "behind resource version is discarded", nodeRV: "4", editedAt: offlineLaterUnix, current: currentHSI("5", offlineCurrentTime, `{}`)},
		{name: "equal timestamp is discarded", nodeRV: "5", editedAt: time.Date(2026, 7, 22, 10, 0, 0, 0, time.UTC).Unix(), current: currentHSI("5", offlineCurrentTime, `{}`)},
		{name: "bad resource version is discarded with warning", nodeRV: "bad", editedAt: offlineLaterUnix, current: currentHSI("5", offlineCurrentTime, `{}`), wantWarn: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			proposal := base
			proposal.resourceVersion = tt.nodeRV
			proposal.editedAt = time.Unix(tt.editedAt, 0).UTC()
			result, err := offlineEditMutation(proposal, tt.current, now)
			switch {
			case tt.wantNoop:
				if !errors.Is(err, errOfflineEditNoop) {
					t.Fatalf("error = %v, want errOfflineEditNoop", err)
				}
			case tt.wantDelete:
				if err != nil || !result.Delete {
					t.Fatalf("result = %+v, error = %v, want delete", result, err)
				}
			default:
				requireOfflineDiscard(t, err, tt.wantWarn)
			}
		})
	}
}

func TestPrepareOfflineEditRejectsPoisonMessages(t *testing.T) {
	validJSON := `{"config":{"user_id":"7"},"metadata":{"resourceVersion":"2"}}`
	tests := []struct {
		name string
		ev   *eventsv1.NodeEvent
		edit *eventsv1.ConfigOfflineEdit
	}{
		func() struct {
			name string
			ev   *eventsv1.NodeEvent
			edit *eventsv1.ConfigOfflineEdit
		} {
			ev, edit := offlineContentEvent(eventsv1.OfflineEditKind_OFFLINE_EDIT_KIND_HSI_CONFIG, "node-1", "7", "3", validJSON)
			return struct {
				name string
				ev   *eventsv1.NodeEvent
				edit *eventsv1.ConfigOfflineEdit
			}{"resource version mismatch", ev, edit}
		}(),
		func() struct {
			name string
			ev   *eventsv1.NodeEvent
			edit *eventsv1.ConfigOfflineEdit
		} {
			ev, edit := offlineContentEvent(eventsv1.OfflineEditKind_OFFLINE_EDIT_KIND_HSI_CONFIG, "node-1", "7", "2", "not-empty")
			edit.Deleted = true
			return struct {
				name string
				ev   *eventsv1.NodeEvent
				edit *eventsv1.ConfigOfflineEdit
			}{"tombstone with config_json", ev, edit}
		}(),
		func() struct {
			name string
			ev   *eventsv1.NodeEvent
			edit *eventsv1.ConfigOfflineEdit
		} {
			ev, edit := offlineContentEvent(eventsv1.OfflineEditKind_OFFLINE_EDIT_KIND_DNS_RECORDS, "node-1", "7", "2", "")
			edit.Deleted = true
			return struct {
				name string
				ev   *eventsv1.NodeEvent
				edit *eventsv1.ConfigOfflineEdit
			}{"non-HSI tombstone", ev, edit}
		}(),
		func() struct {
			name string
			ev   *eventsv1.NodeEvent
			edit *eventsv1.ConfigOfflineEdit
		} {
			ev, edit := offlineContentEvent(eventsv1.OfflineEditKind_OFFLINE_EDIT_KIND_SUBSCRIBER_COUNT, "node-1", "7", "2", `{"subscriber_count":"10","metadata":{"resourceVersion":"2"}}`)
			return struct {
				name string
				ev   *eventsv1.NodeEvent
				edit *eventsv1.ConfigOfflineEdit
			}{"subscriber count with nonzero user", ev, edit}
		}(),
		func() struct {
			name string
			ev   *eventsv1.NodeEvent
			edit *eventsv1.ConfigOfflineEdit
		} {
			ev, edit := offlineContentEvent(eventsv1.OfflineEditKind_OFFLINE_EDIT_KIND_HSI_CONFIG, "bad/node", "7", "2", validJSON)
			return struct {
				name string
				ev   *eventsv1.NodeEvent
				edit *eventsv1.ConfigOfflineEdit
			}{"invalid envelope node id", ev, edit}
		}(),
		func() struct {
			name string
			ev   *eventsv1.NodeEvent
			edit *eventsv1.ConfigOfflineEdit
		} {
			ev, edit := offlineContentEvent(eventsv1.OfflineEditKind_OFFLINE_EDIT_KIND_HSI_CONFIG, "node-1", "bad-user", "2", validJSON)
			return struct {
				name string
				ev   *eventsv1.NodeEvent
				edit *eventsv1.ConfigOfflineEdit
			}{"invalid envelope user id", ev, edit}
		}(),
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if _, err := prepareOfflineEdit(tt.ev, tt.edit); err == nil {
				t.Fatal("prepareOfflineEdit succeeded, want poison-message error")
			}
		})
	}
}

func TestPrepareOfflineEditDerivesTargetsOnlyFromEnvelope(t *testing.T) {
	tests := []struct {
		kind eventsv1.OfflineEditKind
		user string
		json string
		key  string
	}{
		{eventsv1.OfflineEditKind_OFFLINE_EDIT_KIND_HSI_CONFIG, "7", `{"config":{"node":"attacker"},"metadata":{"resourceVersion":"2"}}`, "configs/node-1/hsi/7"},
		{eventsv1.OfflineEditKind_OFFLINE_EDIT_KIND_DNS_RECORDS, "7", `{"records":[],"metadata":{"resourceVersion":"2"}}`, "configs/node-1/dns/7"},
		{eventsv1.OfflineEditKind_OFFLINE_EDIT_KIND_SUBSCRIBER_COUNT, "0", `{"subscriber_count":"10","metadata":{"resourceVersion":"2"}}`, "user_counts/node-1/"},
	}
	for _, tt := range tests {
		ev, edit := offlineContentEvent(tt.kind, "node-1", tt.user, "2", tt.json)
		proposal, err := prepareOfflineEdit(ev, edit)
		if err != nil {
			t.Fatalf("kind %s: %v", tt.kind, err)
		}
		if proposal.key != tt.key {
			t.Fatalf("kind %s key = %q, want %q", tt.kind, proposal.key, tt.key)
		}
	}
}

func TestOfflineEditCASConflictIsDLQEligible(t *testing.T) {
	if isEtcdUnavailable(storage.ErrCASConflict) {
		t.Fatal("ErrCASConflict must remain a bounded-retry/DLQ error, not an infrastructure-unavailable error")
	}
	if !isEtcdUnavailable(wrapEtcdError(errors.New("connection refused"))) {
		t.Fatal("wrapped etcd connection failure must be retried without committing")
	}
}

func TestPoisonOfflineEditIsDLQEligible(t *testing.T) {
	ev, _ := offlineContentEvent(
		eventsv1.OfflineEditKind_OFFLINE_EDIT_KIND_HSI_CONFIG,
		"node-1", "7", "3",
		`{"config":{"user_id":"7"},"metadata":{"resourceVersion":"2"}}`,
	)
	value, err := proto.Marshal(ev)
	if err != nil {
		t.Fatalf("marshal poison event: %v", err)
	}
	err = (&Consumer{}).handle(context.Background(), value)
	if err == nil {
		t.Fatal("poison event succeeded, want bounded-retry/DLQ error")
	}
	if isEtcdUnavailable(err) || isDatabaseUnavailable(err) {
		t.Fatalf("poison event was misclassified as unavailable infrastructure: %v", err)
	}
}
