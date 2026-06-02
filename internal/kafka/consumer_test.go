package kafka

import (
	"testing"

	eventsv1 "fastrg-controller/proto/eventsv1"
)

func TestPhaseString(t *testing.T) {
	cases := map[eventsv1.PPPoEPhase]string{
		eventsv1.PPPoEPhase_PPPOE_PHASE_CONNECTING:    "connecting",
		eventsv1.PPPoEPhase_PPPOE_PHASE_CONNECTED:     "connected",
		eventsv1.PPPoEPhase_PPPOE_PHASE_DISCONNECTING: "disconnecting",
		eventsv1.PPPoEPhase_PPPOE_PHASE_DISCONNECTED:  "disconnected",
		eventsv1.PPPoEPhase_PPPOE_PHASE_UNSPECIFIED:   "unspecified",
	}
	for phase, want := range cases {
		if got := phaseString(phase); got != want {
			t.Errorf("phaseString(%v) = %q, want %q", phase, got, want)
		}
	}
}

func TestEventTypeString(t *testing.T) {
	cases := map[eventsv1.EventType]string{
		eventsv1.EventType_EVENT_TYPE_CONFIG_APPLY_OK:   "CONFIG_APPLY_OK",
		eventsv1.EventType_EVENT_TYPE_CONFIG_APPLY_FAIL: "CONFIG_APPLY_FAIL",
		eventsv1.EventType_EVENT_TYPE_RUNTIME_ERROR:     "RUNTIME_ERROR",
		eventsv1.EventType_EVENT_TYPE_PPPOE_CONNECTED:   "PPPOE_CONNECTED",
	}
	for et, want := range cases {
		if got := eventTypeString(et); got != want {
			t.Errorf("eventTypeString(%v) = %q, want %q", et, got, want)
		}
	}
}

func TestBrokersParsing(t *testing.T) {
	t.Setenv("KAFKA_BROKERS", "")
	if Brokers() != nil {
		t.Error("empty KAFKA_BROKERS should yield nil")
	}
	t.Setenv("KAFKA_BROKERS", "a:9092, b:9092 ,c:9092")
	got := Brokers()
	want := []string{"a:9092", "b:9092", "c:9092"}
	if len(got) != len(want) {
		t.Fatalf("Brokers() = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("Brokers()[%d] = %q, want %q", i, got[i], want[i])
		}
	}
}
