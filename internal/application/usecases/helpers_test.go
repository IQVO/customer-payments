package usecases_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/IQVO/customer-payments/internal/adapters/outbound/memory"
	"github.com/IQVO/customer-payments/internal/application/parked"
	"github.com/IQVO/customer-payments/internal/application/provider"
	"github.com/IQVO/customer-payments/internal/application/usecases"
	"github.com/IQVO/customer-payments/internal/domain/paymentauthorization"
	"github.com/IQVO/customer-payments/internal/domain/shared"
)

var t0 = time.Date(2026, 10, 24, 9, 0, 0, 0, time.UTC)

type env struct {
	t        *testing.T
	clock    *memory.Clock
	store    *memory.Store
	provider *memory.Provider
	deps     usecases.Deps
	esc      *escalator
}

type escalator struct{ got []string }

func (e *escalator) Escalate(_ context.Context, f parked.Fact) error {
	e.got = append(e.got, f.EventID)
	return nil
}

func newEnv(t *testing.T) *env {
	t.Helper()
	clock := memory.NewClock(t0)
	store := memory.NewStore(clock)
	prov := memory.NewProvider(clock)
	esc := &escalator{}
	return &env{t: t, clock: clock, store: store, provider: prov, esc: esc, deps: usecases.Deps{
		Authorizations: store.Authorizations(), Refunds: store.Refunds(), Parked: store.Parked(), Inbox: store.Inbox(),
		Outbox: store.Outbox(), UoW: store.UnitOfWork(), Clock: clock, IDs: &memory.SequentialIDs{},
		Provider: prov, Escalator: esc,
	}}
}

func authorizeInput() usecases.AuthorizePaymentInput {
	return usecases.AuthorizePaymentInput{
		CheckoutSessionID: "cs-1", PaymentMethodToken: "tok_visa", Currency: "BRL", TotalMinor: 10000,
		Allocations: []paymentauthorization.AllocationInput{{GroupNo: 1, AmountMinor: 7000}, {GroupNo: 2, AmountMinor: 3000}},
	}
}

// authorized authorizes the default input and returns the authorization id.
func (e *env) authorized() string {
	e.t.Helper()
	res, err := usecases.AuthorizePayment{Deps: e.deps}.Execute(context.Background(), authorizeInput())
	if err != nil || !res.Created {
		e.t.Fatalf("authorize = %+v %v", res, err)
	}
	return res.Authorization.AuthorizationID
}

// bound authorizes and binds ord-1 through CheckoutCompleted.
func (e *env) bound() string {
	e.t.Helper()
	id := e.authorized()
	e.completed("evt-cc", id, "ord-1")
	return id
}

func (e *env) completed(eventID, authID, orderID string) {
	e.t.Helper()
	err := usecases.Consumer{Deps: e.deps}.CheckoutCompleted(context.Background(), eventID, "T", usecases.CheckoutCompletedEvent{
		SessionID: "cs-1", OrderID: orderID, AuthorizationID: authID,
	})
	if err != nil {
		e.t.Fatalf("CheckoutCompleted: %v", err)
	}
}

func (e *env) fact(eventID string, f usecases.Fact) error {
	return usecases.Consumer{Deps: e.deps}.ApplyFact(context.Background(), eventID, "T", f)
}

func (e *env) get(id string) (state string, groups []string) {
	e.t.Helper()
	v, err := usecases.GetAuthorization{Deps: e.deps}.Execute(context.Background(), id)
	if err != nil {
		e.t.Fatalf("get: %v", err)
	}
	for _, a := range v.Allocations {
		groups = append(groups, a.State)
	}
	return v.State, groups
}

func eventNames(events []shared.Event) []string {
	var out []string
	for _, e := range events {
		out = append(out, e.EventName())
	}
	return out
}

func wantEvents(t *testing.T, e *env, from int, names ...string) {
	t.Helper()
	got := eventNames(e.store.Events())[from:]
	if len(got) != len(names) {
		t.Fatalf("events = %v, want %v", got, names)
	}
	for i := range names {
		if got[i] != names[i] {
			t.Fatalf("events = %v, want %v", got, names)
		}
	}
}

func wantErr(t *testing.T, err, target error) {
	t.Helper()
	if !errors.Is(err, target) {
		t.Fatalf("error = %v, want %v", err, target)
	}
}

func unavailable() error { return provider.ErrUnavailable }
