package postgres

import (
	"context"
	"testing"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/propagation"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/trace"

	"github.com/IQVO/customer-payments/internal/application/outbox"
)

func useTraceContextPropagator(t *testing.T) {
	t.Helper()
	previous := otel.GetTextMapPropagator()
	otel.SetTextMapPropagator(propagation.TraceContext{})
	t.Cleanup(func() { otel.SetTextMapPropagator(previous) })
}

func headerValue(hs []outbox.Header, key string) (string, bool) {
	for _, h := range hs {
		if h.Key == key {
			return h.Value, true
		}
	}
	return "", false
}

func TestHeaderCarrierGetSetKeys(t *testing.T) {
	headers := []outbox.Header{{Key: "existing", Value: "value"}}
	c := headerCarrier{headers: &headers}

	if c.Get("existing") != "value" || c.Get("absent") != "" {
		t.Fatalf("Get = %q / %q", c.Get("existing"), c.Get("absent"))
	}
	c.Set("traceparent", "a")
	c.Set("traceparent", "b") // replaces: two traceparents would leave a consumer guessing
	if len(headers) != 2 || c.Get("traceparent") != "b" {
		t.Fatalf("headers after Set = %v", headers)
	}
	if keys := c.Keys(); len(keys) != 2 || keys[0] != "existing" || keys[1] != "traceparent" {
		t.Fatalf("Keys = %v", keys)
	}
}

// Under a span, the stored message carries the W3C traceparent of that span next
// to the encoder's own headers, which stay untouched.
func TestWithTraceHeadersStampsTheActiveSpan(t *testing.T) {
	useTraceContextPropagator(t)
	ctx, span := sdktrace.NewTracerProvider().Tracer("test").Start(context.Background(), "POST /checkout")
	defer span.End()
	msgs := []outbox.Message{{Headers: []outbox.Header{{Key: "content-type", Value: "application/cloudevents+json; charset=UTF-8"}}}}

	got := withTraceHeaders(ctx, msgs)[0].Headers

	if v, _ := headerValue(got, "content-type"); v != "application/cloudevents+json; charset=UTF-8" {
		t.Errorf("content-type = %q, want the encoder's header unchanged", v)
	}
	tp, ok := headerValue(got, "traceparent")
	if !ok {
		t.Fatalf("headers %v carry no traceparent", got)
	}
	extracted := trace.SpanContextFromContext(propagation.TraceContext{}.Extract(context.Background(), headerCarrier{headers: &got}))
	if extracted.TraceID() != span.SpanContext().TraceID() || extracted.SpanID() != span.SpanContext().SpanID() {
		t.Errorf("traceparent %q is not the active span's %s/%s", tp, span.SpanContext().TraceID(), span.SpanContext().SpanID())
	}
}

// Without a span (a background job, a test) the message is stored exactly as the
// encoder built it.
func TestWithTraceHeadersAddsNothingWithoutASpan(t *testing.T) {
	useTraceContextPropagator(t)
	msgs := []outbox.Message{{Headers: []outbox.Header{{Key: "content-type", Value: "x"}}}}

	got := withTraceHeaders(context.Background(), msgs)[0].Headers

	if len(got) != 1 {
		t.Fatalf("headers = %v, want only the encoder's", got)
	}
}
