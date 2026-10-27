package kafka

import (
	"context"
	"errors"
	"testing"

	kafkago "github.com/segmentio/kafka-go"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/propagation"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
	"go.opentelemetry.io/otel/trace"

	ce "github.com/cloudevents/sdk-go/v2/event"

	"github.com/IQVO/customer-payments/internal/adapters/kafka/cloudevents"
)

const publisherTraceparent = "00-4bf92f3577b34da6a3ce929d0e0e4736-00f067aa0ba902b7-01"

var probeType = cloudevents.Type("probe", "Probed")

var errProbeFailed = errors.New("probe failed")

// tracedHandle feeds one message with the given headers through handleTraced
// and returns the span context the handler saw and the consumer's ended spans.
// The handler fails with failWith when it is non-nil.
func tracedHandle(t *testing.T, headers []kafkago.Header, failWith error) (trace.SpanContext, []sdktrace.ReadOnlySpan) {
	t.Helper()
	tp, prop := otel.GetTracerProvider(), otel.GetTextMapPropagator()
	t.Cleanup(func() { otel.SetTracerProvider(tp); otel.SetTextMapPropagator(prop) })
	recorder := tracetest.NewSpanRecorder()
	otel.SetTracerProvider(sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(recorder)))
	otel.SetTextMapPropagator(propagation.TraceContext{})

	var seen trace.SpanContext
	consumer := &Consumer{
		Name: "probe", Topic: "t", Logger: quietLogger(),
		Handlers: map[string]HandlerFunc{probeType: func(ctx context.Context, _ ce.Event) error {
			seen = trace.SpanContextFromContext(ctx)
			return failWith
		}},
	}
	value, err := cloudevents.New(cloudevents.Spec{
		ID: "e1", Entity: "probe", EventName: "Probed", Subject: "s-1", Stream: cloudevents.StreamEvents, Version: 1, Data: map[string]any{},
	})
	if err != nil {
		t.Fatal(err)
	}
	_ = consumer.handleTraced(context.Background(), kafkago.Message{Topic: "t", Headers: headers, Value: value})
	return seen, recorder.Ended()
}

// The consumer span is a consumer-kind span named "<topic> process" whose parent
// is the publisher's span carried in traceparent, and the handler runs inside it.
func TestConsumerSpanIsParentedToThePublisherTrace(t *testing.T) {
	seen, spans := tracedHandle(t, []kafkago.Header{
		{Key: "content-type", Value: []byte(cloudevents.MediaType)},
		{Key: "traceparent", Value: []byte(publisherTraceparent)},
	}, nil)

	if len(spans) != 1 {
		t.Fatalf("recorded %d spans, want 1", len(spans))
	}
	span := spans[0]
	if span.SpanKind() != trace.SpanKindConsumer || span.Name() != "t process" {
		t.Errorf("span = %v %q, want consumer \"t process\"", span.SpanKind(), span.Name())
	}
	if got := span.SpanContext().TraceID().String(); got != "4bf92f3577b34da6a3ce929d0e0e4736" {
		t.Errorf("trace id = %s, want the publisher's", got)
	}
	if got := span.Parent().SpanID().String(); got != "00f067aa0ba902b7" {
		t.Errorf("parent span id = %s, want the publish span's", got)
	}
	if seen.SpanID() != span.SpanContext().SpanID() {
		t.Errorf("handler ran under span %s, want the consumer span %s", seen.SpanID(), span.SpanContext().SpanID())
	}
}

func TestConsumerWithoutTraceHeadersStartsANewTrace(t *testing.T) {
	_, spans := tracedHandle(t, nil, nil)
	if len(spans) != 1 {
		t.Fatalf("recorded %d spans, want 1", len(spans))
	}
	if spans[0].Parent().IsValid() {
		t.Errorf("parent = %v, want none for a message without traceparent", spans[0].Parent())
	}
}

func TestConsumerSpanRecordsAHandlerFailure(t *testing.T) {
	_, spans := tracedHandle(t, []kafkago.Header{{Key: "traceparent", Value: []byte(publisherTraceparent)}}, errProbeFailed)
	if len(spans) != 1 || spans[0].Status().Code != codes.Error {
		t.Fatalf("spans = %v, want one errored consumer span", spans)
	}
}
