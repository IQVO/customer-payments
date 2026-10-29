package postgres

import (
	"context"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/propagation"

	"github.com/IQVO/customer-payments/internal/application/outbox"
)

// headerCarrier adapts the outbox header slice to propagation.TextMapCarrier,
// so the W3C traceparent/tracestate the propagator emits is stored with the
// outbox row and the relay publishes it as ordinary Kafka headers. kafka-go
// has no OTel instrumentation of its own: this is what lets a consumer's
// Extract parent its span on the request that enqueued the event.
//
// It holds a pointer to the slice because Set appends when a key is absent.
type headerCarrier struct {
	headers *[]outbox.Header
}

var _ propagation.TextMapCarrier = headerCarrier{}

// Get returns the value of the first header with the given key, or "".
func (c headerCarrier) Get(key string) string {
	for _, h := range *c.headers {
		if h.Key == key {
			return h.Value
		}
	}
	return ""
}

// Set replaces the first header with the given key, or appends one, so a
// message never carries two conflicting traceparents.
func (c headerCarrier) Set(key, value string) {
	for i := range *c.headers {
		if (*c.headers)[i].Key == key {
			(*c.headers)[i].Value = value
			return
		}
	}
	*c.headers = append(*c.headers, outbox.Header{Key: key, Value: value})
}

// Keys lists every header key present.
func (c headerCarrier) Keys() []string {
	keys := make([]string, 0, len(*c.headers))
	for _, h := range *c.headers {
		keys = append(keys, h.Key)
	}
	return keys
}

// withTraceHeaders stamps the span context active on ctx (the request or
// consumer span that raised the events) into each message's headers, at write
// time and inside the same transaction as the row. Without a valid span it
// adds nothing, so the stored headers are exactly the encoder's. Only extra
// headers are added: the CloudEvents attributes and body are untouched.
func withTraceHeaders(ctx context.Context, msgs []outbox.Message) []outbox.Message {
	propagator := otel.GetTextMapPropagator()
	for i := range msgs {
		headers := append([]outbox.Header(nil), msgs[i].Headers...)
		propagator.Inject(ctx, headerCarrier{headers: &headers})
		msgs[i].Headers = headers
	}
	return msgs
}
