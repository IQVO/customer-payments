package kafka

import (
	"context"

	kafkago "github.com/segmentio/kafka-go"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/propagation"
	semconv "go.opentelemetry.io/otel/semconv/v1.43.0"
	"go.opentelemetry.io/otel/trace"
)

// consumerTracerName is the instrumentation scope of the consumer spans.
const consumerTracerName = "customer-payments/inbound/kafka"

// headerCarrier adapts a kafka-go header slice to propagation.TextMapCarrier
// for READING the W3C traceparent/tracestate the producer's outbox relay
// published, so the consumer span becomes a child of the span that enqueued the
// event. kafka-go has no OTel instrumentation of its own. It is read-only: Set
// is a no-op because a consumer never re-injects into the message it read.
type headerCarrier struct {
	headers []kafkago.Header
}

var _ propagation.TextMapCarrier = headerCarrier{}

// Get returns the value of the first header with the given key, or "".
func (c headerCarrier) Get(key string) string {
	for _, h := range c.headers {
		if h.Key == key {
			return string(h.Value)
		}
	}
	return ""
}

// Set is a no-op: see the type comment.
func (c headerCarrier) Set(string, string) {}

// Keys lists every header key present.
func (c headerCarrier) Keys() []string {
	keys := make([]string, 0, len(c.headers))
	for _, h := range c.headers {
		keys = append(keys, h.Key)
	}
	return keys
}

// handleTraced runs one handling attempt of msg inside a consumer span named
// "<topic> process", parented to the trace carried in the message headers (a
// message without them starts a new trace). The ctx the handlers receive
// carries that span, so the use case's logs get its trace_id and any event the
// handler enqueues continues the same trace. A returned error marks the span.
func (c *Consumer) handleTraced(ctx context.Context, msg kafkago.Message) error {
	topic := msg.Topic
	if topic == "" {
		topic = c.Topic
	}
	ctx = otel.GetTextMapPropagator().Extract(ctx, headerCarrier{headers: msg.Headers})
	ctx, span := otel.Tracer(consumerTracerName).Start(ctx, topic+" process",
		trace.WithSpanKind(trace.SpanKindConsumer),
		trace.WithAttributes(
			semconv.MessagingSystemKafka,
			semconv.MessagingDestinationName(topic),
			semconv.MessagingOperationName("process"),
		),
	)
	defer span.End()

	err := c.HandleMessage(ctx, msg.Value)
	if err != nil {
		span.RecordError(err)
		span.SetStatus(codes.Error, err.Error())
	}
	return err
}
