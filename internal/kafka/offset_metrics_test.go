package kafka

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus/testutil"
	"github.com/segmentio/kafka-go"
)

// fakeOffsetClient answers the three broker lookups readNegativeLag makes.
type fakeOffsetClient struct {
	topic      string
	partitions []int
	firstLast  map[int][2]int64
	committed  map[int]int64
	err        error
}

func (f *fakeOffsetClient) Metadata(context.Context, *kafka.MetadataRequest) (*kafka.MetadataResponse, error) {
	if f.err != nil {
		return nil, f.err
	}
	topic := kafka.Topic{Name: f.topic}
	for _, id := range f.partitions {
		topic.Partitions = append(topic.Partitions, kafka.Partition{ID: id})
	}
	return &kafka.MetadataResponse{Topics: []kafka.Topic{topic}}, nil
}

func (f *fakeOffsetClient) ListOffsets(context.Context, *kafka.ListOffsetsRequest) (*kafka.ListOffsetsResponse, error) {
	resp := &kafka.ListOffsetsResponse{Topics: map[string][]kafka.PartitionOffsets{}}
	for _, id := range f.partitions {
		bounds, ok := f.firstLast[id]
		if !ok {
			continue
		}
		resp.Topics[f.topic] = append(resp.Topics[f.topic], kafka.PartitionOffsets{
			Partition:   id,
			FirstOffset: bounds[0],
			LastOffset:  bounds[1],
		})
	}
	return resp, nil
}

func (f *fakeOffsetClient) OffsetFetch(context.Context, *kafka.OffsetFetchRequest) (*kafka.OffsetFetchResponse, error) {
	resp := &kafka.OffsetFetchResponse{Topics: map[string][]kafka.OffsetFetchPartition{}}
	for _, id := range f.partitions {
		committed, ok := f.committed[id]
		if !ok {
			committed = -1 // group never committed on this partition
		}
		resp.Topics[f.topic] = append(resp.Topics[f.topic], kafka.OffsetFetchPartition{
			Partition:       id,
			CommittedOffset: committed,
		})
	}
	return resp, nil
}

// TestPartitionsToReset: only a committed offset past the log end is repaired,
// and it is snapped back to the first surviving offset.
func TestPartitionsToReset(t *testing.T) {
	offsets := []partitionOffsets{
		{Partition: 0, First: 40, Last: 100, Committed: 250}, // broker lost data
		{Partition: 1, First: 0, Last: 100, Committed: 100},  // caught up
		{Partition: 2, First: 0, Last: 100, Committed: 7},    // normal lag
		{Partition: 3, First: 5, Last: 100, Committed: -1},   // never committed
	}

	resets := partitionsToReset(offsets)
	if len(resets) != 1 {
		t.Fatalf("resets = %+v, want exactly partition 0", resets)
	}
	if resets[0].Partition != 0 {
		t.Fatalf("reset partition = %d, want 0", resets[0].Partition)
	}
	if resets[0].Offset != 40 {
		t.Fatalf("reset offset = %d, want the first surviving offset 40", resets[0].Offset)
	}

	if got := partitionsToReset(nil); len(got) != 0 {
		t.Fatalf("resets for no partitions = %+v, want none", got)
	}
}

// TestOffsetsBeyondLogEnd: the gauge value is the distance past the log end,
// and every healthy partition reports 0.
func TestOffsetsBeyondLogEnd(t *testing.T) {
	beyond := offsetsBeyondLogEnd([]partitionOffsets{
		{Partition: 0, First: 40, Last: 100, Committed: 250},
		{Partition: 1, First: 0, Last: 100, Committed: 100},
		{Partition: 2, First: 0, Last: 100, Committed: 7},
		{Partition: 3, First: 5, Last: 100, Committed: -1},
	})

	want := map[int]float64{0: 150, 1: 0, 2: 0, 3: 0}
	if len(beyond) != len(want) {
		t.Fatalf("beyond = %v, want %v", beyond, want)
	}
	for partition, wantValue := range want {
		if beyond[partition] != wantValue {
			t.Fatalf("partition %d beyond log end = %v, want %v", partition, beyond[partition], wantValue)
		}
	}
}

