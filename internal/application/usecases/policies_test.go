package usecases_test

import (
	"context"
	"testing"
	"time"

	"github.com/IQVO/customer-payments/internal/application/provider"
	"github.com/IQVO/customer-payments/internal/application/usecases"
)

func TestVoidUnboundAfter24h(t *testing.T) {
	e := newEnv(t)
	id := e.authorized()
	p := usecases.VoidUnboundAuthorizations{Deps: e.deps}
	if n, err := p.Execute(context.Background()); err != nil || n != 0 {
		t.Fatalf("too early = %d %v", n, err)
	}
	e.clock.Advance(24 * time.Hour)
	if n, err := p.Execute(context.Background()); err != nil || n != 1 {
		t.Fatalf("policy = %d %v", n, err)
	}
	if state, _ := e.get(id); state != "VOIDED" {
		t.Errorf("state = %s", state)
	}
	wantEvents(t, e, 1, "PaymentReleased", "PaymentReleased")
}

func TestVoidUnboundSkipsBoundAndSurvivesProviderOutage(t *testing.T) {
	e := newEnv(t)
	e.bound()
	e.clock.Advance(25 * time.Hour)
	p := usecases.VoidUnboundAuthorizations{Deps: e.deps}
	if n, err := p.Execute(context.Background()); err != nil || n != 0 {
		t.Fatalf("bound = %d %v", n, err)
	}
	e2 := newEnv(t)
	id := e2.authorized()
	e2.clock.Advance(25 * time.Hour)
	e2.provider.Fail("void", unavailable())
	if n, err := (usecases.VoidUnboundAuthorizations{Deps: e2.deps}).Execute(context.Background()); err != nil || n != 0 {
		t.Fatalf("outage = %d %v", n, err)
	}
	if state, _ := e2.get(id); state != "AUTHORIZED" {
		t.Errorf("state = %s", state)
	}
}

func TestReauthorizeBeforeExpiry(t *testing.T) {
	e := newEnv(t)
	id := e.bound()
	p := usecases.ReauthorizeBeforeExpiry{Deps: e.deps}
	if n, _ := p.Execute(context.Background()); n != 0 {
		t.Fatalf("not due yet: %d", n)
	}
	e.clock.Advance(6*24*time.Hour + time.Hour)
	if n, err := p.Execute(context.Background()); err != nil || n != 1 {
		t.Fatalf("policy = %d %v", n, err)
	}
	v, _ := usecases.GetAuthorization{Deps: e.deps}.Execute(context.Background(), id)
	if v.State != "AUTHORIZED" || v.ProviderAuthorizationID == "hold-1" || !v.ExpiresAt.After(t0.Add(7*24*time.Hour)) {
		t.Errorf("after reauth = %+v", v)
	}
	wantEvents(t, e, 1, "PaymentReauthorized")
	last := e.provider.Calls()
	if k := last[len(last)-1]; k.Operation != "reauthorize" || k.Key != "reauthorize:pay-1:0:1" || k.AmountMinor != 10000 {
		t.Errorf("provider call = %+v", k)
	}
	if n, _ := p.Execute(context.Background()); n != 0 {
		t.Errorf("renewed hold is due again: %d", n)
	}
}

func TestReauthorizeDeclinedExpiresAndRaisesPaymentDeclined(t *testing.T) {
	e := newEnv(t)
	id := e.bound()
	e.clock.Advance(6*24*time.Hour + time.Hour)
	e.provider.Fail("reauthorize", provider.Declined("CARD_EXPIRED"))
	if n, err := (usecases.ReauthorizeBeforeExpiry{Deps: e.deps}).Execute(context.Background()); err != nil || n != 1 {
		t.Fatalf("policy = %d %v", n, err)
	}
	if state, _ := e.get(id); state != "EXPIRED" {
		t.Errorf("state = %s", state)
	}
	wantEvents(t, e, 1, "PaymentReleased", "PaymentReleased", "PaymentAuthorizationExpired", "PaymentDeclined")
}

func TestReauthorizeOutageRetriesAndLapsedHoldExpires(t *testing.T) {
	e := newEnv(t)
	id := e.bound()
	e.clock.Advance(6*24*time.Hour + time.Hour)
	e.provider.Fail("reauthorize", unavailable())
	p := usecases.ReauthorizeBeforeExpiry{Deps: e.deps}
	if n, err := p.Execute(context.Background()); err != nil || n != 0 {
		t.Fatalf("outage = %d %v", n, err)
	}
	e.clock.Advance(24 * time.Hour)
	if n, err := p.Execute(context.Background()); err != nil || n != 1 {
		t.Fatalf("lapsed = %d %v", n, err)
	}
	if state, _ := e.get(id); state != "EXPIRED" {
		t.Errorf("state = %s", state)
	}
	wantEvents(t, e, 1, "PaymentReleased", "PaymentReleased", "PaymentAuthorizationExpired")
}
