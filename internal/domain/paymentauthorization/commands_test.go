package paymentauthorization_test

import (
	"testing"
	"time"

	pa "github.com/IQVO/customer-payments/internal/domain/paymentauthorization"
	"github.com/IQVO/customer-payments/internal/domain/shared"
)

func TestCaptureDerivesPartiallyCapturedThenCaptured(t *testing.T) {
	p := newAuth(t)
	capture(t, p, 1, 7000)
	if p.State() != pa.StatePartiallyCaptured {
		t.Fatalf("state = %s, want PARTIALLY_CAPTURED", p.State())
	}
	capture(t, p, 2, 3000)
	if p.State() != pa.StateCaptured || p.Live() {
		t.Fatalf("state = %s live=%v, want CAPTURED", p.State(), p.Live())
	}
}

func TestCaptureIsIdempotentPerGroup(t *testing.T) {
	p := newAuth(t)
	capture(t, p, 1, 7000)
	v := p.Version()
	events, err := p.Capture(1, 7000, []string{"X"}, "again", t0.Add(time.Hour))
	if err != nil || len(events) != 0 || p.Version() != v {
		t.Fatalf("second capture = %v %v version %d->%d", events, err, v, p.Version())
	}
	if a, _ := p.Allocation(1); a.TrackingNumber() != "TRK-1" {
		t.Errorf("tracking overwritten: %q", a.TrackingNumber())
	}
}

func TestCaptureRefusals(t *testing.T) {
	p := newAuth(t)
	_, err := p.Capture(9, 100, nil, "c", t0)
	wantErr(t, err, pa.ErrUnknownGroup)
	_, err = p.Capture(1, 7001, nil, "c", t0)
	wantErr(t, err, pa.ErrOverCapture)
	_, err = p.Capture(1, 0, nil, "c", t0)
	wantErr(t, err, pa.ErrOverCapture)
	_, err = p.Capture(1, 7000, nil, "c", approved().ExpiresAt)
	wantErr(t, err, pa.ErrCaptureAfterExpiry)
	if _, err = p.Capture(1, 7000, nil, "c", approved().ExpiresAt.Add(-time.Second)); err != nil {
		t.Errorf("capture just before expiry: %v", err)
	}
}

func TestCaptureAfterReleaseOrVoidIsIllegal(t *testing.T) {
	p := newAuth(t)
	_, err := p.Release(1, pa.ReasonCancelled, t0)
	if err != nil {
		t.Fatal(err)
	}
	_, err = p.Capture(1, 7000, nil, "c", t0)
	wantErr(t, err, shared.ErrIllegalTransition)
	q := newAuth(t)
	if _, err = q.Void(t0); err != nil {
		t.Fatal(err)
	}
	_, err = q.Capture(1, 7000, nil, "c", t0)
	wantErr(t, err, shared.ErrIllegalTransition)
}

func TestReleaseAllocation(t *testing.T) {
	p := newAuth(t)
	events, err := p.Release(2, pa.ReasonUnsourceable, t0)
	mustEvents(t, events, err, "PaymentReleased")
	if e := events[0].(pa.PaymentReleased); e.GroupNo != 2 || e.AmountMinor != 3000 || e.Reason != "UNSOURCEABLE" {
		t.Errorf("event = %+v", e)
	}
	if p.State() != pa.StateAuthorized {
		t.Errorf("state = %s, want AUTHORIZED while group 1 is pending", p.State())
	}
	if events, err = p.Release(2, pa.ReasonCancelled, t0); err != nil || len(events) != 0 {
		t.Errorf("repeat release = %v %v", events, err)
	}
	if _, err = p.Release(1, pa.ReasonCancelled, t0); err != nil {
		t.Fatal(err)
	}
	if p.State() != pa.StateReleased {
		t.Errorf("state = %s, want RELEASED", p.State())
	}
}

func TestReleaseRefusals(t *testing.T) {
	p := newAuth(t)
	capture(t, p, 1, 7000)
	_, err := p.Release(1, pa.ReasonCancelled, t0)
	wantErr(t, err, shared.ErrIllegalTransition)
	_, err = p.Release(9, pa.ReasonCancelled, t0)
	wantErr(t, err, pa.ErrUnknownGroup)
	_, err = p.Release(2, "VOIDED", t0)
	wantErr(t, err, shared.ErrValidation)
}

func TestReleaseAfterCapturedGroupDoesNotChangeCapturedState(t *testing.T) {
	p := newAuth(t)
	capture(t, p, 1, 7000)
	if _, err := p.Release(2, pa.ReasonCancelled, t0); err != nil {
		t.Fatal(err)
	}
	if p.State() != pa.StateCaptured {
		t.Errorf("state = %s, want CAPTURED (captured + released)", p.State())
	}
}