// TestReadNegativeLagCollectsOffsets: the shared read pairs each partition's
// log bounds with the group's committed offset.
func TestReadNegativeLagCollectsOffsets(t *testing.T) {
	client := &fakeOffsetClient{
		topic:      "events",
		partitions: []int{0, 1},
		firstLast:  map[int][2]int64{0: {40, 100}, 1: {0, 12}},
		committed:  map[int]int64{0: 250},
	}

	offsets, err := readNegativeLag(context.Background(), client, "events", "group")
	if err != nil {
		t.Fatalf("readNegativeLag: %v", err)
	}
	if len(offsets) != 2 {
		t.Fatalf("offsets = %+v, want 2 partitions", offsets)
	}
	byPartition := map[int]partitionOffsets{}
	for _, off := range offsets {
		byPartition[off.Partition] = off
	}
	if got := byPartition[0]; got.First != 40 || got.Last != 100 || got.Committed != 250 {
		t.Fatalf("partition 0 = %+v, want first=40 last=100 committed=250", got)
	}
	if got := byPartition[1]; got.Committed >= 0 {
		t.Fatalf("partition 1 committed = %d, want negative (never committed)", got.Committed)
	}
}

// TestSampleOffsetsOnceRecordsGauges: a healthy sample sets the per-partition
// gauge and the fetch age; a broker lookup failure counts as a check error.
func TestSampleOffsetsOnceRecordsGauges(t *testing.T) {
	kafkaOffsetBeyondLogEnd.Reset()
	kafkaConsumerLastFetchAgeSeconds.Set(0)
	t.Cleanup(func() {
		kafkaOffsetBeyondLogEnd.Reset()
		kafkaConsumerLastFetchAgeSeconds.Set(0)
	})

	consumer := &Consumer{}
	now := time.Date(2026, time.August, 23, 0, 0, 0, 0, time.UTC)
	consumer.markFetch(now.Add(-45 * time.Second))

	client := &fakeOffsetClient{
		topic:      "events",
		partitions: []int{0, 1},
		firstLast:  map[int][2]int64{0: {40, 100}, 1: {0, 12}},
		committed:  map[int]int64{0: 250, 1: 12},
	}
	consumer.sampleOffsetsOnce(context.Background(), client, "events", "group", now)

	if got := testutil.ToFloat64(kafkaOffsetBeyondLogEnd.WithLabelValues("0")); got != 150 {
		t.Fatalf("partition 0 gauge = %v, want 150", got)
	}
	if got := testutil.ToFloat64(kafkaOffsetBeyondLogEnd.WithLabelValues("1")); got != 0 {
		t.Fatalf("partition 1 gauge = %v, want 0", got)
	}
	if got := testutil.ToFloat64(kafkaConsumerLastFetchAgeSeconds); got != 45 {
		t.Fatalf("last fetch age = %v, want 45", got)
	}

	errorsBefore := testutil.ToFloat64(kafkaOffsetCheckErrorsTotal)
	consumer.sampleOffsetsOnce(context.Background(), &fakeOffsetClient{err: errors.New("brokers unreachable")}, "events", "group", now)
	if got := testutil.ToFloat64(kafkaOffsetCheckErrorsTotal); got != errorsBefore+1 {
		t.Fatalf("offset check errors = %v, want %v", got, errorsBefore+1)
	}
}

// TestFetchAgeStartsAtSession: the age is measured from the last fetch, and a
// consumer that has never fetched reports 0 rather than a huge number.
func TestFetchAgeStartsAtSession(t *testing.T) {
	consumer := &Consumer{}
	now := time.Date(2026, time.August, 23, 0, 0, 0, 0, time.UTC)

	if got := consumer.fetchAge(now); got != 0 {
		t.Fatalf("fetch age before any fetch = %v, want 0", got)
	}
	consumer.markFetch(now.Add(-2 * time.Second))
	if got := consumer.fetchAge(now); got != 2*time.Second {
		t.Fatalf("fetch age = %v, want 2s", got)
	}
	// A clock that moved backwards must not produce a negative age.
	if got := consumer.fetchAge(now.Add(-10 * time.Second)); got != 0 {
		t.Fatalf("fetch age with earlier now = %v, want 0", got)
	}
}

// TestBeginSessionRepublishesOnce: the consumer's startup path asks the nodes
// to re-send their PPPoE state exactly once, and a consumer without a
// republish callback still starts.
func TestBeginSessionRepublishesOnce(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	calls := make(chan struct{}, 4)
	consumer := &Consumer{}
	consumer.SetRepublishAll(func(context.Context) { calls <- struct{}{} })

	consumer.beginSession(ctx, kafka.ReaderConfig{}, time.Now())

	select {
	case <-calls:
	case <-time.After(2 * time.Second):
		t.Fatal("beginSession did not request a republish")
	}
	select {
	case <-calls:
		t.Fatal("beginSession requested more than one republish")
	case <-time.After(100 * time.Millisecond):
	}

	// Without a callback the session still starts (Kafka-only deployments).
	(&Consumer{}).beginSession(ctx, kafka.ReaderConfig{}, time.Now())
}
