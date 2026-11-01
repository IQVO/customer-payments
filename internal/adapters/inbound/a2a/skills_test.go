package a2a

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/a2aproject/a2a-go/v2/a2a"
	"github.com/a2aproject/a2a-go/v2/a2aclient"
	"github.com/a2aproject/a2a-go/v2/a2asrv"

	"github.com/IQVO/customer-payments/internal/adapters/outbound/memory"
	"github.com/IQVO/customer-payments/internal/application/pagination"
	"github.com/IQVO/customer-payments/internal/application/ports"
	"github.com/IQVO/customer-payments/internal/domain/paymentauthorization"
	"github.com/IQVO/customer-payments/internal/domain/refund"
	"github.com/IQVO/customer-payments/internal/domain/shared"
)

var t0 = time.Date(2026, 10, 24, 10, 0, 0, 0, time.UTC)

func must(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}

// seededStore holds: a declined attempt (pay-0) and a live authorization
// (pay-1) in checkout session cs-1, the latter bound to ord-1 with group 1
// captured under tracking number TRK1; and one FAILED refund (ref-1) of ord-1.
// The provider hold id and the PSP token are seeded on purpose: no answer may
// carry either.
func seededStore(t *testing.T) *memory.Store {
	t.Helper()
	store := memory.NewStore(memory.NewClock(t0))
	ctx := context.Background()

	declined, _, err := paymentauthorization.New(paymentauthorization.Draft{
		ID: "pay-0", CheckoutSessionID: "cs-1", PaymentMethodToken: "tok_declined", Currency: "BRL", TotalMinor: 10000,
		Allocations: []paymentauthorization.AllocationInput{{GroupNo: 1, AmountMinor: 10000}},
	}, paymentauthorization.Outcome{DeclineReason: "INSUFFICIENT_FUNDS"}, t0.Add(-time.Hour))
	must(t, err)
	must(t, store.Authorizations().Save(ctx, declined))

	p, _, err := paymentauthorization.New(paymentauthorization.Draft{
		ID: "pay-1", CheckoutSessionID: "cs-1", PaymentMethodToken: "tok_visa", Currency: "BRL", TotalMinor: 10000,
		Allocations: []paymentauthorization.AllocationInput{{GroupNo: 1, AmountMinor: 7000}, {GroupNo: 2, AmountMinor: 3000}},
	}, paymentauthorization.Outcome{ProviderAuthorizationID: "hold-1", ExpiresAt: t0.Add(7 * 24 * time.Hour)}, t0)
	must(t, err)
	_, err = p.BindOrder("ord-1")
	must(t, err)
	_, err = p.Capture(1, 7000, []string{"TRK1"}, "cap-1", t0.Add(time.Hour))
	must(t, err)
	must(t, store.Authorizations().Save(ctx, p))

	f, err := refund.New(refund.Request{
		ID: "ref-1", AuthorizationID: "pay-1", OrderID: "ord-1", GroupNo: 1, Currency: "BRL", AmountMinor: 7000, Reason: refund.ReasonLost,
	}, refund.Limits{CapturedMinor: 7000, OrderCapturedMinor: 7000}, t0)
	must(t, err)
	_, err = f.RecordFailure("ACCOUNT_CLOSED", t0)
	must(t, err)
	must(t, store.Refunds().Save(ctx, f))
	return store
}

func depsOver(store *memory.Store) Deps { return NewDeps(store.Authorizations(), store.Refunds()) }

// ---- the wire: JSON-RPC over HTTP, driven by the SDK's own client ----------

// wireEnvelope is a TEST-LOCAL copy of the answer contract. If the adapter's
// envelope drifts, this suite fails instead of silently following it.
type wireEnvelope struct {
	Skill string `json:"skill"`
	Data  struct {
		Authorizations *struct {
			Authorizations []wireAuthorization `json:"authorizations"`
		} `json:"authorizations"`
		Refunds *struct {
			Refunds []wireRefund `json:"refunds"`
		} `json:"refunds"`
	} `json:"data"`
	Problem *struct {
		Type   string `json:"type"`
		Title  string `json:"title"`
		Detail string `json:"detail"`
		Status int    `json:"status"`
	} `json:"problem"`
}

