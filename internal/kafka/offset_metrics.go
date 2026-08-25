package kafka

import (
	"context"
	"strconv"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
	"github.com/segmentio/kafka-go"
	"github.com/sirupsen/logrus"
)

// offsetSampleInterval is how often consumer health is re-measured. It is far
// shorter than the time an operator needs to notice an alert, and each round is
// three cheap broker lookups.
const offsetSampleInterval = 30 * time.Second

var (
	kafkaOffsetBeyondLogEnd = promauto.NewGaugeVec(prometheus.GaugeOpts{
		Name: "fastrg_kafka_consumer_offset_beyond_log_end",
		Help: "How far the consumer group's committed offset is past the partition's log end. Above 0 means the broker lost data the group had already acknowledged.",
	}, []string{"partition"})
	kafkaOffsetCheckErrorsTotal = promauto.NewCounter(prometheus.CounterOpts{
		Name: "fastrg_kafka_consumer_offset_check_errors_total",
		Help: "Total failed attempts to read the consumer group's offsets from the brokers.",
	})
	kafkaConsumerLastFetchAgeSeconds = promauto.NewGauge(prometheus.GaugeOpts{
		Name: "fastrg_kafka_consumer_last_fetch_age_seconds",
		Help: "Seconds since the consumer last fetched a message. It also grows on an idle topic, so it only indicates a wedged consumer together with the other consumer metrics.",
	})
)

// offsetClient is the part of kafka.Client the offset reads use. Naming it
// keeps the read path testable against a fake broker.
type offsetClient interface {
	Metadata(context.Context, *kafka.MetadataRequest) (*kafka.MetadataResponse, error)
	ListOffsets(context.Context, *kafka.ListOffsetsRequest) (*kafka.ListOffsetsResponse, error)
	OffsetFetch(context.Context, *kafka.OffsetFetchRequest) (*kafka.OffsetFetchResponse, error)
}

// partitionOffsets pairs one partition's log bounds with the consumer group's
// committed offset. Committed is negative when the group never committed there.
type partitionOffsets struct {
	Partition int
	First     int64
	Last      int64
	Committed int64
}

// readNegativeLag reads the log bounds and committed offsets of every partition
// of topic for group. It only reads, so both the startup guard and the periodic
// health sampler use it.
func readNegativeLag(ctx context.Context, client offsetClient, topic, group string) ([]partitionOffsets, error) {
	meta, err := client.Metadata(ctx, &kafka.MetadataRequest{Topics: []string{topic}})
	if err != nil {
		return nil, err
	}
	var partitions []int
	for _, t := range meta.Topics {
		if t.Name != topic || t.Error != nil {
			continue
		}
		for _, part := range t.Partitions {
			partitions = append(partitions, part.ID)
		}
	}
	if len(partitions) == 0 {
		return nil, nil
	}

	offReqs := make([]kafka.OffsetRequest, 0, len(partitions)*2)
	for _, part := range partitions {
		offReqs = append(offReqs, kafka.FirstOffsetOf(part), kafka.LastOffsetOf(part))
	}
	ranges, err := client.ListOffsets(ctx, &kafka.ListOffsetsRequest{
		Topics: map[string][]kafka.OffsetRequest{topic: offReqs},
	})
	if err != nil {
		return nil, err
	}
	firstLast := map[int][2]int64{}
	for _, po := range ranges.Topics[topic] {
		if po.Error != nil {
			continue
		}
		firstLast[po.Partition] = [2]int64{po.FirstOffset, po.LastOffset}
	}

	fetched, err := client.OffsetFetch(ctx, &kafka.OffsetFetchRequest{
		GroupID: group,
		Topics:  map[string][]int{topic: partitions},
	})
	if err != nil {
		return nil, err
	}

	var offsets []partitionOffsets
	for _, of := range fetched.Topics[topic] {
		if of.Error != nil {
			continue
		}
		bounds, ok := firstLast[of.Partition]
		if !ok {
			continue // no usable log bounds for this partition
		}
		offsets = append(offsets, partitionOffsets{
			Partition: of.Partition,
			First:     bounds[0],
			Last:      bounds[1],
			Committed: of.CommittedOffset,
		})
	}
	return offsets, nil
}

