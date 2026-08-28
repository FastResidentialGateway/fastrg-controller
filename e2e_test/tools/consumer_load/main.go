// Command consumer_load measures how long the controller's Kafka consumer needs
// to work through a burst of PPPoE state events, which is what a fleet-wide
// republish looks like from the database's side.
//
// It drives the production code: the events go through a real Kafka topic, and
// the real internal/kafka consumer projects them into a real PostgreSQL. Nothing
// here reimplements the consuming path, so the number it prints is the number
// the controller would get.
//
// Everything it touches is scoped to one run — its own topic, its own consumer
// group, and node UUIDs carrying a per-run prefix — so it never reads or
// rewrites another run's rows and never deletes anything.
//
// Usage:
//
//	go run ./e2e_test/tools/consumer_load \
//	    -brokers localhost:9092 \
//	    -dsn 'postgres://fastrg:fastrg@localhost:5432/fastrg?sslmode=disable' \
//	    -nodes 100 -users 1000
package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"os"
	"strings"
	"time"

	"fastrg-controller/internal/db"
	fastrgkafka "fastrg-controller/internal/kafka"
	eventsv1 "fastrg-controller/proto/eventsv1"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/segmentio/kafka-go"
	"github.com/sirupsen/logrus"
	"google.golang.org/protobuf/proto"
)

func main() {
	brokers := flag.String("brokers", "localhost:9092", "comma-separated Kafka brokers")
	dsn := flag.String("dsn", "", "PostgreSQL DSN (required)")
	nodes := flag.Int("nodes", 100, "number of distinct node UUIDs")
	users := flag.Int("users", 1000, "number of subscribers per node")
	batch := flag.Int("batch", 1000, "messages per produce call")
	partitions := flag.Int("partitions", 3, "partitions on the run's topic")
	timeout := flag.Duration("timeout", 30*time.Minute, "give up if the consumer has not caught up by then")
	pollInterval := flag.Duration("poll", 2*time.Second, "how often to check progress")
	flag.Parse()

	if *dsn == "" {
		log.Fatal("-dsn is required")
	}
	// The consumer is chatty at info level and would bury the measurement.
	logrus.SetLevel(logrus.WarnLevel)

	runID := time.Now().UTC().Format("20060102150405")
	nodePrefix := "loadtest-" + runID + "-"
	topic := "fastrg.loadtest." + runID
	group := "fastrg-loadtest-" + runID
	total := *nodes * *users

	brokerList := splitAndTrim(*brokers)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	fmt.Printf("run id:   %s\n", runID)
	fmt.Printf("topic:    %s (group %s)\n", topic, group)
	fmt.Printf("events:   %d (%d nodes x %d subscribers)\n", total, *nodes, *users)

	if err := createTopic(ctx, brokerList, topic, *partitions); err != nil {
		log.Fatalf("create topic: %v", err)
	}

	produceElapsed, err := produce(ctx, brokerList, topic, nodePrefix, *nodes, *users, *batch)
	if err != nil {
		log.Fatalf("produce: %v", err)
	}
	fmt.Printf("produced: %d events in %s (%.0f events/s)\n",
		total, produceElapsed.Round(time.Millisecond), float64(total)/produceElapsed.Seconds())

	database, err := db.New(ctx, *dsn)
	if err != nil {
		log.Fatalf("open database: %v", err)
	}
	defer database.Close()

	pool, err := pgxpool.New(ctx, *dsn)
	if err != nil {
		log.Fatalf("open counting pool: %v", err)
	}
	defer pool.Close()

	// NewConsumer reads the topic and group from the environment, so this is how
	// the run is pointed at its own scratch topic.
	os.Setenv("KAFKA_TOPIC", topic)
	os.Setenv("KAFKA_GROUP", group)
	consumer := fastrgkafka.NewConsumer(brokerList, database, nil)

	consumerCtx, stopConsumer := context.WithCancel(ctx)
	defer stopConsumer()
	consumeStart := time.Now()
	go consumer.Run(consumerCtx)

	rows, peakFetchAge, consumeElapsed, err := waitForCatchUp(
		ctx, pool, nodePrefix, total, *timeout, *pollInterval, consumeStart,
	)
	stopConsumer()

	// The consumer's own log is the only place a shutdown problem would show, so
	// give it a moment to finish before the process exits.
	time.Sleep(time.Second)

	status := "OK"
	if err != nil {
		status = "TIMEOUT"
		fmt.Printf("error:    %v\n", err)
	}
	fmt.Printf("consumed: %d/%d rows in %s\n", rows, total, consumeElapsed.Round(time.Millisecond))
	fmt.Printf("RESULT %s events=%d rows=%d produce_seconds=%.1f consume_seconds=%.1f rows_per_second=%.0f peak_fetch_age_seconds=%.1f\n",
		status, total, rows, produceElapsed.Seconds(), consumeElapsed.Seconds(),
		float64(rows)/consumeElapsed.Seconds(), peakFetchAge)

	if err != nil {
		os.Exit(1)
	}
}

func splitAndTrim(list string) []string {
	parts := strings.Split(list, ",")
	for i := range parts {
		parts[i] = strings.TrimSpace(parts[i])
	}
	return parts
}

