package usecases_test

import (
	"context"
	"testing"
	"time"

	"github.com/IQVO/customer-payments/internal/application/parked"
	"github.com/IQVO/customer-payments/internal/application/ports"
	"github.com/IQVO/customer-payments/internal/application/provider"
	"github.com/IQVO/customer-payments/internal/application/usecases"
	"github.com/IQVO/customer-payments/internal/domain/refund"
	"github.com/IQVO/customer-payments/internal/domain/shared"
)

// shippedBoth authorizes, binds and ships both groups.
func (e *env) shippedBoth() {
	e.t.Helper()
	e.bound()
	for i, g := range []int{1, 2} {
		if err := e.fact("evt-s"+itoa(i), shipped(g, "TRK"+itoa(g))); err != nil {
			e.t.Fatal(err)
		}
	}
}

func manual(amount int64) usecases.RequestRefundInput {
	return usecases.RequestRefundInput{OrderID: "ord-1", AmountMinor: amount, Reason: "MANUAL"}
}

func TestManualRefundSucceedsAndPublishesRefundIssued(t *testing.T) {
	e := newEnv(t)
	e.shippedBoth()
	n := len(e.store.Events())
	v, err := usecases.RequestRefund{Deps: e.deps}.Execute(context.Background(), manual(2500))
	if err != nil || v.State != "SUCCEEDED" || v.AuthorizationID != "pay-1" || v.GroupNo != 0 {
		t.Fatalf("refund = %+v %v", v, err)
	}
	wantEvents(t, e, n, "RefundIssued")
	if k := e.provider.Calls(); k[len(k)-1].Key != "refund:pay-1:0:ref-1:1" {
		t.Errorf("key = %s", k[len(k)-1].Key)
	}
}

func TestRefundOverCapturedTotal(t *testing.T) {
	e := newEnv(t)
	e.shippedBoth()
	r := usecases.RequestRefund{Deps: e.deps}
	if _, err := r.Execute(context.Background(), manual(9000)); err != nil {
		t.Fatal(err)
	}
	_, err := r.Execute(context.Background(), manual(1001))
	wantErr(t, err, refund.ErrOverRefund)
	if _, err = r.Execute(context.Background(), manual(1000)); err != nil {
		t.Errorf("exact remainder: %v", err)
	}
}

func TestRefundValidationAndMissingOrder(t *testing.T) {
	e := newEnv(t)
	r := usecases.RequestRefund{Deps: e.deps}
	_, err := r.Execute(context.Background(), usecases.RequestRefundInput{OrderID: "a b", AmountMinor: 1, Reason: "MANUAL"})
	wantErr(t, err, shared.ErrValidation)
	_, err = r.Execute(context.Background(), manual(100))
	wantErr(t, err, ports.ErrNotFound)
	e.shippedBoth()
	_, err = r.Execute(context.Background(), usecases.RequestRefundInput{OrderID: "ord-1", AmountMinor: 100, Reason: "WHIM"})
	wantErr(t, err, shared.ErrValidation)
}

func TestRefundDeclinedFailsAndRetryReachesTheCap(t *testing.T) {
	e := newEnv(t)
	e.shippedBoth()
	e.provider.Fail("refund", provider.Declined("ACCOUNT_CLOSED"))
	v, err := usecases.RequestRefund{Deps: e.deps}.Execute(context.Background(), manual(500))
	if err != nil || v.State != "FAILED" || v.FailureCode != "ACCOUNT_CLOSED" {
		t.Fatalf("refund = %+v %v", v, err)
	}
	retry := usecases.RetryRefund{Deps: e.deps}
	for i := 2; i <= refund.MaxAttempts; i++ {
		if v, err = retry.Execute(context.Background(), v.RefundID); err != nil || v.State != "FAILED" || v.Attempts != i {
			t.Fatalf("retry %d = %+v %v", i, v, err)
		}
	}
	_, err = retry.Execute(context.Background(), v.RefundID)
	wantErr(t, err, shared.ErrIllegalTransition)
}

func TestFailedRefundsDoNotCountTowardsOverRefund(t *testing.T) {
	e := newEnv(t)
	e.shippedBoth()
	e.provider.Fail("refund", provider.Declined("X"))
	r := usecases.RequestRefund{Deps: e.deps}
	if _, err := r.Execute(context.Background(), manual(10000)); err != nil {
		t.Fatal(err)
	}
	e.provider.Fail("refund", nil)
	if v, err := r.Execute(context.Background(), manual(10000)); err != nil || v.State != "SUCCEEDED" {
		t.Fatalf("second refund = %+v %v", v, err)
	}
}

