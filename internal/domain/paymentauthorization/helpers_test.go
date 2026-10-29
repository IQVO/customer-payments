package paymentauthorization_test

import (
	"errors"
	"testing"
	"time"

	pa "github.com/IQVO/customer-payments/internal/domain/paymentauthorization"
	"github.com/IQVO/customer-payments/internal/domain/shared"
)

var t0 = time.Date(2026, 10, 24, 9, 0, 0, 0, time.UTC)

func draft() pa.Draft {
	return pa.Draft{
		ID: "pay-1", CheckoutSessionID: "cs-1", PaymentMethodToken: "tok_visa", Currency: "BRL", TotalMinor: 10000,
		Allocations: []pa.AllocationInput{{GroupNo: 2, AmountMinor: 3000}, {GroupNo: 1, AmountMinor: 7000}},
	}
}

func approved() pa.Outcome {
	return pa.Outcome{ProviderAuthorizationID: "prov-1", ExpiresAt: t0.Add(7 * 24 * time.Hour)}
}

func newAuth(t *testing.T) *pa.PaymentAuthorization {
	t.Helper()
	p, _, err := pa.New(draft(), approved(), t0)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return p
}

func mustEvents(t *testing.T, events []shared.Event, err error, names ...string) {
	t.Helper()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(events) != len(names) {
		t.Fatalf("got %d events %v, want %v", len(events), eventNames(events), names)
	}
	for i, e := range events {
		if e.EventName() != names[i] {
			t.Fatalf("event %d = %s, want %s", i, e.EventName(), names[i])
		}
		if e.Entity() != "paymentauthorization" || e.SubjectID() != "pay-1" {
			t.Errorf("event %s entity/subject = %s/%s", e.EventName(), e.Entity(), e.SubjectID())
		}
	}
}

func eventNames(events []shared.Event) []string {
	var out []string
	for _, e := range events {
		out = append(out, e.EventName())
	}
	return out
}

func wantErr(t *testing.T, err, target error) {
	t.Helper()
	if !errors.Is(err, target) {
		t.Fatalf("error = %v, want %v", err, target)
	}
}

func capture(t *testing.T, p *pa.PaymentAuthorization, group int, amount int64) {
	t.Helper()
	events, err := p.Capture(group, amount, []string{"TRK-" + string(rune('0'+group))}, "cap", t0.Add(time.Hour))
	mustEvents(t, events, err, "PaymentCaptured")
}