// createTopic creates the run's own topic and waits until the brokers report
// its partitions. Producing into a topic that is only half-created fails with
// "unknown topic or partition", so this is done up front rather than relying on
// broker-side auto-creation.
func createTopic(ctx context.Context, brokers []string, topic string, partitions int) error {
	client := &kafka.Client{Addr: kafka.TCP(brokers...)}
	_, err := client.CreateTopics(ctx, &kafka.CreateTopicsRequest{
		Topics: []kafka.TopicConfig{{
			Topic:             topic,
			NumPartitions:     partitions,
			ReplicationFactor: 1,
		}},
	})
	if err != nil {
		return err
	}

	deadline := time.Now().Add(30 * time.Second)
	for time.Now().Before(deadline) {
		conn, err := kafka.DialContext(ctx, "tcp", brokers[0])
		if err == nil {
			parts, err := conn.ReadPartitions(topic)
			conn.Close()
			if err == nil && len(parts) >= partitions {
				return nil
			}
		}
		time.Sleep(time.Second)
	}
	return fmt.Errorf("topic %q did not report %d partition(s) in time", topic, partitions)
}

// produce writes one PPPoE state event per (node, subscriber). Every event has
// its own key, so catching up means writing one row each — the worst case for
// the consumer, with no upsert able to collapse two events into one write.
func produce(
	ctx context.Context,
	brokers []string,
	topic, nodePrefix string,
	nodes, users, batch int,
) (time.Duration, error) {
	writer := &kafka.Writer{
		Addr:     kafka.TCP(brokers...),
		Topic:    topic,
		Balancer: &kafka.Hash{},
		// The run's topic does not exist yet; let the first write create it.
		AllowAutoTopicCreation: true,
		BatchSize:              batch,
		BatchTimeout:           50 * time.Millisecond,
		RequiredAcks:           kafka.RequireOne,
		// A freshly created topic reports "unknown partition" for a moment.
		MaxAttempts: 10,
	}
	defer writer.Close()

	eventTime := time.Now().Unix()
	messages := make([]kafka.Message, 0, batch)
	start := time.Now()

	for n := range nodes {
		nodeUUID := fmt.Sprintf("%s%03d", nodePrefix, n)
		for u := range users {
			value, err := proto.Marshal(&eventsv1.NodeEvent{
				NodeUuid:  nodeUUID,
				UserId:    fmt.Sprint(u + 1),
				Type:      eventsv1.EventType_EVENT_TYPE_PPPOE_CONNECTED,
				Timestamp: eventTime,
				Payload: &eventsv1.NodeEvent_PppoeStateChange{
					PppoeStateChange: &eventsv1.PPPoEStateChange{
						Phase:      eventsv1.PPPoEPhase_PPPOE_PHASE_CONNECTED,
						HsiIpv4:    fmt.Sprintf("10.%d.%d.%d", n%256, (u/256)%256, u%256),
						HsiIpv4Gw:  "10.0.0.1",
						HsiIpv6Dns: "2001:db8::53",
					},
				},
			})
			if err != nil {
				return time.Since(start), err
			}
			messages = append(messages, kafka.Message{Key: []byte(nodeUUID), Value: value})
			if len(messages) == batch {
				if err := writer.WriteMessages(ctx, messages...); err != nil {
					return time.Since(start), err
				}
				messages = messages[:0]
			}
		}
	}
	if len(messages) > 0 {
		if err := writer.WriteMessages(ctx, messages...); err != nil {
			return time.Since(start), err
		}
	}
	return time.Since(start), nil
}

// waitForCatchUp polls until this run's rows are all present, reporting the row
// count, the highest fetch age seen, and how long the catch-up took. The row
// count is the completion signal: the consumer keeps running whether or not it
// has anything left to do, so nothing it prints could stand in for it.
func waitForCatchUp(
	ctx context.Context,
	pool *pgxpool.Pool,
	nodePrefix string,
	want int,
	timeout, pollInterval time.Duration,
	start time.Time,
) (rows int, peakFetchAge float64, elapsed time.Duration, err error) {
	deadline := time.Now().Add(timeout)
	lastReport := time.Now()

	for {
		rows = countRows(ctx, pool, nodePrefix)
		if age, ok := fetchAgeSeconds(); ok && age > peakFetchAge {
			peakFetchAge = age
		}
		if rows >= want {
			return rows, peakFetchAge, time.Since(start), nil
		}
		if time.Now().After(deadline) {
			return rows, peakFetchAge, time.Since(start), fmt.Errorf(
				"consumer still %d row(s) short after %s", want-rows, timeout)
		}
		if time.Since(lastReport) >= 10*time.Second {
			fmt.Printf("progress: %d/%d rows after %s\n",
				rows, want, time.Since(start).Round(time.Second))
			lastReport = time.Now()
		}
		select {
		case <-ctx.Done():
			return rows, peakFetchAge, time.Since(start), ctx.Err()
		case <-time.After(pollInterval):
		}
	}
}

func countRows(ctx context.Context, pool *pgxpool.Pool, nodePrefix string) int {
	var count int
	err := pool.QueryRow(ctx,
		`SELECT count(*) FROM pppoe_status WHERE node_uuid LIKE $1`, nodePrefix+"%",
	).Scan(&count)
	if err != nil {
		return 0
	}
	return count
}

// fetchAgeSeconds reads the consumer's own fetch-age gauge out of the process
// registry, which is the same value the controller exports for alerting.
func fetchAgeSeconds() (float64, bool) {
	families, err := prometheus.DefaultGatherer.Gather()
	if err != nil {
		return 0, false
	}
	for _, family := range families {
		if family.GetName() != "fastrg_kafka_consumer_last_fetch_age_seconds" {
			continue
		}
		for _, metric := range family.GetMetric() {
			if metric.GetGauge() != nil {
				return metric.GetGauge().GetValue(), true
			}
		}
	}
	return 0, false
}
