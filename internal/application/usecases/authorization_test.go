package usecases_test

import (
	"context"
	"testing"

	"github.com/IQVO/customer-payments/internal/application/ports"
	"github.com/IQVO/customer-payments/internal/application/provider"
	"github.com/IQVO/customer-payments/internal/application/usecases"
	"github.com/IQVO/customer-payments/internal/domain/paymentauthorization"
	"github.com/IQVO/customer-payments/internal/domain/shared"
)

func TestAuthorizeCreatesAndPublishes(t *testing.T) {
	e := newEnv(t)
	id := e.authorized()
	if state, _ := e.get(id); state != "AUTHORIZED" {
		t.Errorf("state = %s", state)
	}
	wantEvents(t, e, 0, "PaymentAuthorized")
	calls := e.provider.Calls()
	if len(calls) != 1 || calls[0].Key != "authorize:cs-1:0:1" || calls[0].AmountMinor != 10000 {
		t.Errorf("calls = %+v", calls)
	}
}

func TestAuthorizeReplayReturnsExistingWithoutProvider(t *testing.T) {
	e := newEnv(t)
	id := e.authorized()
	res, err := usecases.AuthorizePayment{Deps: e.deps}.Execute(context.Background(), authorizeInput())
	if err != nil || res.Created || res.Authorization.AuthorizationID != id {
		t.Fatalf("replay = %+v %v", res, err)
	}
	if len(e.provider.Calls()) != 1 || len(e.store.Events()) != 1 {
		t.Errorf("replay touched the provider or the outbox: %d calls %d events", len(e.provider.Calls()), len(e.store.Events()))
	}
}

func TestAuthorizeDeclinedIsRecordedAndAnotherTokenMayRetry(t *testing.T) {
	e := newEnv(t)
	e.provider.Fail("authorize", provider.Declined("CARD_DECLINED"))
	res, err := usecases.AuthorizePayment{Deps: e.deps}.Execute(context.Background(), authorizeInput())
	if err != nil || !res.Created || res.Authorization.State != "DECLINED" || res.Authorization.DeclineReason != "CARD_DECLINED" {
		t.Fatalf("declined = %+v %v", res, err)
	}
	wantEvents(t, e, 0, "PaymentDeclined")
	e.provider.Fail("authorize", nil)
	res2, err := usecases.AuthorizePayment{Deps: e.deps}.Execute(context.Background(), authorizeInput())
	if err != nil || !res2.Created || res2.Authorization.State != "AUTHORIZED" || res2.Authorization.AuthorizationID == res.Authorization.AuthorizationID {
		t.Fatalf("second attempt = %+v %v", res2, err)
	}
	if k := e.provider.Calls()[1].Key; k != "authorize:cs-1:0:2" {
		t.Errorf("second attempt key = %s", k)
	}
}

func TestAuthorizeProviderUnavailableStoresNothing(t *testing.T) {
	e := newEnv(t)
	e.provider.Fail("authorize", unavailable())
	_, err := usecases.AuthorizePayment{Deps: e.deps}.Execute(context.Background(), authorizeInput())
	wantErr(t, err, provider.ErrUnavailable)
	if c := e.store.Counts(); c.Authorizations != 0 || c.OutboxEvents != 0 {
		t.Errorf("counts = %+v", c)
	}
}

func TestAuthorizeValidationNeverReachesProvider(t *testing.T) {
	e := newEnv(t)
	in := authorizeInput()
	in.TotalMinor = 9999
	_, err := usecases.AuthorizePayment{Deps: e.deps}.Execute(context.Background(), in)
	wantErr(t, err, paymentauthorization.ErrAllocationMismatch)
	in = authorizeInput()
	in.CheckoutSessionID = ""
	_, err = usecases.AuthorizePayment{Deps: e.deps}.Execute(context.Background(), in)
	wantErr(t, err, shared.ErrValidation)
	if len(e.provider.Calls()) != 0 {
		t.Errorf("provider was called: %+v", e.provider.Calls())
	}
}

func TestVoidReleasesAndCallsProvider(t *testing.T) {
	e := newEnv(t)
	id := e.authorized()
	v, err := usecases.VoidAuthorization{Deps: e.deps}.Execute(context.Background(), id)
	if err != nil || v.State != "VOIDED" {
		t.Fatalf("void = %+v %v", v, err)
	}
	wantEvents(t, e, 1, "PaymentReleased", "PaymentReleased")
	if c := e.provider.Calls(); c[1].Operation != "void" || c[1].Key != "void:pay-1:0" {
		t.Errorf("provider call = %+v", c[1])
	}
	_, err = usecases.VoidAuthorization{Deps: e.deps}.Execute(context.Background(), id)
	wantErr(t, err, shared.ErrIllegalTransition)
}

func TestVoidProviderUnavailableChangesNothing(t *testing.T) {
	e := newEnv(t)
	id := e.authorized()
	e.provider.Fail("void", unavailable())
	_, err := usecases.VoidAuthorization{Deps: e.deps}.Execute(context.Background(), id)
	wantErr(t, err, provider.ErrUnavailable)
	if state, _ := e.get(id); state != "AUTHORIZED" {
		t.Errorf("state = %s", state)
	}
	_, err = usecases.VoidAuthorization{Deps: e.deps}.Execute(context.Background(), "nope")
	wantErr(t, err, ports.ErrNotFound)
}

func TestListAuthorizations(t *testing.T) {
	e := newEnv(t)
	id := e.bound()
	l := usecases.ListAuthorizations{Deps: e.deps}
	if got, err := l.Execute(context.Background(), "ord-1", "", nil, 100); err != nil || len(got.Items) != 1 || got.Items[0].AuthorizationID != id || got.Next != nil {
		t.Errorf("by order = %+v %v", got, err)
	}
	if got, err := l.Execute(context.Background(), "", "cs-1", nil, 100); err != nil || len(got.Items) != 1 {
		t.Errorf("by session = %+v %v", got, err)
	}
	if got, err := l.Execute(context.Background(), "ord-9", "", nil, 100); err != nil || len(got.Items) != 0 {
		t.Errorf("unknown order = %+v %v", got, err)
	}
	for _, args := range [][2]string{{"", ""}, {"a", "b"}} {
		_, err := l.Execute(context.Background(), args[0], args[1], nil, 100)
		wantErr(t, err, shared.ErrValidation)
	}
}