func TestRetrySucceedsAfterTheProviderRecovers(t *testing.T) {
	e := newEnv(t)
	e.shippedBoth()
	e.provider.Fail("refund", provider.Declined("X"))
	v, _ := usecases.RequestRefund{Deps: e.deps}.Execute(context.Background(), manual(500))
	e.provider.Fail("refund", nil)
	v, err := usecases.RetryRefund{Deps: e.deps}.Execute(context.Background(), v.RefundID)
	if err != nil || v.State != "SUCCEEDED" || v.Attempts != 2 {
		t.Fatalf("retry = %+v %v", v, err)
	}
	if k := e.provider.Calls(); k[len(k)-1].Key != "refund:pay-1:0:ref-1:2" {
		t.Errorf("retry key = %s", k[len(k)-1].Key)
	}
	got, _ := usecases.GetRefund{Deps: e.deps}.Execute(context.Background(), v.RefundID)
	if got.State != "SUCCEEDED" {
		t.Errorf("GetRefund = %+v", got)
	}
	_, err = usecases.GetRefund{Deps: e.deps}.Execute(context.Background(), "nope")
	wantErr(t, err, ports.ErrNotFound)
	_, err = usecases.RetryRefund{Deps: e.deps}.Execute(context.Background(), "nope")
	wantErr(t, err, ports.ErrNotFound)
	_, err = usecases.ListRefunds{Deps: e.deps}.Execute(context.Background(), "")
	wantErr(t, err, shared.ErrValidation)
}

func TestProviderOutageLeavesRefundRequestedUntilThePolicyRuns(t *testing.T) {
	e := newEnv(t)
	e.shippedBoth()
	e.provider.Fail("refund", unavailable())
	v, err := usecases.RequestRefund{Deps: e.deps}.Execute(context.Background(), manual(500))
	if err != nil || v.State != "REQUESTED" {
		t.Fatalf("refund = %+v %v", v, err)
	}
	p := usecases.ProcessRequestedRefunds{Deps: e.deps}
	if n, err := p.Execute(context.Background()); err != nil || n != 0 {
		t.Fatalf("too early = %d %v", n, err)
	}
	e.clock.Advance(time.Minute)
	e.provider.Fail("refund", nil)
	if n, err := p.Execute(context.Background()); err != nil || n != 1 {
		t.Fatalf("policy = %d %v", n, err)
	}
	if got, _ := (usecases.GetRefund{Deps: e.deps}).Execute(context.Background(), v.RefundID); got.State != "SUCCEEDED" {
		t.Errorf("state = %s", got.State)
	}
}

func TestPackageLostRefundsTheGroupOnce(t *testing.T) {
	e := newEnv(t)
	e.shippedBoth()
	lost := usecases.Fact{Kind: parked.Lost, OrderID: "ord-1", TrackingNumbers: []string{"TRK2"}}
	if err := e.fact("evt-l1", lost); err != nil {
		t.Fatal(err)
	}
	returned := usecases.Fact{Kind: parked.Returned, OrderID: "ord-1", TrackingNumbers: []string{"TRK2"}}
	if err := e.fact("evt-r1", returned); err != nil {
		t.Fatal(err)
	}
	list, _ := usecases.ListRefunds{Deps: e.deps}.Execute(context.Background(), "ord-1")
	if len(list) != 1 || list[0].AmountMinor != 3000 || list[0].GroupNo != 2 || list[0].Reason != "LOST" {
		t.Fatalf("refunds = %+v", list)
	}
}

func TestPackageReturnedRefundsWithItsReason(t *testing.T) {
	e := newEnv(t)
	e.shippedBoth()
	returned := usecases.Fact{Kind: parked.Returned, OrderID: "ord-1", TrackingNumbers: []string{"TRK1"}}
	if err := e.fact("evt-r1", returned); err != nil {
		t.Fatal(err)
	}
	list, _ := usecases.ListRefunds{Deps: e.deps}.Execute(context.Background(), "ord-1")
	if len(list) != 1 || list[0].Reason != "RETURNED_TO_SENDER" || list[0].AmountMinor != 7000 {
		t.Fatalf("refunds = %+v", list)
	}
	bad := usecases.Fact{Kind: parked.Lost, OrderID: "ord-1"}
	wantErr(t, e.fact("evt-bad", bad), usecases.ErrInvalidEvent)
}
