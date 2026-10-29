package kafka

import (
	"context"
	"fmt"

	kafkago "github.com/segmentio/kafka-go"

	"github.com/IQVO/customer-payments/internal/application/outbox"
)

// Headers converts the stored outbox headers to Kafka headers.
func Headers(hs []outbox.Header) []kafkago.Header {
	out := make([]kafkago.Header, len(hs))
	for i, h := range hs {
		out[i] = kafkago.Header{Key: h.Key, Value: []byte(h.Value)}
	}
	return out
}

// Writer is the part of *kafkago.Writer the relay sink needs.
type Writer interface {
	WriteMessages(ctx context.Context, msgs ...kafkago.Message) error
}

// Sink writes outbox messages to Kafka; each message names its own topic, so
// the writer behind it has none (see NewWriter).
type Sink struct{ W Writer }

// Send writes msgs, in order, and returns when the broker acknowledged them.
func (s Sink) Send(ctx context.Context, msgs ...outbox.Message) error {
	out := make([]kafkago.Message, len(msgs))
	for i, m := range msgs {
		out[i] = kafkago.Message{Topic: m.Topic, Key: m.Key, Value: m.Value, Headers: Headers(m.Headers)}
	}
	if err := s.W.WriteMessages(ctx, out...); err != nil {
		return fmt.Errorf("write %d messages to kafka: %w", len(out), err)
	}
	return nil
}