func TestVoidReleasesEveryPendingAllocation(t *testing.T) {
	p := newAuth(t)
	events, err := p.Void(t0)
	mustEvents(t, events, err, "PaymentReleased", "PaymentReleased")
	for _, e := range events {
		if e.(pa.PaymentReleased).Reason != "VOIDED" {
			t.Errorf("reason = %s", e.(pa.PaymentReleased).Reason)
		}
	}
	if p.State() != pa.StateVoided {
		t.Errorf("state = %s", p.State())
	}
	_, err = p.Void(t0)
	wantErr(t, err, shared.ErrIllegalTransition)
}

func TestVoidAfterCaptureIsIllegal(t *testing.T) {
	p := newAuth(t)
	capture(t, p, 1, 7000)
	_, err := p.Void(t0)
	wantErr(t, err, shared.ErrIllegalTransition)
}

func TestExpireReleasesPendingAndRaisesExpired(t *testing.T) {
	p := newAuth(t)
	capture(t, p, 1, 7000)
	events, err := p.Expire(false, t0.Add(8*24*time.Hour))
	mustEvents(t, events, err, "PaymentReleased", "PaymentAuthorizationExpired")
	if p.State() != pa.StateExpired {
		t.Errorf("state = %s", p.State())
	}
	_, err = p.Capture(2, 3000, nil, "c", t0)
	wantErr(t, err, pa.ErrCaptureAfterExpiry)
	_, err = p.Expire(false, t0)
	wantErr(t, err, shared.ErrIllegalTransition)
}

func TestExpireAfterDeclinedReauthRaisesPaymentDeclined(t *testing.T) {
	p := newAuth(t)
	events, err := p.Expire(true, t0)
	mustEvents(t, events, err, "PaymentReleased", "PaymentReleased", "PaymentAuthorizationExpired", "PaymentDeclined")
	if e := events[3].(pa.PaymentDeclined); e.Reason != "REAUTH_DECLINED" || e.AuthorizationID != "pay-1" {
		t.Errorf("declined = %+v", e)
	}
}

func TestReauthorizeKeepsStateAndMovesExpiry(t *testing.T) {
	p := newAuth(t)
	capture(t, p, 1, 7000)
	next := approved().ExpiresAt.Add(7 * 24 * time.Hour)
	events, err := p.Reauthorize("prov-2", next, t0)
	mustEvents(t, events, err, "PaymentReauthorized")
	if p.State() != pa.StatePartiallyCaptured || p.ProviderAuthorizationID() != "prov-2" || !p.ExpiresAt().Equal(next) || p.Reauthorizations() != 1 {
		t.Errorf("state/ref/expiry/count = %s/%s/%v/%d", p.State(), p.ProviderAuthorizationID(), p.ExpiresAt(), p.Reauthorizations())
	}
	if e := events[0].(pa.PaymentReauthorized); e.ProviderAuthorizationID != "prov-2" || !e.ExpiresAt.Equal(next) {
		t.Errorf("event = %+v", e)
	}
}

func TestReauthorizeRefusals(t *testing.T) {
	p := newAuth(t)
	_, err := p.Reauthorize("", approved().ExpiresAt.Add(time.Hour), t0)
	wantErr(t, err, shared.ErrValidation)
	_, err = p.Reauthorize("prov-2", approved().ExpiresAt, t0)
	wantErr(t, err, shared.ErrValidation)
	capture(t, p, 1, 7000)
	capture(t, p, 2, 3000)
	_, err = p.Reauthorize("prov-2", approved().ExpiresAt.Add(time.Hour), t0)
	wantErr(t, err, shared.ErrIllegalTransition)
}

func TestBindOrder(t *testing.T) {
	p := newAuth(t)
	changed, err := p.BindOrder("ord-1")
	if err != nil || !changed || p.OrderID() != "ord-1" {
		t.Fatalf("bind = %v %v order %q", changed, err, p.OrderID())
	}
	if changed, err = p.BindOrder("ord-1"); err != nil || changed {
		t.Errorf("rebind same = %v %v", changed, err)
	}
	_, err = p.BindOrder("ord-2")
	wantErr(t, err, pa.ErrOrderAlreadyBound)
	_, err = newAuth(t).BindOrder("a b")
	wantErr(t, err, shared.ErrValidation)
	d, _, _ := pa.New(draft(), pa.Outcome{DeclineReason: "X"}, t0)
	_, err = d.BindOrder("ord-1")
	wantErr(t, err, shared.ErrIllegalTransition)
}
