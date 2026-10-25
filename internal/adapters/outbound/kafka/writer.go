// Package kafka holds the outbound Kafka adapters. The outbox relay publishes
// through the Writer built here; the topic is chosen per message, never on the
// Writer, so one Writer serves the events and analytics topics.
package kafka

import (
	"strings"
	"time"

	kafkago "github.com/segmentio/kafka-go"
)

// batchTimeout is the writer's BatchTimeout. kafka-go's default (1s) would cap
// a relay that sends one row per call at about one event per second; 10ms
// keeps writes batched under load while a lone event flushes at once.
const batchTimeout = 10 * time.Millisecond

// NewWriter builds the shared Kafka writer. RequiredAcks is All and the key
// balancer is Hash, so every event of one aggregate lands on one partition in
// order (the aggregate id is the message key). AllowAutoTopicCreation matches
// the broker, which creates business topics on first write.
func NewWriter(brokers string) *kafkago.Writer {
	return &kafkago.Writer{
		Addr:                   kafkago.TCP(splitBrokers(brokers)...),
		RequiredAcks:           kafkago.RequireAll,
		Balancer:               &kafkago.Hash{},
		BatchTimeout:           batchTimeout,
		AllowAutoTopicCreation: true,
	}
}

func splitBrokers(s string) []string {
	var out []string
	for _, b := range strings.Split(s, ",") {
		if b = strings.TrimSpace(b); b != "" {
			out = append(out, b)
		}
	}
	return out
}
