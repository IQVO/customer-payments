package usecases_test

import (
	"context"
	"testing"
	"time"

	"github.com/IQVO/customer-payments/internal/application/parked"
	"github.com/IQVO/customer-payments/internal/application/usecases"
)

func TestFactForUnboundOrderIsParkedThenAppliedOnBind(t *testing.T) {
	e := newEnv(t)
	id := e.authorized()
	if err := e.fact("evt-s1", shipped(1, "TRK1")); err != nil {
		t.Fatal(err)
	}
	if err := e.fact("evt-c1", usecases.Fact{Kind: parked.Cancelled, OrderID: "ord-1", GroupNo: 2}); err != nil {
		t.Fatal(err)
	}
	if n, _ := e.store.Parked().Count(context.Background()); n != 2 {
		t.Fatalf("parked = %d", n)
	}
	e.completed("evt-cc", id, "ord-1")
	state, groups := e.get(id)
	if state != "CAPTURED" || groups[0] != "CAPTURED" || groups[1] != "RELEASED" {
		t.Fatalf("after bind: %s %v", state, groups)
	}
	if n, _ := e.store.Parked().Count(context.Background()); n != 0 {
		t.Errorf("parked after bind = %d", n)
	}
}

func TestLostBeforeShipmentWaitsForTheCapture(t *testing.T) {
	e := newEnv(t)
	e.bound()
	lost := usecases.Fact{Kind: parked.Lost, OrderID: "ord-1", TrackingNumbers: []string{"TRK1"}}
	if err := e.fact("evt-l1", lost); err != nil {
		t.Fatal(err)
	}
	if n, _ := e.store.Parked().Count(context.Background()); n != 1 {
		t.Fatalf("parked = %d", n)
	}
	if err := e.fact("evt-s1", shipped(1, "TRK1")); err != nil {
		t.Fatal(err)
	}
	if n, _ := e.store.Parked().Count(context.Background()); n != 0 {
		t.Errorf("parked after shipment = %d", n)
	}
	refunds, _ := usecases.ListRefunds{Deps: e.deps}.Execute(context.Background(), "ord-1")
	if len(refunds) != 1 || refunds[0].AmountMinor != 7000 || refunds[0].Reason != "LOST" || refunds[0].State != "SUCCEEDED" {
		t.Fatalf("refunds = %+v", refunds)
	}
}

func TestMaintainEscalatesAfter24hAndPurgesAfter30d(t *testing.T) {
	e := newEnv(t)
	if err := e.fact("evt-s1", shipped(1, "TRK1")); err != nil {
		t.Fatal(err)
	}
	m := usecases.MaintainParkedFacts{Deps: e.deps}
	got, err := m.Execute(context.Background())
	if err != nil || got.Escalated != 0 || got.Parked != 1 {
		t.Fatalf("early pass = %+v %v", got, err)
	}
	e.clock.Advance(25 * time.Hour)
	got, err = m.Execute(context.Background())
	if err != nil || got.Escalated != 1 || got.Purged != 0 || got.Parked != 1 || len(e.esc.got) != 1 {
		t.Fatalf("escalation pass = %+v %v %v", got, err, e.esc.got)
	}
	got, _ = m.Execute(context.Background())
	if got.Escalated != 0 {
		t.Errorf("escalated twice: %+v", got)
	}
	e.clock.Advance(30 * 24 * time.Hour)
	got, _ = m.Execute(context.Background())
	if got.Purged != 1 || got.Parked != 0 {
		t.Errorf("purge pass = %+v", got)
	}
}
