package kafka_test

import (
	"errors"
	"testing"

	kafkaconsumer "github.com/IQVO/customer-payments/internal/adapters/inbound/kafka"
)

func TestCheckoutCompletedBindsAndShipmentCaptures(t *testing.T) {
	r := newRig(t)
	id := r.authorize()
	for _, m := range [][]byte{completed(t, "e1", id), completed(t, "e1", id), shipped(t, "e2", 1, "TRK1")} {
		if err := r.handle(r.checkout, m); err != nil {
			t.Fatal(err)
		}
	}
	if err := r.handle(r.sourcing, shipped(t, "e2", 1, "TRK1")); err != nil {
		t.Fatal(err)
	}
	if got := r.state(id); got != "PARTIALLY_CAPTURED" {
		t.Fatalf("state = %s", got)
	}
	if c := r.store.Counts(); c.Inbox != 2 {
		t.Fatalf("inbox = %d, want 2 (duplicates are claimed once)", c.Inbox)
	}
}

func TestCancellationAndUnsourceableRelease(t *testing.T) {
	r := newRig(t)
	id := r.authorize()
	if err := r.handle(r.checkout, completed(t, "e1", id)); err != nil {
		t.Fatal(err)
	}
	cancelled := message(t, "e2", kafkaconsumer.TypeFulfillmentCancelled, map[string]any{"orderId": "ord-1", "groupNo": 1, "reason": "CUSTOMER_REQUEST"})
	unsourceable := message(t, "e3", kafkaconsumer.TypeFulfillmentUnsourcing, map[string]any{"orderId": "ord-1", "groupNo": 2, "reason": "NO_NODE"})
	for _, m := range [][]byte{cancelled, unsourceable} {
		if err := r.handle(r.sourcing, m); err != nil {
			t.Fatal(err)
		}
	}
	if got := r.state(id); got != "RELEASED" {
		t.Fatalf("state = %s", got)
	}
}

func TestPackageLostAndReturnedRefund(t *testing.T) {
	r := newRig(t)
	id := r.authorize()
	for _, m := range [][]byte{completed(t, "e1", id)} {
		if err := r.handle(r.checkout, m); err != nil {
			t.Fatal(err)
		}
	}
	if err := r.handle(r.sourcing, shipped(t, "e2", 1, "TRK1")); err != nil {
		t.Fatal(err)
	}
	lost := message(t, "e3", kafkaconsumer.TypePackageLost, map[string]any{"trackingNumber": "TRK1", "orderId": "ord-1", "declaredAt": t0})
	if err := r.handle(r.shipping, lost); err != nil {
		t.Fatal(err)
	}
	if c := r.store.Counts(); c.Refunds != 1 {
		t.Fatalf("refunds = %d", c.Refunds)
	}
	ret := message(t, "e4", kafkaconsumer.TypePackageReturned, map[string]any{"trackingNumber": "TRK1", "orderId": "ord-1"})
	if err := r.handle(r.shipping, ret); err != nil {
		t.Fatal(err)
	}
	if c := r.store.Counts(); c.Refunds != 1 {
		t.Fatalf("a returned package already refunded as lost refunds again: %d", c.Refunds)
	}
}

func TestFullTypeDispatchIgnoresSuffixAndUnknownTypes(t *testing.T) {
	r := newRig(t)
	for _, typ := range []string{
		"FulfillmentShipped", "com.retailnetwork.commerce.order-sourcing.assignment.FulfillmentDelivered",
		"x." + kafkaconsumer.TypeFulfillmentShipped,
	} {
		if err := r.handle(r.sourcing, message(t, "e-"+typ, typ, map[string]any{"orderId": "ord-1", "groupNo": 1})); err != nil {
			t.Fatal(err)
		}
	}
	if c := r.store.Counts(); c.Inbox != 0 || c.Parked != 0 {
		t.Fatalf("counts = %+v", c)
	}
}

func TestLegacyFlatMessageIsSkipped(t *testing.T) {
	r := newRig(t)
	flat := []byte(`{"event_id":"1","event_type":"FulfillmentShipped","orderId":"ord-1"}`)
	if err := r.handle(r.sourcing, flat); err != nil {
		t.Fatal(err)
	}
	if c := r.store.Counts(); c.Inbox != 0 {
		t.Fatalf("counts = %+v", c)
	}
}

func TestPoisonPayloadsAreDeadLettered(t *testing.T) {
	r := newRig(t)
	id := r.authorize()
	if err := r.handle(r.checkout, completed(t, "e1", id)); err != nil {
		t.Fatal(err)
	}
	cases := map[string][]byte{
		"invalid order id":  message(t, "p1", kafkaconsumer.TypeFulfillmentShipped, map[string]any{"orderId": "bad id!", "groupNo": 1}),
		"wrong shape":       message(t, "p2", kafkaconsumer.TypeFulfillmentShipped, []int{1}),
		"unknown group":     shipped(t, "p3", 9, "T"),
		"lost w/o tracking": message(t, "p4", kafkaconsumer.TypePackageLost, map[string]any{"orderId": "ord-1", "trackingNumber": ""}),
	}
	for name, m := range cases {
		c := r.sourcing
		if name == "lost w/o tracking" {
			c = r.shipping
		}
		if err := r.handle(c, m); !errors.Is(err, kafkaconsumer.ErrPoison) {
			t.Errorf("%s: err = %v, want poison", name, err)
		}
	}
}

func TestPoisonIsSkippedWithoutADeadLetterWriter(t *testing.T) {
	r := newRig(t)
	r.sourcing.DLQ = nil
	if err := r.handle(r.sourcing, message(t, "p", kafkaconsumer.TypeFulfillmentShipped, map[string]any{"orderId": "", "groupNo": 1})); err != nil {
		t.Fatal(err)
	}
}

func TestProviderOutageIsTransient(t *testing.T) {
	r := newRig(t)
	id := r.authorize()
	if err := r.handle(r.checkout, completed(t, "e1", id)); err != nil {
		t.Fatal(err)
	}
	r.provider.Fail("capture", errors.New("boom"))
	err := r.handle(r.sourcing, shipped(t, "e2", 1, "T1"))
	if err == nil || errors.Is(err, kafkaconsumer.ErrPoison) {
		t.Fatalf("err = %v, want a transient error", err)
	}
}
