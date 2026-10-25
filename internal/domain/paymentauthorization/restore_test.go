package paymentauthorization_test

import (
	"errors"
	"testing"
	"time"

	pa "github.com/IQVO/customer-payments/internal/domain/paymentauthorization"
	"github.com/IQVO/customer-payments/internal/domain/shared"
)

func TestSnapshotRestoreRoundTrip(t *testing.T) {
	p := newAuth(t)
	capture(t, p, 1, 7000)
	if _, err := p.BindOrder("ord-1"); err != nil {
		t.Fatal(err)
	}
	r, err := pa.Restore(p.Snapshot())
	if err != nil {
		t.Fatalf("Restore: %v", err)
	}
	if r.State() != p.State() || r.OrderID() != "ord-1" || r.Version() != p.Version() || r.LoadedVersion() != p.Version() {
		t.Errorf("restored = %s %q v%d loaded %d", r.State(), r.OrderID(), r.Version(), r.LoadedVersion())
	}
	if a, _ := r.Allocation(1); a.TrackingNumber() != "TRK-1" || a.CapturedMinor != 7000 {
		t.Errorf("allocation 1 = %+v", a)
	}
	if r.PaymentMethodToken() != "tok_visa" || r.Currency() != "BRL" || r.TotalMinor() != 10000 || r.CheckoutSessionID() != "cs-1" || r.ID() != "pay-1" {
		t.Error("scalar fields lost")
	}
	if !r.CreatedAt().Equal(t0) {
		t.Errorf("createdAt = %v", r.CreatedAt())
	}
}

func TestRestoreDeclinedRoundTrip(t *testing.T) {
	p, _, _ := pa.New(draft(), pa.Outcome{DeclineReason: "CARD_DECLINED"}, t0)
	r, err := pa.Restore(p.Snapshot())
	if err != nil || r.State() != pa.StateDeclined || r.DeclineReason() != "CARD_DECLINED" {
		t.Fatalf("restore declined = %v %v", r, err)
	}
}

func TestRestoreRejectsCorruptSnapshots(t *testing.T) {
	cases := map[string]func(*pa.Snapshot){
		"version":         func(s *pa.Snapshot) { s.Version = 0 },
		"bad id":          func(s *pa.Snapshot) { s.ID = "" },
		"bad order":       func(s *pa.Snapshot) { s.OrderID = "a b" },
		"bad terminal":    func(s *pa.Snapshot) { s.Terminal = pa.StateCaptured },
		"no provider":     func(s *pa.Snapshot) { s.ProviderAuthorizationID = "" },
		"no expiry":       func(s *pa.Snapshot) { s.ExpiresAt = time.Time{} },
		"sum":             func(s *pa.Snapshot) { s.TotalMinor++ },
		"unordered":       func(s *pa.Snapshot) { s.Allocations[0], s.Allocations[1] = s.Allocations[1], s.Allocations[0] },
		"unknown state":   func(s *pa.Snapshot) { s.Allocations[0].State = "X" },
		"capture amount":  func(s *pa.Snapshot) { s.Allocations[0].State = pa.AllocCaptured },
		"pending carries": func(s *pa.Snapshot) { s.Allocations[0].CapturedMinor = 5 },
		"declined reason": func(s *pa.Snapshot) { s.Terminal = pa.StateDeclined },
	}
	for name, mut := range cases {
		s := newAuth(t).Snapshot()
		mut(&s)
		if _, err := pa.Restore(s); err == nil {
			t.Errorf("%s: Restore succeeded", name)
		} else if !errors.Is(err, shared.ErrInvalidSnapshot) {
			t.Errorf("%s: error %v is not ErrInvalidSnapshot", name, err)
		}
	}
}