type wireAuthorization struct {
	AuthorizationID   string `json:"authorizationId"`
	CheckoutSessionID string `json:"checkoutSessionId"`
	OrderID           string `json:"orderId"`
	State             string `json:"state"`
	Currency          string `json:"currency"`
	TotalMinor        int64  `json:"totalMinor"`
	Allocations       []struct {
		GroupNo         int      `json:"groupNo"`
		AmountMinor     int64    `json:"amountMinor"`
		State           string   `json:"state"`
		TrackingNumbers []string `json:"trackingNumbers"`
	} `json:"allocations"`
	ExpiresAt     string `json:"expiresAt"`
	DeclineReason string `json:"declineReason"`
}

type wireRefund struct {
	RefundID        string `json:"refundId"`
	AuthorizationID string `json:"authorizationId"`
	OrderID         string `json:"orderId"`
	GroupNo         int    `json:"groupNo"`
	AmountMinor     int64  `json:"amountMinor"`
	Reason          string `json:"reason"`
	State           string `json:"state"`
	Attempts        int    `json:"attempts"`
	FailureCode     string `json:"failureCode"`
}

// answerParts is everything one reply carried: the decoded envelope, the raw
// JSON of the data part and the one-line text summary.
type answerParts struct {
	env  wireEnvelope
	raw  string
	text string
}

// all is every string the agent said, for leak assertions.
func (a answerParts) all() string { return a.raw + "\n" + a.text }

type wire struct{ client *a2aclient.Client }

func newWire(t *testing.T, deps Deps) *wire {
	t.Helper()
	srv := httptest.NewServer(Handler(NewExecutor(deps)))
	t.Cleanup(srv.Close)
	card := Card(Config{AgentURL: srv.URL + "/", Version: "test"})
	client, err := a2aclient.NewFromCard(context.Background(), card)
	if err != nil {
		t.Fatalf("NewFromCard: %v", err)
	}
	return &wire{client: client}
}

// call sends one message part and returns the agent's reply.
func (w *wire) call(t *testing.T, part *a2a.Part) answerParts {
	t.Helper()
	resp, err := w.client.SendMessage(context.Background(), &a2a.SendMessageRequest{Message: a2a.NewMessage(a2a.MessageRoleUser, part)})
	if err != nil {
		t.Fatalf("SendMessage: %v", err)
	}
	msg, ok := resp.(*a2a.Message)
	if !ok || len(msg.Parts) < 2 {
		t.Fatalf("reply = %T, want a message with a data and a text part", resp)
	}
	data, ok := msg.Parts[0].Content.(a2a.Data)
	if !ok {
		t.Fatalf("first reply part is %T, want data", msg.Parts[0].Content)
	}
	raw, _ := json.Marshal(data.Value)
	var env wireEnvelope
	if err := json.Unmarshal(raw, &env); err != nil {
		t.Fatalf("reply is not the skill envelope: %v (%s)", err, raw)
	}
	text, _ := msg.Parts[1].Content.(a2a.Text)
	return answerParts{env: env, raw: string(raw), text: string(text)}
}

func (w *wire) skill(t *testing.T, skill string, args map[string]any) answerParts {
	t.Helper()
	return w.call(t, a2a.NewDataPart(map[string]any{"skill": skill, "arguments": args}))
}

func wantProblem(t *testing.T, a answerParts, status int, slug string) {
	t.Helper()
	p := a.env.Problem
	if p == nil {
		t.Fatalf("expected a %d problem, got data: %s", status, a.raw)
	}
	if p.Status != status || p.Type != problemTypeBase+slug {
		t.Fatalf("problem = %+v, want %d %s", p, status, slug)
	}
	if a.env.Data.Authorizations != nil || a.env.Data.Refunds != nil {
		t.Fatalf("a problem answer must carry no data: %s", a.raw)
	}
}

