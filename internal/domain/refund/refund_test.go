package refund_test

import (
	"errors"
	"testing"
	"time"

	"github.com/IQVO/customer-payments/internal/domain/refund"
	"github.com/IQVO/customer-payments/internal/domain/shared"
)

var t0 = time.Date(2026, 10, 24, 9, 0, 0, 0, time.UTC)

func req() refund.Request {
	return refund.Request{
		ID: "ref-1", AuthorizationID: "pay-1", OrderID: "ord-1", GroupNo: 1, Currency: "BRL",
		AmountMinor: 7000, Reason: refund.ReasonLost,
	}
}

func lim() refund.Limits { return refund.Limits{CapturedMinor: 7000} }

func newRefund(t *testing.T) *refund.Refund {
	t.Helper()
	r, err := refund.New(req(), lim(), t0)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return r
}

func failN(t *testing.T, r *refund.Refund, n int) {
	t.Helper()
	for i := 0; i < n; i++ {
		if _, err := r.RecordFailure("PROVIDER_REFUSED", t0); err != nil {
			t.Fatalf("failure %d: %v", i, err)
		}
		if i < n-1 {
			if err := r.Retry(); err != nil {
				t.Fatalf("retry %d: %v", i, err)
			}
		}
	}
}

func TestNewIsRequested(t *testing.T) {
	r := newRefund(t)
	if r.State() != refund.StateRequested || r.Version() != 1 || r.LoadedVersion() != 0 || r.Attempts() != 0 {
		t.Fatalf("state/version/loaded/attempts = %s/%d/%d/%d", r.State(), r.Version(), r.LoadedVersion(), r.Attempts())
	}
	if r.ID() != "ref-1" || r.AuthorizationID() != "pay-1" || r.OrderID() != "ord-1" || r.GroupNo() != 1 ||
		r.Currency() != "BRL" || r.AmountMinor() != 7000 || r.Reason() != refund.ReasonLost || !r.CreatedAt().Equal(t0) {
		t.Error("accessor mismatch")
	}
	r.MarkPersisted()
	if r.LoadedVersion() != 1 {
		t.Errorf("LoadedVersion = %d", r.LoadedVersion())
	}
}

func TestNewOverRefundCountsRequestedAndIssuedNotFailed(t *testing.T) {
	_, err := refund.New(req(), refund.Limits{CapturedMinor: 7000, RefundedMinor: 1}, t0)
	if !errors.Is(err, refund.ErrOverRefund) {
		t.Fatalf("err = %v, want ErrOverRefund", err)
	}
	if _, err = refund.New(req(), refund.Limits{CapturedMinor: 6999}, t0); !errors.Is(err, refund.ErrOverRefund) {
		t.Fatalf("err = %v, want ErrOverRefund", err)
	}
	if _, err = refund.New(req(), refund.Limits{CapturedMinor: 7000}, t0); err != nil {
		t.Errorf("exact captured total: %v", err)
	}
}

func TestNewValidation(t *testing.T) {
	cases := map[string]func(*refund.Request){
		"refund id": func(r *refund.Request) { r.ID = "" },
		"auth id":   func(r *refund.Request) { r.AuthorizationID = "a b" },
		"order id":  func(r *refund.Request) { r.OrderID = "" },
		"group":     func(r *refund.Request) { r.GroupNo = -1 },
		"currency":  func(r *refund.Request) { r.Currency = "x" },
		"zero":      func(r *refund.Request) { r.AmountMinor = 0 },
		"huge":      func(r *refund.Request) { r.AmountMinor = shared.MaxMinor + 1 },
		"reason":    func(r *refund.Request) { r.Reason = "WHIM" },
	}
	for name, mut := range cases {
		r := req()
		mut(&r)
		if _, err := refund.New(r, refund.Limits{CapturedMinor: shared.MaxMinor * 2}, t0); !errors.Is(err, shared.ErrValidation) {
			t.Errorf("%s: err = %v", name, err)
		}
	}
	r := req()
	r.GroupNo, r.Reason = 0, refund.ReasonManual
	if _, err := refund.New(r, lim(), t0); err != nil {
		t.Errorf("order-level manual refund: %v", err)
	}
}

func TestRecordSuccessRaisesRefundIssued(t *testing.T) {
	r := newRefund(t)
	events, err := r.RecordSuccess("prf-1", t0)
	if err != nil || len(events) != 1 || events[0].EventName() != "RefundIssued" {
		t.Fatalf("events = %v err %v", events, err)
	}
	e := events[0].(refund.RefundIssued)
	if e.Entity() != "refund" || e.SubjectID() != "ref-1" || e.AuthorizationID != "pay-1" || e.GroupNo != 1 || e.AmountMinor != 7000 || e.Reason != refund.ReasonLost {
		t.Errorf("event = %+v", e)
	}
	if r.State() != refund.StateSucceeded || r.ProviderRefundID() != "prf-1" || r.Attempts() != 1 || e.AggregateVersion() != 2 {
		t.Errorf("state/ref/attempts/version = %s/%s/%d/%d", r.State(), r.ProviderRefundID(), r.Attempts(), e.AggregateVersion())
	}
	if _, err = r.RecordSuccess("prf-2", t0); !errors.Is(err, shared.ErrIllegalTransition) {
		t.Errorf("second outcome err = %v", err)
	}
}

