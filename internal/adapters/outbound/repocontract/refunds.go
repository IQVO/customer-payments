package repocontract

import (
	"encoding/json"
	"testing"

	"github.com/IQVO/customer-payments/internal/application/parked"
	"github.com/IQVO/customer-payments/internal/application/ports"
	"github.com/IQVO/customer-payments/internal/domain/paymentauthorization"
)

func idOf(p *paymentauthorization.PaymentAuthorization, err error) (string, error) {
	if err != nil {
		return "", err
	}
	return string(p.ID()), nil
}

func testRefunds(t *testing.T, e Env) {
	savedAuth(t, e, "pay-1", "cs-1", 0)
	_, err := e.Refunds.Get(ctx(), "nope")
	wantErr(t, err, ports.ErrNotFound)
	f := newRefund(t, "ref-1", "pay-1", "ord-1", 0)
	fatalIf(t, e.Refunds.Save(ctx(), f), "save refund")
	got, err := e.Refunds.Get(ctx(), "ref-1")
	fatalIf(t, err, "get refund")
	g, _ := json.Marshal(got.Snapshot())
	w, _ := json.Marshal(f.Snapshot())
	if string(g) != string(w) {
		t.Fatalf("round trip changed the refund:\n got %s\nwant %s", g, w)
	}
	wantErr(t, e.Refunds.Save(ctx(), newRefund(t, "ref-1", "pay-1", "ord-1", 0)), ports.ErrConcurrentUpdate)

	_, err = got.RecordFailure("PROVIDER_REFUSED", at(1))
	fatalIf(t, err, "fail")
	stale, _ := e.Refunds.Get(ctx(), "ref-1")
	fatalIf(t, e.Refunds.Save(ctx(), got), "save failed refund")
	reloaded, _ := e.Refunds.Get(ctx(), "ref-1")
	if reloaded.State() != "FAILED" || reloaded.FailureCode() != "PROVIDER_REFUSED" || reloaded.Attempts() != 1 {
		t.Fatalf("reloaded = %s %s %d", reloaded.State(), reloaded.FailureCode(), reloaded.Attempts())
	}
	_, err = stale.RecordSuccess("prf", at(2))
	fatalIf(t, err, "stale success")
	wantErr(t, e.Refunds.Save(ctx(), stale), ports.ErrConcurrentUpdate)
	refundLists(t, e)
}

func refundLists(t *testing.T, e Env) {
	fatalIf(t, e.Refunds.Save(ctx(), newRefund(t, "ref-2", "pay-1", "ord-1", 5)), "second refund")
	byOrder, err := e.Refunds.ListByOrder(ctx(), "ord-1")
	fatalIf(t, err, "list by order")
	if len(byOrder) != 2 || byOrder[0].ID() != "ref-1" {
		t.Fatalf("by order = %d", len(byOrder))
	}
	byAuth, err := e.Refunds.ListByAuthorization(ctx(), "pay-1")
	fatalIf(t, err, "list by authorization")
	if len(byAuth) != 2 {
		t.Fatalf("by authorization = %d", len(byAuth))
	}
	empty, err := e.Refunds.ListByOrder(ctx(), "ord-none")
	fatalIf(t, err, "list empty")
	if len(empty) != 0 {
		t.Fatalf("empty list = %d", len(empty))
	}
	due, err := e.Refunds.DueRequested(ctx(), at(10), 10)
	fatalIf(t, err, "due requested")
	if len(due) != 1 || due[0] != "ref-2" {
		t.Fatalf("due requested = %v", due)
	}
	early, _ := e.Refunds.DueRequested(ctx(), at(1), 10)
	if len(early) != 0 {
		t.Fatalf("early due = %v", early)
	}
}

func parkedFact(eventID, order string, m int) parked.Fact {
	return parked.Fact{
		EventID: eventID, Kind: parked.Shipped, OrderID: order, GroupNo: 1, TrackingNumbers: []string{"TRK1"},
		OccurredAt: at(m), ParkedAt: at(m),
	}
}

func testParked(t *testing.T, e Env) {
	fatalIf(t, e.Parked.Park(ctx(), parkedFact("ev-1", "ord-1", 0)), "park")
	fatalIf(t, e.Parked.Park(ctx(), parkedFact("ev-1", "ord-1", 9)), "park again")
	late := parkedFact("ev-2", "ord-1", 5)
	late.Kind, late.TrackingNumbers = parked.Lost, nil
	fatalIf(t, e.Parked.Park(ctx(), late), "park lost")
	fatalIf(t, e.Parked.Park(ctx(), parkedFact("ev-3", "ord-2", 1)), "park other order")
	if n, _ := e.Parked.Count(ctx()); n != 3 {
		t.Fatalf("count = %d, want 3", n)
	}
	old, err := e.Parked.Unescalated(ctx(), at(2), 10)
	fatalIf(t, err, "unescalated")
	if len(old) != 2 || old[0].EventID != "ev-1" || old[0].ParkedAt != at(0) {
		t.Fatalf("unescalated = %+v", old)
	}
	fatalIf(t, e.Parked.MarkEscalated(ctx(), "ev-1", at(3)), "escalate")
	wantErr(t, e.Parked.MarkEscalated(ctx(), "ev-none", at(3)), ports.ErrNotFound)
	old, _ = e.Parked.Unescalated(ctx(), at(2), 10)
	if len(old) != 1 || old[0].EventID != "ev-3" {
		t.Fatalf("after escalation = %+v", old)
	}
	taken, err := e.Parked.TakeForOrder(ctx(), "ord-1")
	fatalIf(t, err, "take")
	if len(taken) != 2 || taken[0].EventID != "ev-1" || taken[1].Kind != parked.Lost || taken[0].EscalatedAt.IsZero() {
		t.Fatalf("taken = %+v", taken)
	}
	n, err := e.Parked.Purge(ctx(), at(1))
	fatalIf(t, err, "purge")
	if n != 1 {
		t.Fatalf("purged = %d", n)
	}
	if c, _ := e.Parked.Count(ctx()); c != 0 {
		t.Fatalf("count after purge = %d", c)
	}
}
