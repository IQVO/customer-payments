package paymentauthorization_test

import (
	"strings"
	"testing"
	"time"

	pa "github.com/IQVO/customer-payments/internal/domain/paymentauthorization"
	"github.com/IQVO/customer-payments/internal/domain/shared"
)

func TestNewApprovedRaisesPaymentAuthorized(t *testing.T) {
	p, events, err := pa.New(draft(), approved(), t0)
	mustEvents(t, events, err, "PaymentAuthorized")
	if p.State() != pa.StateAuthorized || p.Version() != 1 || p.LoadedVersion() != 0 {
		t.Fatalf("state/version/loaded = %s/%d/%d", p.State(), p.Version(), p.LoadedVersion())
	}
	e := events[0].(pa.PaymentAuthorized)
	if e.TotalMinor != 10000 || len(e.Allocations) != 2 || e.Allocations[0].GroupNo != 1 || !e.ExpiresAt.Equal(approved().ExpiresAt) {
		t.Errorf("event = %+v", e)
	}
	if p.PendingMinor() != 10000 || p.CapturedMinor() != 0 || !p.Live() {
		t.Errorf("pending/captured/live = %d/%d/%v", p.PendingMinor(), p.CapturedMinor(), p.Live())
	}
	p.MarkPersisted()
	if p.LoadedVersion() != 1 {
		t.Errorf("LoadedVersion after MarkPersisted = %d", p.LoadedVersion())
	}
}

func TestNewDeclinedIsTerminalAndRaisesPaymentDeclined(t *testing.T) {
	p, events, err := pa.New(draft(), pa.Outcome{DeclineReason: "CARD_DECLINED"}, t0)
	mustEvents(t, events, err, "PaymentDeclined")
	if p.State() != pa.StateDeclined || p.Live() || p.DeclineReason() != "CARD_DECLINED" {
		t.Fatalf("state = %s live=%v reason=%q", p.State(), p.Live(), p.DeclineReason())
	}
	if e := events[0].(pa.PaymentDeclined); e.AuthorizationID != "pay-1" || e.CheckoutSessionID != "cs-1" || e.Reason != "CARD_DECLINED" {
		t.Errorf("event = %+v", e)
	}
	if _, err := p.Capture(1, 100, nil, "c", t0); err == nil {
		t.Error("a declined authorization must not capture")
	}
}

func TestNewRejectsApprovedWithoutReferenceOrFutureExpiry(t *testing.T) {
	for _, out := range []pa.Outcome{
		{ExpiresAt: t0.Add(time.Hour)},
		{ProviderAuthorizationID: "p", ExpiresAt: t0},
	} {
		if _, _, err := pa.New(draft(), out, t0); err == nil {
			t.Errorf("New with %+v succeeded", out)
		}
	}
}

func TestCheckAllocationMismatch(t *testing.T) {
	cases := map[string]func(*pa.Draft){
		"sum below total": func(d *pa.Draft) { d.TotalMinor = 10001 },
		"sum above total": func(d *pa.Draft) { d.TotalMinor = 9999 },
		"duplicate group": func(d *pa.Draft) { d.Allocations[1].GroupNo = 2 },
	}
	for name, mut := range cases {
		d := draft()
		mut(&d)
		wantErr(t, pa.Check(d), pa.ErrAllocationMismatch)
		_ = name
	}
}

func TestCheckValidation(t *testing.T) {
	cases := map[string]func(*pa.Draft){
		"bad id":         func(d *pa.Draft) { d.ID = "" },
		"bad session":    func(d *pa.Draft) { d.CheckoutSessionID = "a b" },
		"empty token":    func(d *pa.Draft) { d.PaymentMethodToken = "" },
		"long token":     func(d *pa.Draft) { d.PaymentMethodToken = strings.Repeat("t", pa.MaxTokenLen+1) },
		"bad currency":   func(d *pa.Draft) { d.Currency = "brl" },
		"negative total": func(d *pa.Draft) { d.TotalMinor = -1 },
		"no allocations": func(d *pa.Draft) { d.Allocations = nil },
		"group zero":     func(d *pa.Draft) { d.Allocations[0].GroupNo = 0 },
		"negative alloc": func(d *pa.Draft) { d.Allocations[0].AmountMinor = -5 },
	}
	for name, mut := range cases {
		d := draft()
		mut(&d)
		if err := pa.Check(d); err == nil {
			t.Errorf("%s: Check succeeded", name)
		}
	}
	d := draft()
	d.Allocations = make([]pa.AllocationInput, pa.MaxGroups+1)
	for i := range d.Allocations {
		d.Allocations[i] = pa.AllocationInput{GroupNo: i + 1}
	}
	d.TotalMinor = 0
	wantErr(t, pa.Check(d), shared.ErrValidation)
	d.Allocations = d.Allocations[:pa.MaxGroups]
	if err := pa.Check(d); err != nil {
		t.Errorf("MaxGroups allocations: %v", err)
	}
}

func TestAllocationLookups(t *testing.T) {
	p := newAuth(t)
	capture(t, p, 1, 7000)
	if a, ok := p.Allocation(1); !ok || a.TrackingNumber() != "TRK-1" || a.State != pa.AllocCaptured {
		t.Errorf("Allocation(1) = %+v %v", a, ok)
	}
	if a, ok := p.Allocation(2); !ok || a.TrackingNumber() != "" {
		t.Errorf("Allocation(2) = %+v %v", a, ok)
	}
	if _, ok := p.Allocation(9); ok {
		t.Error("Allocation(9) found")
	}
	if g, ok := p.GroupOfTracking("TRK-1"); !ok || g != 1 {
		t.Errorf("GroupOfTracking = %d %v", g, ok)
	}
	if _, ok := p.GroupOfTracking("nope"); ok {
		t.Error("GroupOfTracking(nope) found")
	}
	if p.CapturedMinor() != 7000 || p.PendingMinor() != 3000 {
		t.Errorf("captured/pending = %d/%d", p.CapturedMinor(), p.PendingMinor())
	}
}
