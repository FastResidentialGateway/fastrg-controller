// kafka_produce sends a single binary Kafka message from a base64-encoded payload.
// Usage: kafka_produce <base64-payload>
//
// Reads KAFKA_BROKERS (default localhost:9092) and KAFKA_TOPIC from environment.
// Unlike kafka-producer-perf-test.sh --payload-file, this handles binary payloads
// that contain 0x0A (newline) bytes without truncation.
package main

import (
	"context"
	"encoding/base64"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/segmentio/kafka-go"
)

func main() {
	if len(os.Args) < 2 {
		fmt.Fprintln(os.Stderr, "usage: kafka_produce <base64-payload>")
		fmt.Fprintln(os.Stderr, "env:   KAFKA_BROKERS (default: localhost:9092), KAFKA_TOPIC (required)")
		os.Exit(1)
	}

	brokersEnv := os.Getenv("KAFKA_BROKERS")
	if brokersEnv == "" {
		brokersEnv = "localhost:9092"
	}
	brokers := strings.Split(brokersEnv, ",")
	for i := range brokers {
		brokers[i] = strings.TrimSpace(brokers[i])
	}

	topic := os.Getenv("KAFKA_TOPIC")
	if topic == "" {
		fmt.Fprintln(os.Stderr, "KAFKA_TOPIC must be set")
		os.Exit(1)
	}

	payload, err := base64.StdEncoding.DecodeString(os.Args[1])
	if err != nil {
		fmt.Fprintf(os.Stderr, "base64 decode: %v\n", err)
		os.Exit(1)
	}

	w := &kafka.Writer{
		Addr:         kafka.TCP(brokers...),
		Topic:        topic,
		RequiredAcks: kafka.RequireOne,
	}
	defer w.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	if err := w.WriteMessages(ctx, kafka.Message{Value: payload}); err != nil {
		fmt.Fprintf(os.Stderr, "write: %v\n", err)
		os.Exit(1)
	}
	fmt.Printf("produced 1 message (%d bytes) to %s\n", len(payload), topic)
}
