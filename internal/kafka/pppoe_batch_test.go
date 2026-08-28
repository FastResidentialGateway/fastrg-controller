package kafka

import (
	"testing"
	"time"

	eventsv1 "fastrg-controller/proto/eventsv1"

	"github.com/segmentio/kafka-go"
	"google.golang.org/protobuf/proto"
)

// pppoeMessage builds one Kafka message carrying a PPPoE state change.
func pppoeMessage(t *testing.T, node, user string, phase eventsv1.PPPoEPhase, ts int64, ipv4 string) kafka.Message {
	t.Helper()
	value, err := proto.Marshal(&eventsv1.NodeEvent{
		NodeUuid:  node,
		UserId:    user,
		Type:      eventsv1.EventType_EVENT_TYPE_PPPOE_CONNECTED,
		Timestamp: ts,
		Payload: &eventsv1.NodeEvent_PppoeStateChange{
			PppoeStateChange: &eventsv1.PPPoEStateChange{Phase: phase, HsiIpv4: ipv4},
		},
	})
	if err != nil {
		t.Fatalf("marshal PPPoE event: %v", err)
	}
	return kafka.Message{Value: value}
}

// TestPPPoERowsFromBatchKeepsOneRowPerSubscriber: PostgreSQL will not let one
// statement touch a row twice, so repeats within a batch collapse to the state
// that would have survived one-at-a-time writes.
func TestPPPoERowsFromBatchKeepsOneRowPerSubscriber(t *testing.T) {
	base := time.Now().Unix()
	batch := []kafka.Message{
		pppoeMessage(t, "node-a", "1", eventsv1.PPPoEPhase_PPPOE_PHASE_CONNECTING, base, ""),
		pppoeMessage(t, "node-a", "2", eventsv1.PPPoEPhase_PPPOE_PHASE_CONNECTED, base, "10.0.0.2"),
		pppoeMessage(t, "node-a", "1", eventsv1.PPPoEPhase_PPPOE_PHASE_CONNECTED, base+1, "10.0.0.1"),
		pppoeMessage(t, "node-b", "1", eventsv1.PPPoEPhase_PPPOE_PHASE_DISCONNECTED, base, ""),
	}

	rows, ok := pppoeRowsFromBatch(batch)
	if !ok {
		t.Fatal("pppoeRowsFromBatch rejected a batch of PPPoE events")
	}
	if len(rows) != 3 {
		t.Fatalf("rows = %d, want 3 (one per subscriber)", len(rows))
	}
	for _, row := range rows {
		if row.NodeUUID == "node-a" && row.UserID == "1" {
			if row.Phase != "connected" || row.HSIIPv4 != "10.0.0.1" {
				t.Fatalf("node-a user 1 = %+v, want the later connected state", row)
			}
		}
	}
}

// TestPPPoERowsFromBatchTieBreaksOnArrivalOrder: PPPoE timestamps have
// one-second resolution, so two transitions for the same subscriber often share
// an event_time. The row's guard lets an equal timestamp overwrite, so the last
// one to arrive has to win here too.
func TestPPPoERowsFromBatchTieBreaksOnArrivalOrder(t *testing.T) {
	ts := time.Now().Unix()
	batch := []kafka.Message{
		pppoeMessage(t, "node-a", "1", eventsv1.PPPoEPhase_PPPOE_PHASE_CONNECTING, ts, ""),
		pppoeMessage(t, "node-a", "1", eventsv1.PPPoEPhase_PPPOE_PHASE_CONNECTED, ts, "10.0.0.1"),
	}

	rows, ok := pppoeRowsFromBatch(batch)
	if !ok {
		t.Fatal("pppoeRowsFromBatch rejected a batch of PPPoE events")
	}
	if len(rows) != 1 {
		t.Fatalf("rows = %d, want 1", len(rows))
	}
	if rows[0].Phase != "connected" {
		t.Fatalf("phase = %q, want %q — the later message must win an equal timestamp", rows[0].Phase, "connected")
	}
}

// TestPPPoERowsFromBatchKeepsTheNewestEventTime: an out-of-order redelivery must
// not drag the row back to an older state.
func TestPPPoERowsFromBatchKeepsTheNewestEventTime(t *testing.T) {
	base := time.Now().Unix()
	batch := []kafka.Message{
		pppoeMessage(t, "node-a", "1", eventsv1.PPPoEPhase_PPPOE_PHASE_CONNECTED, base+10, "10.0.0.1"),
		pppoeMessage(t, "node-a", "1", eventsv1.PPPoEPhase_PPPOE_PHASE_DISCONNECTED, base, ""),
	}

	rows, ok := pppoeRowsFromBatch(batch)
	if !ok {
		t.Fatal("pppoeRowsFromBatch rejected a batch of PPPoE events")
	}
	if len(rows) != 1 || rows[0].Phase != "connected" {
		t.Fatalf("rows = %+v, want the newer connected state", rows)
	}
}

// TestPPPoERowsFromBatchRejectsOtherEvents: anything that is not a PPPoE state
// change needs the full handler, so the whole batch falls back.
func TestPPPoERowsFromBatchRejectsOtherEvents(t *testing.T) {
	applyEvent, err := proto.Marshal(configApplyEvent("node-a", "1", true, time.Now().Unix()))
	if err != nil {
		t.Fatalf("marshal config apply event: %v", err)
	}

	tests := map[string][]kafka.Message{
		"config apply result": {
			pppoeMessage(t, "node-a", "1", eventsv1.PPPoEPhase_PPPOE_PHASE_CONNECTED, time.Now().Unix(), ""),
			{Value: applyEvent},
		},
		"undecodable message": {
			pppoeMessage(t, "node-a", "1", eventsv1.PPPoEPhase_PPPOE_PHASE_CONNECTED, time.Now().Unix(), ""),
			{Value: []byte("not a protobuf message at all")},
		},
		"event with no payload": {
			{Value: mustMarshalEmptyEvent(t)},
		},
	}

	for name, batch := range tests {
		t.Run(name, func(t *testing.T) {
			if _, ok := pppoeRowsFromBatch(batch); ok {
				t.Fatal("pppoeRowsFromBatch accepted a batch it cannot write on its own")
			}
		})
	}
}

func mustMarshalEmptyEvent(t *testing.T) []byte {
	t.Helper()
	value, err := proto.Marshal(&eventsv1.NodeEvent{NodeUuid: "node-a", UserId: "1"})
	if err != nil {
		t.Fatalf("marshal empty event: %v", err)
	}
	return value
}