func TestWireGetPaymentStatusByOrder(t *testing.T) {
	w := newWire(t, depsOver(seededStore(t)))

	a := w.skill(t, skillGetPaymentStatus, map[string]any{"orderId": "ord-1"})
	l := a.env.Data.Authorizations
	if a.env.Problem != nil || l == nil || len(l.Authorizations) != 1 {
		t.Fatalf("answer = %s", a.raw)
	}
	p := l.Authorizations[0]
	if p.AuthorizationID != "pay-1" || p.CheckoutSessionID != "cs-1" || p.OrderID != "ord-1" || p.State != "PARTIALLY_CAPTURED" ||
		p.Currency != "BRL" || p.TotalMinor != 10000 || p.ExpiresAt != "2026-10-31T10:00:00Z" || p.DeclineReason != "" {
		t.Fatalf("authorization = %+v", p)
	}
	assertSeededAllocations(t, p)
	if a.text != "1 payment authorizations: pay-1 PARTIALLY_CAPTURED 10000 BRL" {
		t.Fatalf("summary = %q", a.text)
	}
}

// assertSeededAllocations checks the two allocations of pay-1: group 1
// captured under TRK1, group 2 still pending with an empty (not null) tracking
// list.
func assertSeededAllocations(t *testing.T, p wireAuthorization) {
	t.Helper()
	if len(p.Allocations) != 2 {
		t.Fatalf("allocations = %+v", p.Allocations)
	}
	first, second := p.Allocations[0], p.Allocations[1]
	if first.GroupNo != 1 || first.AmountMinor != 7000 || first.State != "CAPTURED" || len(first.TrackingNumbers) != 1 || first.TrackingNumbers[0] != "TRK1" {
		t.Fatalf("first allocation = %+v", first)
	}
	if second.State != "PENDING" || second.TrackingNumbers == nil || len(second.TrackingNumbers) != 0 {
		t.Fatalf("second allocation = %+v", second)
	}
}

func TestWireGetPaymentStatusBySessionIncludesDeclinedAttempts(t *testing.T) {
	w := newWire(t, depsOver(seededStore(t)))

	a := w.skill(t, skillGetPaymentStatus, map[string]any{"checkoutSessionId": "cs-1"})
	l := a.env.Data.Authorizations
	if a.env.Problem != nil || l == nil || len(l.Authorizations) != 2 {
		t.Fatalf("answer = %s", a.raw)
	}
	if l.Authorizations[0].AuthorizationID != "pay-0" || l.Authorizations[0].State != "DECLINED" || l.Authorizations[0].DeclineReason != "INSUFFICIENT_FUNDS" ||
		l.Authorizations[1].AuthorizationID != "pay-1" {
		t.Fatalf("authorizations = %+v", l.Authorizations)
	}
}

func TestWireGetPaymentStatusByID(t *testing.T) {
	w := newWire(t, depsOver(seededStore(t)))

	a := w.skill(t, skillGetPaymentStatus, map[string]any{"authorizationId": "pay-1"})
	l := a.env.Data.Authorizations
	if a.env.Problem != nil || l == nil || len(l.Authorizations) != 1 || l.Authorizations[0].AuthorizationID != "pay-1" || l.Authorizations[0].State != "PARTIALLY_CAPTURED" {
		t.Fatalf("answer = %s", a.raw)
	}
}

// An order or session with nothing behind it is an empty list, exactly as REST
// and MCP answer it, never a problem.
func TestWireUnknownOrderAndSessionAreEmptyListsNotProblems(t *testing.T) {
	w := newWire(t, depsOver(seededStore(t)))

	for name, args := range map[string]map[string]any{"order": {"orderId": "ord-ghost"}, "session": {"checkoutSessionId": "cs-ghost"}} {
		t.Run(name, func(t *testing.T) {
			a := w.skill(t, skillGetPaymentStatus, args)
			if a.env.Problem != nil || a.env.Data.Authorizations == nil || a.env.Data.Authorizations.Authorizations == nil || len(a.env.Data.Authorizations.Authorizations) != 0 {
				t.Fatalf("answer = %s, want an empty authorizations array", a.raw)
			}
			if a.text != "0 payment authorizations" {
				t.Fatalf("summary = %q", a.text)
			}
		})
	}
	a := w.skill(t, skillGetRefundStatus, map[string]any{"orderId": "ord-ghost"})
	if a.env.Problem != nil || a.env.Data.Refunds == nil || a.env.Data.Refunds.Refunds == nil || len(a.env.Data.Refunds.Refunds) != 0 {
		t.Fatalf("answer = %s, want an empty refunds array", a.raw)
	}
}