// isBeyondLogEnd reports whether the group's committed offset points past the
// end of the surviving log, which happens only when the broker dropped data the
// group had already acknowledged.
func isBeyondLogEnd(off partitionOffsets) bool {
	return off.Committed >= 0 && off.Committed > off.Last
}

// partitionsToReset selects the partitions whose committed offset sits beyond
// the log end — the broker dropped data the group had already acknowledged —
// and snaps each back to the first offset that survived. Partitions without a
// committed offset have nothing to repair.
func partitionsToReset(offsets []partitionOffsets) []kafka.OffsetCommit {
	var resets []kafka.OffsetCommit
	for _, off := range offsets {
		if !isBeyondLogEnd(off) {
			continue
		}
		logrus.Warnf("kafka: negative-lag guard: partition %d committed offset %d is beyond log end %d (broker data loss); resetting to first offset %d and replaying",
			off.Partition, off.Committed, off.Last, off.First)
		resets = append(resets, kafka.OffsetCommit{Partition: off.Partition, Offset: off.First})
	}
	return resets
}

// offsetsBeyondLogEnd reports per partition how many messages the committed
// offset is past the log end. Healthy partitions report 0, including those the
// group has never committed on.
func offsetsBeyondLogEnd(offsets []partitionOffsets) map[int]float64 {
	beyond := make(map[int]float64, len(offsets))
	for _, off := range offsets {
		if !isBeyondLogEnd(off) {
			beyond[off.Partition] = 0
			continue
		}
		beyond[off.Partition] = float64(off.Committed - off.Last)
	}
	return beyond
}

// sampleOffsetMetrics refreshes the consumer-health metrics every
// offsetSampleInterval until ctx is cancelled. It never repairs anything: a
// truncated log during a run is an alert, and the repair is a controller
// restart, whose startup path both resets the offsets and republishes state.
func (c *Consumer) sampleOffsetMetrics(ctx context.Context, cfg kafka.ReaderConfig) {
	if len(cfg.Brokers) == 0 || cfg.GroupID == "" {
		return
	}
	client := &kafka.Client{Addr: kafka.TCP(cfg.Brokers...)}

	ticker := time.NewTicker(offsetSampleInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
		c.sampleOffsetsOnce(ctx, client, cfg.Topic, cfg.GroupID, time.Now())
	}
}

// sampleOffsetsOnce records one round of consumer-health metrics.
func (c *Consumer) sampleOffsetsOnce(ctx context.Context, client offsetClient, topic, group string, now time.Time) {
	kafkaConsumerLastFetchAgeSeconds.Set(c.fetchAge(now).Seconds())

	offsets, err := readNegativeLag(ctx, client, topic, group)
	if err != nil {
		kafkaOffsetCheckErrorsTotal.Inc()
		logrus.WithError(err).Warn("kafka: could not read consumer group offsets")
		return
	}
	for partition, beyond := range offsetsBeyondLogEnd(offsets) {
		kafkaOffsetBeyondLogEnd.WithLabelValues(strconv.Itoa(partition)).Set(beyond)
	}
}

// markFetch records the moment a fetch succeeded.
func (c *Consumer) markFetch(now time.Time) {
	c.lastFetch.Store(now.UnixNano())
}

// fetchAge is how long ago the consumer last fetched a message. Run seeds it
// when it starts, so the age is meaningful before the first message arrives.
func (c *Consumer) fetchAge(now time.Time) time.Duration {
	last := c.lastFetch.Load()
	if last == 0 {
		return 0
	}
	age := now.Sub(time.Unix(0, last))
	if age < 0 {
		return 0
	}
	return age
}