func TestRecordSuccessNeedsProviderReference(t *testing.T) {
	if _, err := newRefund(t).RecordSuccess("", t0); !errors.Is(err, shared.ErrValidation) {
		t.Errorf("err = %v", err)
	}
}

func TestRecordFailureRaisesRefundFailed(t *testing.T) {
	r := newRefund(t)
	events, err := r.RecordFailure("INSUFFICIENT_FUNDS", t0)
	if err != nil || len(events) != 1 || events[0].EventName() != "RefundFailed" {
		t.Fatalf("events = %v err %v", events, err)
	}
	e := events[0].(refund.RefundFailed)
	if e.Code != "INSUFFICIENT_FUNDS" || e.Attempts != 1 || e.Final || e.OrderID != "ord-1" || e.SubjectID() != "ref-1" || e.Entity() != "refund" {
		t.Errorf("event = %+v", e)
	}
	if r.State() != refund.StateFailed || r.FailureCode() != "INSUFFICIENT_FUNDS" || r.Exhausted() {
		t.Errorf("state/code/exhausted = %s/%s/%v", r.State(), r.FailureCode(), r.Exhausted())
	}
	if _, err = newRefund(t).RecordFailure("", t0); !errors.Is(err, shared.ErrValidation) {
		t.Errorf("empty code err = %v", err)
	}
	if _, err = r.RecordFailure("X", t0); !errors.Is(err, shared.ErrIllegalTransition) {
		t.Errorf("failure on FAILED err = %v", err)
	}
}

func TestRetryReturnsToRequestedAndClearsFailureOnSuccess(t *testing.T) {
	r := newRefund(t)
	failN(t, r, 2)
	if err := r.Retry(); err != nil || r.State() != refund.StateRequested {
		t.Fatalf("retry = %v state %s", err, r.State())
	}
	if _, err := r.RecordSuccess("prf-1", t0); err != nil || r.FailureCode() != "" || r.Attempts() != 3 {
		t.Errorf("success after retry: %v code %q attempts %d", err, r.FailureCode(), r.Attempts())
	}
	if err := newRefund(t).Retry(); !errors.Is(err, shared.ErrIllegalTransition) {
		t.Errorf("retry of REQUESTED err = %v", err)
	}
}

func TestRetryStopsAfterMaxAttempts(t *testing.T) {
	r := newRefund(t)
	failN(t, r, refund.MaxAttempts-1)
	if err := r.Retry(); err != nil {
		t.Fatalf("retry before the last attempt: %v", err)
	}
	events, err := r.RecordFailure("PROVIDER_REFUSED", t0)
	if err != nil || !events[0].(refund.RefundFailed).Final || !r.Exhausted() {
		t.Fatalf("last failure: %v final=%v exhausted=%v", err, events, r.Exhausted())
	}
	if err = r.Retry(); !errors.Is(err, shared.ErrIllegalTransition) || r.State() != refund.StateFailed {
		t.Errorf("retry past the cap: %v state %s", err, r.State())
	}
}

func TestSnapshotRestoreRoundTrip(t *testing.T) {
	r := newRefund(t)
	failN(t, r, 2)
	got, err := refund.Restore(r.Snapshot())
	if err != nil || got.State() != refund.StateFailed || got.Attempts() != 2 || got.Version() != r.Version() || got.LoadedVersion() != r.Version() {
		t.Fatalf("restore = %v %v", got, err)
	}
	ok := newRefund(t)
	if _, err = ok.RecordSuccess("prf", t0); err != nil {
		t.Fatal(err)
	}
	if got, err = refund.Restore(ok.Snapshot()); err != nil || got.ProviderRefundID() != "prf" {
		t.Errorf("restore succeeded = %v %v", got, err)
	}
	if got, err = refund.Restore(newRefund(t).Snapshot()); err != nil || got.State() != refund.StateRequested {
		t.Errorf("restore requested = %v %v", got, err)
	}
}

func TestRestoreRejectsCorruptSnapshots(t *testing.T) {
	cases := map[string]func(*refund.Snapshot){
		"request":     func(s *refund.Snapshot) { s.Request.Reason = "X" },
		"version":     func(s *refund.Snapshot) { s.Version = 0 },
		"attempts":    func(s *refund.Snapshot) { s.Attempts = refund.MaxAttempts + 1 },
		"negative":    func(s *refund.Snapshot) { s.Attempts = -1 },
		"state":       func(s *refund.Snapshot) { s.State = "DONE" },
		"no provider": func(s *refund.Snapshot) { s.State = refund.StateSucceeded },
		"no code":     func(s *refund.Snapshot) { s.State = refund.StateFailed },
	}
	for name, mut := range cases {
		s := newRefund(t).Snapshot()
		mut(&s)
		if _, err := refund.Restore(s); !errors.Is(err, shared.ErrInvalidSnapshot) {
			t.Errorf("%s: err = %v", name, err)
		}
	}
}