func TestWireGetRefundStatus(t *testing.T) {
	w := newWire(t, depsOver(seededStore(t)))

	for name, args := range map[string]map[string]any{"by order": {"orderId": "ord-1"}, "by id": {"refundId": "ref-1"}} {
		t.Run(name, func(t *testing.T) {
			a := w.skill(t, skillGetRefundStatus, args)
			l := a.env.Data.Refunds
			if a.env.Problem != nil || l == nil || len(l.Refunds) != 1 {
				t.Fatalf("answer = %s", a.raw)
			}
			r := l.Refunds[0]
			if r.RefundID != "ref-1" || r.AuthorizationID != "pay-1" || r.OrderID != "ord-1" || r.GroupNo != 1 || r.AmountMinor != 7000 ||
				r.Reason != "LOST" || r.State != "FAILED" || r.Attempts != 1 || r.FailureCode != "ACCOUNT_CLOSED" {
				t.Fatalf("refund = %+v", r)
			}
			if a.text != "1 refunds: ref-1 FAILED 7000 (LOST)" {
				t.Fatalf("summary = %q", a.text)
			}
		})
	}
}

// The card promises "never sees or returns a card number, a PSP token or any
// payment instrument": the seeded PSP token, the provider's hold id and the
// field names that would carry them must appear in no answer part.
func TestAnswersNeverCarryProviderOrCardData(t *testing.T) {
	w := newWire(t, depsOver(seededStore(t)))

	answers := []answerParts{
		w.skill(t, skillGetPaymentStatus, map[string]any{"orderId": "ord-1"}),
		w.skill(t, skillGetPaymentStatus, map[string]any{"checkoutSessionId": "cs-1"}),
		w.skill(t, skillGetPaymentStatus, map[string]any{"authorizationId": "pay-1"}),
		w.skill(t, skillGetRefundStatus, map[string]any{"orderId": "ord-1"}),
		w.skill(t, skillGetRefundStatus, map[string]any{"refundId": "ref-1"}),
	}
	forbidden := []string{"tok_visa", "tok_declined", "hold-1", "providerAuthorizationId", "providerRefundId", "paymentMethod", "PaymentMethodToken", "cardNumber"}
	for _, a := range answers {
		if a.env.Problem != nil {
			t.Fatalf("answer = %s", a.raw)
		}
		lower := strings.ToLower(a.all())
		for _, s := range forbidden {
			if strings.Contains(lower, strings.ToLower(s)) {
				t.Errorf("an answer mentions %q: %s", s, a.all())
			}
		}
	}
}

func TestWireUnknownIDsAreAnsweredAs404NotRaised(t *testing.T) {
	w := newWire(t, depsOver(seededStore(t)))

	cases := map[string]struct {
		skill  string
		args   map[string]any
		detail string
	}{
		"authorization": {skillGetPaymentStatus, map[string]any{"authorizationId": "pay-ghost"}, `no payment authorization exists with authorizationId "pay-ghost"`},
		"refund":        {skillGetRefundStatus, map[string]any{"refundId": "ref-ghost"}, `no refund exists with refundId "ref-ghost"`},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			a := w.skill(t, tc.skill, tc.args)
			wantProblem(t, a, 404, "not-found")
			if a.env.Problem.Detail != tc.detail || a.env.Problem.Title != "Not found" {
				t.Fatalf("problem = %+v", a.env.Problem)
			}
			// The shared use case error text is "not found"; the adapter owns the
			// answer text and must not echo the internal sentinel.
			if strings.Contains(a.env.Problem.Detail, "not found") {
				t.Fatalf("an answer leaks the internal error text: %q", a.env.Problem.Detail)
			}
		})
	}
}

