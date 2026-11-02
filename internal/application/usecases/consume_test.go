package usecases_test

import (
	"context"
	"testing"
	"time"

	"github.com/IQVO/customer-payments/internal/application/parked"
	"github.com/IQVO/customer-payments/internal/application/ports"
	"github.com/IQVO/customer-payments/internal/application/provider"
	"github.com/IQVO/customer-payments/internal/application/usecases"
	"github.com/IQVO/customer-payments/internal/domain/paymentauthorization"
)

func shipped(group int, tracking ...string) usecases.Fact {
	return usecases.Fact{Kind: parked.Shipped, OrderID: "ord-1", GroupNo: group, TrackingNumbers: tracking, OccurredAt: t0}
}

func TestCheckoutCompletedBindsTheOrder(t *testing.T) {
	e := newEnv(t)
	id := e.bound()
	list, _ := usecases.ListAuthorizations{Deps: e.deps}.Execute(context.Background(), "ord-1", "", nil, 100)
	if len(list.Items) != 1 || list.Items[0].AuthorizationID != id {
		t.Fatalf("bound list = %+v", list)
	}
	e.completed("evt-cc-2", id, "ord-1")
}

func TestCheckoutCompletedRejections(t *testing.T) {
	e := newEnv(t)
	id := e.authorized()
	c := usecases.Consumer{Deps: e.deps}
	err := c.CheckoutCompleted(context.Background(), "e1", "T", usecases.CheckoutCompletedEvent{OrderID: "a b", AuthorizationID: id})
	wantErr(t, err, usecases.ErrInvalidEvent)
	err = c.CheckoutCompleted(context.Background(), "e2", "T", usecases.CheckoutCompletedEvent{SessionID: "other", OrderID: "ord-1", AuthorizationID: id})
	wantErr(t, err, usecases.ErrInvalidEvent)
	err = c.CheckoutCompleted(context.Background(), "e3", "T", usecases.CheckoutCompletedEvent{OrderID: "ord-1", AuthorizationID: "nope"})
	wantErr(t, err, ports.ErrNotFound)
	e.completed("e4", id, "ord-1")
	err = c.CheckoutCompleted(context.Background(), "e5", "T", usecases.CheckoutCompletedEvent{OrderID: "ord-2", AuthorizationID: id})
	wantErr(t, err, paymentauthorization.ErrOrderAlreadyBound)
}

func TestShipmentCapturesTheGroupAndStoresTracking(t *testing.T) {
	e := newEnv(t)
	id := e.bound()
	if err := e.fact("evt-s1", shipped(1, "TRK1", "TRK2")); err != nil {
		t.Fatal(err)
	}
	state, groups := e.get(id)
	if state != "PARTIALLY_CAPTURED" || groups[0] != "CAPTURED" {
		t.Fatalf("state = %s %v", state, groups)
	}
	v, _ := usecases.GetAuthorization{Deps: e.deps}.Execute(context.Background(), id)
	if v.Allocations[0].TrackingNumber != "TRK1" || len(v.Allocations[0].TrackingNumbers) != 2 {
		t.Errorf("tracking = %+v", v.Allocations[0])
	}
	wantEvents(t, e, 1, "PaymentCaptured")
	if c := e.provider.Calls(); c[len(c)-1].Key != "capture:pay-1:1" || c[len(c)-1].AmountMinor != 7000 {
		t.Errorf("capture call = %+v", c[len(c)-1])
	}
}

func TestShipmentRedeliveryIsIdempotent(t *testing.T) {
	e := newEnv(t)
	e.bound()
	for _, evt := range []string{"evt-s1", "evt-s1", "evt-s2"} {
		if err := e.fact(evt, shipped(1, "TRK1")); err != nil {
			t.Fatal(err)
		}
	}
	if n := e.provider.Effects("capture"); n != 1 {
		t.Errorf("capture effects = %d", n)
	}
	wantEvents(t, e, 1, "PaymentCaptured")
}

func TestShipmentProviderUnavailableRollsBack(t *testing.T) {
	e := newEnv(t)
	id := e.bound()
	e.provider.Fail("capture", unavailable())
	wantErr(t, e.fact("evt-s1", shipped(1, "TRK1")), provider.ErrUnavailable)
	e.provider.Fail("capture", nil)
	if state, _ := e.get(id); state != "AUTHORIZED" {
		t.Fatalf("state = %s", state)
	}
	if err := e.fact("evt-s1", shipped(1, "TRK1")); err != nil {
		t.Fatalf("redelivery after recovery: %v", err)
	}
}

func TestShipmentRefusals(t *testing.T) {
	e := newEnv(t)
	e.bound()
	wantErr(t, e.fact("e1", shipped(9, "T")), paymentauthorization.ErrUnknownGroup)
	e.clock.Advance(8 * 24 * time.Hour)
	wantErr(t, e.fact("e2", shipped(1, "T")), paymentauthorization.ErrCaptureAfterExpiry)
	wantErr(t, e.fact("e3", usecases.Fact{Kind: parked.Shipped, OrderID: "a b"}), usecases.ErrInvalidEvent)
}

func TestCancellationReleasesAndReducesTheHold(t *testing.T) {
	e := newEnv(t)
	id := e.bound()
	c := usecases.Fact{Kind: parked.Cancelled, OrderID: "ord-1", GroupNo: 2}
	if err := e.fact("evt-c1", c); err != nil {
		t.Fatal(err)
	}
	if state, groups := e.get(id); state != "AUTHORIZED" || groups[1] != "RELEASED" {
		t.Fatalf("state = %s %v", state, groups)
	}
	wantEvents(t, e, 1, "PaymentReleased")
	if last := e.provider.Calls(); last[len(last)-1].Operation != "reduce" || last[len(last)-1].AmountMinor != 7000 {
		t.Errorf("provider call = %+v", last[len(last)-1])
	}
	c2 := usecases.Fact{Kind: parked.Cancelled, OrderID: "ord-1", GroupNo: 2}
	if err := e.fact("evt-c2", c2); err != nil {
		t.Fatal(err)
	}
	wantEvents(t, e, 1, "PaymentReleased")
}

func TestUnsourceableReleasesEverythingAndVoidsTheHold(t *testing.T) {
	e := newEnv(t)
	id := e.bound()
	for i, g := range []int{1, 2} {
		f := usecases.Fact{Kind: parked.Unsourceable, OrderID: "ord-1", GroupNo: g}
		if err := e.fact("evt-u"+itoa(i), f); err != nil {
			t.Fatal(err)
		}
	}
	if state, _ := e.get(id); state != "RELEASED" {
		t.Fatalf("state = %s", state)
	}
	last := e.provider.Calls()
	if last[len(last)-1].Operation != "void" {
		t.Errorf("last provider call = %+v", last[len(last)-1])
	}
	ev := e.store.Events()[1].(paymentauthorization.PaymentReleased)
	if ev.Reason != "UNSOURCEABLE" {
		t.Errorf("reason = %s", ev.Reason)
	}
}

func itoa(i int) string { return string(rune('0' + i)) }
