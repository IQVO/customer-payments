//go:build integration

package postgres_test

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/propagation"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"

	"github.com/IQVO/customer-payments/internal/adapters/outbound/postgres"
	"github.com/IQVO/customer-payments/internal/application/outbox"
	"github.com/IQVO/customer-payments/internal/domain/paymentauthorization"
	"github.com/IQVO/customer-payments/internal/domain/shared"
	"github.com/IQVO/customer-payments/internal/testing/pgtest"
)

// traceStubEncoder yields one fixed message whatever the events, so the test pins
// only what the repository adds to it.
type traceStubEncoder struct{ n *int }

func (e traceStubEncoder) Encode(...shared.Event) ([]outbox.Message, error) {
	*e.n++
	return []outbox.Message{{
		EventID: fmt.Sprintf("evt-trace-%d", *e.n), Topic: "t", EventType: "x.Probed", Subject: "o-1", Key: []byte("o-1"), Value: []byte(`{}`),
		Headers: []outbox.Header{{Key: "content-type", Value: "application/cloudevents+json; charset=UTF-8"}},
	}}, nil
}

// An Append made under a span stores that span's traceparent in the row's
// headers, next to the encoder's own header, and a span-less Append stores none.
func TestOutboxAppendStoresTheActiveTraceparent(t *testing.T) {
	previous := otel.GetTextMapPropagator()
	otel.SetTextMapPropagator(propagation.TraceContext{})
	t.Cleanup(func() { otel.SetTextMapPropagator(previous) })

	pool := pgtest.NewPool(t)
	repo := postgres.NewOutboxRepo(pool, traceStubEncoder{n: new(int)})
	event := paymentauthorization.PaymentCaptured{}

	ctx, span := sdktrace.NewTracerProvider().Tracer("test").Start(context.Background(), "POST /payment-authorizations/{authorizationId}:void")
	defer span.End()
	if err := repo.Append(ctx, event); err != nil {
		t.Fatal(err)
	}
	if err := repo.Append(context.Background(), event); err != nil {
		t.Fatal(err)
	}

	rows, err := pool.Query(context.Background(), `SELECT headers::text FROM outbox_events ORDER BY id`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var stored [][]struct{ Key, Value string }
	for rows.Next() {
		var raw string
		if err := rows.Scan(&raw); err != nil {
			t.Fatal(err)
		}
		var hs []struct{ Key, Value string }
		if err := json.Unmarshal([]byte(raw), &hs); err != nil {
			t.Fatal(err)
		}
		stored = append(stored, hs)
	}
	if len(stored) != 2 {
		t.Fatalf("stored %d rows, want 2", len(stored))
	}
	want := "00-" + span.SpanContext().TraceID().String() + "-" + span.SpanContext().SpanID().String() + "-01"
	if len(stored[0]) != 2 || stored[0][0].Key != "content-type" || stored[0][1].Key != "traceparent" || stored[0][1].Value != want {
		t.Errorf("row under a span has headers %v, want content-type and traceparent %s", stored[0], want)
	}
	if len(stored[1]) != 1 || stored[1][0].Key != "content-type" {
		t.Errorf("row without a span has headers %v, want only content-type", stored[1])
	}
}