// An over-long id cannot name a resource: a path-style id is a miss (404) like
// REST and MCP, a listing key is a malformed request.
func TestWireOverLongIDs(t *testing.T) {
	w := newWire(t, depsOver(seededStore(t)))
	long := strings.Repeat("x", maxIDLength+1)

	wantProblem(t, w.skill(t, skillGetPaymentStatus, map[string]any{"authorizationId": long}), 404, "not-found")
	wantProblem(t, w.skill(t, skillGetRefundStatus, map[string]any{"refundId": long}), 404, "not-found")
	wantProblem(t, w.skill(t, skillGetPaymentStatus, map[string]any{"orderId": long}), 400, "malformed-request")
	wantProblem(t, w.skill(t, skillGetRefundStatus, map[string]any{"orderId": long}), 400, "malformed-request")
	// Exactly the guard length is still a lookup.
	exact := strings.Repeat("x", maxIDLength)
	wantProblem(t, w.skill(t, skillGetPaymentStatus, map[string]any{"authorizationId": exact}), 404, "not-found")
}

// failingStore makes every read fail, standing in for a database outage.
type failingAuthorizations struct{ ports.AuthorizationRepository }

type failingRefunds struct{ ports.RefundRepository }

var errBoom = errors.New("pgx: connection refused to 10.0.0.7:5432 password=hunter2")

func (failingAuthorizations) Get(context.Context, paymentauthorization.ID) (*paymentauthorization.PaymentAuthorization, error) {
	return nil, errBoom
}

func (failingAuthorizations) ListBySession(context.Context, string) ([]*paymentauthorization.PaymentAuthorization, error) {
	return nil, errBoom
}

func (failingAuthorizations) PageBySession(context.Context, string, *pagination.Keyset, int) ([]*paymentauthorization.PaymentAuthorization, error) {
	return nil, errBoom
}

func (failingAuthorizations) FindByOrder(context.Context, string) (*paymentauthorization.PaymentAuthorization, error) {
	return nil, errBoom
}

func (failingRefunds) Get(context.Context, refund.ID) (*refund.Refund, error) { return nil, errBoom }

func (failingRefunds) ListByOrder(context.Context, string, *pagination.Keyset, int) ([]*refund.Refund, error) {
	return nil, errBoom
}

// An unexpected failure is answered as a 500 problem, never a JSON-RPC error,
// and its raw text (which may carry infrastructure detail) is not echoed.
func TestWireUnexpectedFailureIs500AndDoesNotLeakTheCause(t *testing.T) {
	store := memory.NewStore(memory.NewClock(t0))
	w := newWire(t, NewDeps(failingAuthorizations{store.Authorizations()}, failingRefunds{store.Refunds()}))

	cases := map[string]struct {
		skill string
		args  map[string]any
	}{
		"authorization by id":       {skillGetPaymentStatus, map[string]any{"authorizationId": "pay-1"}},
		"authorizations by order":   {skillGetPaymentStatus, map[string]any{"orderId": "ord-1"}},
		"authorizations by session": {skillGetPaymentStatus, map[string]any{"checkoutSessionId": "cs-1"}},
		"refund by id":              {skillGetRefundStatus, map[string]any{"refundId": "ref-1"}},
		"refunds by order":          {skillGetRefundStatus, map[string]any{"orderId": "ord-1"}},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			a := w.skill(t, tc.skill, tc.args)
			wantProblem(t, a, 500, "internal-error")
			for _, leak := range []string{"hunter2", "10.0.0.7", "pgx", "connection refused"} {
				if strings.Contains(a.all(), leak) {
					t.Fatalf("a 500 answer leaks the underlying error (%q): %s", leak, a.all())
				}
			}
		})
	}
}

