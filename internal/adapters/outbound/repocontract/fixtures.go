package repocontract

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/IQVO/customer-payments/internal/domain/paymentauthorization"
	"github.com/IQVO/customer-payments/internal/domain/refund"
	"github.com/IQVO/customer-payments/internal/domain/shared"
)

// newAuth builds an unsaved AUTHORIZED authorization for a session, created m
// minutes after t0 and expiring seven days after that.
func newAuth(t *testing.T, id, session string, m int) *paymentauthorization.PaymentAuthorization {
	t.Helper()
	p, _, err := paymentauthorization.New(paymentauthorization.Draft{
		ID: paymentauthorization.ID(id), CheckoutSessionID: session, PaymentMethodToken: "tok_visa", Currency: "BRL", TotalMinor: 10000,
		Allocations: []paymentauthorization.AllocationInput{{GroupNo: 1, AmountMinor: 7000}, {GroupNo: 2, AmountMinor: 3000}},
	}, paymentauthorization.Outcome{ProviderAuthorizationID: "prov-" + id, ExpiresAt: at(m).Add(7 * 24 * time.Hour)}, at(m))
	fatalIf(t, err, "new authorization "+id)
	return p
}

func declinedAuth(t *testing.T, id, session string) *paymentauthorization.PaymentAuthorization {
	t.Helper()
	p, _, err := paymentauthorization.New(paymentauthorization.Draft{
		ID: paymentauthorization.ID(id), CheckoutSessionID: session, PaymentMethodToken: "tok_x", Currency: "BRL", TotalMinor: 100,
		Allocations: []paymentauthorization.AllocationInput{{GroupNo: 1, AmountMinor: 100}},
	}, paymentauthorization.Outcome{DeclineReason: "CARD_DECLINED"}, t0)
	fatalIf(t, err, "declined authorization "+id)
	return p
}

func savedAuth(t *testing.T, e Env, id, session string, m int) *paymentauthorization.PaymentAuthorization {
	t.Helper()
	p := newAuth(t, id, session, m)
	fatalIf(t, e.Authorizations.Save(ctx(), p), "save authorization "+id)
	return p
}

func authEvents(t *testing.T, id string) []shared.Event {
	t.Helper()
	p, events, err := paymentauthorization.New(paymentauthorization.Draft{
		ID: paymentauthorization.ID(id), CheckoutSessionID: "cs-" + id, PaymentMethodToken: "tok", Currency: "BRL", TotalMinor: 100,
		Allocations: []paymentauthorization.AllocationInput{{GroupNo: 1, AmountMinor: 100}},
	}, paymentauthorization.Outcome{ProviderAuthorizationID: "p", ExpiresAt: at(60)}, t0)
	fatalIf(t, err, "events")
	_ = p
	return events
}

func sameAuth(t *testing.T, got, want *paymentauthorization.PaymentAuthorization) {
	t.Helper()
	g, _ := json.Marshal(got.Snapshot())
	w, _ := json.Marshal(want.Snapshot())
	if string(g) != string(w) {
		t.Fatalf("round trip changed the authorization:\n got %s\nwant %s", g, w)
	}
}

func newRefund(t *testing.T, id, authID, order string, m int) *refund.Refund {
	t.Helper()
	f, err := refund.New(refund.Request{
		ID: refund.ID(id), AuthorizationID: authID, OrderID: order, GroupNo: 1, Currency: "BRL", AmountMinor: 700, Reason: refund.ReasonLost,
	}, refund.Limits{CapturedMinor: 7000, OrderCapturedMinor: 7000}, at(m))
	fatalIf(t, err, "new refund "+id)
	return f
}