// A use case ErrValidation maps to the REST validation-failed class (422) with
// adapter-owned text; ErrNotFound and anything else are covered on the wire.
func TestProblemFromUseCaseClassification(t *testing.T) {
	v := problemFromUseCase(shared.Invalid("secret internal wording"), "")
	if v.Status != 422 || v.Type != problemTypeBase+"validation-failed" || strings.Contains(v.Detail, "secret internal wording") {
		t.Fatalf("validation problem = %+v", v)
	}
	n := problemFromUseCase(ports.ErrNotFound, "adapter text")
	if n.Status != 404 || n.Detail != "adapter text" {
		t.Fatalf("not-found problem = %+v", n)
	}
	// Wrapped sentinels classify too (errors.Is), like REST.
	if w := problemFromUseCase(errors.Join(errors.New("ctx"), ports.ErrNotFound), "x"); w.Status != 404 {
		t.Fatalf("wrapped not-found problem = %+v", w)
	}
	if c := problemFromUseCase(ports.ErrConcurrentUpdate, ""); c.Status != 500 {
		t.Fatalf("a write-only class must not be answered as its REST 409 by a read: %+v", c)
	}
}

func TestWireArgumentValidation(t *testing.T) {
	w := newWire(t, depsOver(seededStore(t)))
	cases := map[string]struct {
		skill  string
		args   map[string]any
		status int
		slug   string
	}{
		"payment: no selector":              {skillGetPaymentStatus, map[string]any{}, 422, "validation-failed"},
		"payment: blank selector":           {skillGetPaymentStatus, map[string]any{"orderId": "   "}, 422, "validation-failed"},
		"payment: two selectors":            {skillGetPaymentStatus, map[string]any{"orderId": "ord-1", "checkoutSessionId": "cs-1"}, 422, "validation-failed"},
		"payment: three selectors":          {skillGetPaymentStatus, map[string]any{"orderId": "ord-1", "checkoutSessionId": "cs-1", "authorizationId": "pay-1"}, 422, "validation-failed"},
		"payment: id and order":             {skillGetPaymentStatus, map[string]any{"orderId": "ord-1", "authorizationId": "pay-1"}, 422, "validation-failed"},
		"payment: selector wrong type":      {skillGetPaymentStatus, map[string]any{"orderId": 7}, 400, "malformed-request"},
		"payment: unknown argument":         {skillGetPaymentStatus, map[string]any{"orderId": "ord-1", "force": true}, 400, "malformed-request"},
		"payment: typo'd argument":          {skillGetPaymentStatus, map[string]any{"orderIdd": "ord-1"}, 400, "malformed-request"},
		"payment: wrong-skill argument":     {skillGetPaymentStatus, map[string]any{"refundId": "ref-1"}, 400, "malformed-request"},
		"payment: legacy snake_case key":    {skillGetPaymentStatus, map[string]any{"order_id": "ord-1"}, 400, "malformed-request"},
		"refund: no selector":               {skillGetRefundStatus, map[string]any{}, 422, "validation-failed"},
		"refund: two selectors":             {skillGetRefundStatus, map[string]any{"orderId": "ord-1", "refundId": "ref-1"}, 422, "validation-failed"},
		"refund: blank selector":            {skillGetRefundStatus, map[string]any{"refundId": ""}, 422, "validation-failed"},
		"refund: selector wrong type":       {skillGetRefundStatus, map[string]any{"refundId": []string{"ref-1"}}, 400, "malformed-request"},
		"refund: unknown argument":          {skillGetRefundStatus, map[string]any{"orderId": "ord-1", "limit": 5}, 400, "malformed-request"},
		"refund: wrong-skill argument":      {skillGetRefundStatus, map[string]any{"authorizationId": "pay-1"}, 400, "malformed-request"},
		"refund: checkoutSessionId unknown": {skillGetRefundStatus, map[string]any{"checkoutSessionId": "cs-1"}, 400, "malformed-request"},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			wantProblem(t, w.skill(t, tc.skill, tc.args), tc.status, tc.slug)
		})
	}
}

func TestWireSelectorProblemNamesTheSurfacesArguments(t *testing.T) {
	w := newWire(t, depsOver(seededStore(t)))

	a := w.skill(t, skillGetPaymentStatus, map[string]any{})
	if a.env.Problem == nil || a.env.Problem.Detail != "give exactly one of authorizationId, orderId, checkoutSessionId" {
		t.Fatalf("answer = %s", a.raw)
	}
	a = w.skill(t, skillGetRefundStatus, map[string]any{})
	if a.env.Problem == nil || a.env.Problem.Detail != "give exactly one of refundId, orderId" {
		t.Fatalf("answer = %s", a.raw)
	}
}

func TestWireRequestShapeProblems(t *testing.T) {
	w := newWire(t, depsOver(seededStore(t)))

	wantProblem(t, w.call(t, a2a.NewTextPart("not json at all")), 400, "malformed-request")
	wantProblem(t, w.call(t, a2a.NewDataPart(map[string]any{"arguments": map[string]any{}})), 400, "malformed-request")
	wantProblem(t, w.call(t, a2a.NewDataPart(map[string]any{"skill": skillGetRefundStatus, "arguments": map[string]any{"orderId": "ord-1"}, "extra": 1})), 400, "malformed-request")

	// Trailing data after the JSON document is refused too.
	wantProblem(t, w.call(t, a2a.NewTextPart(`{"skill":"get_refund_status","arguments":{"orderId":"ord-1"}} {"skill":"get_payment_status"}`)), 400, "malformed-request")

	wantProblem(t, w.skill(t, "launch_rocket", map[string]any{}), 400, "unknown-skill")

	// A text part holding the same JSON is accepted (documented contract).
	a := w.call(t, a2a.NewTextPart(`{"skill":"get_refund_status","arguments":{"refundId":"ref-1"}}`))
	if a.env.Problem != nil || a.env.Data.Refunds == nil || len(a.env.Data.Refunds.Refunds) != 1 {
		t.Fatalf("a text part holding the skill JSON must be accepted, got %s", a.raw)
	}
}

// The write operations of the REST surface (and the MCP tool names) are not
// offered here: asking for one is an unknown skill, answered in-band, and
// nothing is written.
func TestWireOffersNoWriteSkill(t *testing.T) {
	store := seededStore(t)
	w := newWire(t, depsOver(store))

	for _, skill := range []string{"authorize_payment", "void_authorization", "request_refund", "retry_refund", "capture_payment", "create_payment_authorization", "get_payment_authorization", "list_refunds"} {
		wantProblem(t, w.skill(t, skill, map[string]any{"orderId": "ord-1", "authorizationId": "pay-9"}), 400, "unknown-skill")
	}
	if _, err := store.Authorizations().Get(context.Background(), "pay-9"); !errors.Is(err, ports.ErrNotFound) {
		t.Fatalf("a write skill name must not write anything: %v", err)
	}
}

// TestCardIsServedAndDialable proves discovery end to end: the card handler
// serves the card at the well-known path and a client built from the FETCHED
// card reaches the agent.
func TestCardIsServedAndDialable(t *testing.T) {
	mux := http.NewServeMux()
	mux.Handle("/", Handler(NewExecutor(depsOver(seededStore(t)))))
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	mux.Handle(a2asrv.WellKnownAgentCardPath, a2asrv.NewStaticAgentCardHandler(Card(Config{AgentURL: srv.URL + "/", Version: "test"})))

	res, err := http.Get(srv.URL + a2asrv.WellKnownAgentCardPath)
	if err != nil || res.StatusCode != http.StatusOK {
		t.Fatalf("card fetch = %v / %v", res, err)
	}
	defer func() { _ = res.Body.Close() }()
	var card a2a.AgentCard
	if err := json.NewDecoder(res.Body).Decode(&card); err != nil {
		t.Fatal(err)
	}
	client, err := a2aclient.NewFromCard(context.Background(), &card)
	if err != nil {
		t.Fatal(err)
	}
	w := &wire{client: client}
	a := w.skill(t, skillGetRefundStatus, map[string]any{"refundId": "ref-1"})
	if a.env.Data.Refunds == nil {
		t.Fatalf("a client built from the served card could not call the agent: %s", a.raw)
	}
}
