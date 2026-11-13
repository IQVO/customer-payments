//go:build integration

package main

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/a2aproject/a2a-go/v2/a2a"
	"github.com/a2aproject/a2a-go/v2/a2aclient"

	inbounda2a "github.com/IQVO/customer-payments/internal/adapters/inbound/a2a"
	"github.com/IQVO/customer-payments/internal/adapters/outbound/postgres"
	"github.com/IQVO/customer-payments/internal/domain/paymentauthorization"
	"github.com/IQVO/customer-payments/internal/domain/refund"
	"github.com/IQVO/customer-payments/internal/testing/pgtest"
)

func TestMain(m *testing.M) {
	code := m.Run()
	pgtest.Shutdown()
	os.Exit(code)
}

// The binary migrates a database itself (idempotently: pgtest's databases are
// already migrated, a second run must be a no-op), then the real JSON-RPC
// router, over the cmd's own wiring, answers the two documented skill
// questions (ADR 0005) from the state the command side persisted — and its
// reads add no outbox row: the agent is read-only (ADR 0005; ADR 0007 pins
// both skills at L1), so nothing can leave this binary for a relay to publish.
func TestA2AAgainstPostgres_AnswersTheDocumentedSkillsAndNeverWrites(t *testing.T) {
	url := pgtest.URL(t) // a fresh, already migrated database
	ctx := context.Background()

	pool, err := postgres.NewPool(ctx, url)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	countOutbox := func() int {
		t.Helper()
		var n int
		if err := pool.QueryRow(ctx, `SELECT count(*) FROM outbox_events`).Scan(&n); err != nil {
			t.Fatalf("count outbox_events: %v", err)
		}
		return n
	}

	seedPayments(t, url)
	before := countOutbox()

	deps, closeFn, err := buildDeps(ctx, quietLogger(), url, url)
	if err != nil {
		t.Fatalf("buildDeps: %v", err)
	}
	t.Cleanup(closeFn)

	srv := httptest.NewServer(newAgent(deps, "http://placeholder.invalid/", "it", quietLogger(), "customer-payments-a2a"))
	t.Cleanup(srv.Close)
	client, err := a2aclient.NewFromCard(ctx, inbounda2a.Card(inbounda2a.Config{AgentURL: srv.URL + "/", Version: "it"}))
	if err != nil {
		t.Fatal(err)
	}

	assertSkillAnswers(t, client)

	if after := countOutbox(); after != before {
		t.Fatalf("outbox rows %d -> %d: the A2A binary wrote an event", before, after)
	}
}

// seedPayments persists what the command side owns, the documented way: the
// domain constructors and transitions the write use cases drive, saved through
// the Postgres repositories cmd/api wires. pay-it-1 is an authorized hold
// bound to ord-it-1, pay-it-2 a declined attempt on the same checkout session
// (the card documents that a session lists declined attempts too), ref-it-1 a
// succeeded refund of group 1 and ref-it-2 a failed one of group 2. Repository
// Save writes no outbox row — in production the unit of work adds those — so
// the read-only assertion starts from zero.
func seedPayments(t *testing.T, url string) {
	t.Helper()
	ctx := context.Background()
	pool, err := postgres.NewPool(ctx, url)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	auths := postgres.NewAuthorizationRepository(pool)
	t0 := time.Date(2026, 11, 13, 10, 0, 0, 0, time.UTC)

	pay1, _, err := paymentauthorization.New(paymentauthorization.Draft{
		ID: "pay-it-1", CheckoutSessionID: "cs-it-1", PaymentMethodToken: "tok_visa", Currency: "BRL", TotalMinor: 5000,
		Allocations: []paymentauthorization.AllocationInput{{GroupNo: 1, AmountMinor: 3000}, {GroupNo: 2, AmountMinor: 2000}},
	}, paymentauthorization.Outcome{ProviderAuthorizationID: "hold-1", ExpiresAt: t0.Add(7 * 24 * time.Hour)}, t0)
	if err != nil {
		t.Fatalf("authorize pay-it-1: %v", err)
	}
	if _, err := pay1.BindOrder("ord-it-1"); err != nil {
		t.Fatalf("bind ord-it-1: %v", err)
	}
	if err := auths.Save(ctx, pay1); err != nil {
		t.Fatalf("save pay-it-1: %v", err)
	}

	pay2, _, err := paymentauthorization.New(paymentauthorization.Draft{
		ID: "pay-it-2", CheckoutSessionID: "cs-it-1", PaymentMethodToken: "tok_mastercard", Currency: "BRL", TotalMinor: 5000,
		Allocations: []paymentauthorization.AllocationInput{{GroupNo: 1, AmountMinor: 5000}},
	}, paymentauthorization.Outcome{DeclineReason: "insufficient_funds"}, t0.Add(time.Minute))
	if err != nil {
		t.Fatalf("decline pay-it-2: %v", err)
	}
	if err := auths.Save(ctx, pay2); err != nil {
		t.Fatalf("save pay-it-2: %v", err)
	}

	refundRepo := postgres.NewRefundRepository(pool)
	ref1, err := refund.New(refund.Request{
		ID: "ref-it-1", AuthorizationID: "pay-it-1", OrderID: "ord-it-1", GroupNo: 1,
		Currency: "BRL", AmountMinor: 1500, Reason: refund.ReasonLost,
	}, refund.Limits{CapturedMinor: 3000, OrderCapturedMinor: 5000}, t0.Add(2*time.Minute))
	if err != nil {
		t.Fatalf("request ref-it-1: %v", err)
	}
	if _, err := ref1.RecordSuccess("pr-1", t0.Add(3*time.Minute)); err != nil {
		t.Fatalf("record ref-it-1 success: %v", err)
	}
	if err := refundRepo.Save(ctx, ref1); err != nil {
		t.Fatalf("save ref-it-1: %v", err)
	}

	ref2, err := refund.New(refund.Request{
		ID: "ref-it-2", AuthorizationID: "pay-it-1", OrderID: "ord-it-1", GroupNo: 2,
		Currency: "BRL", AmountMinor: 500, Reason: refund.ReasonReturnedToSender,
	}, refund.Limits{CapturedMinor: 2000, OrderCapturedMinor: 5000, OrderRefundedMinor: 1500}, t0.Add(4*time.Minute))
	if err != nil {
		t.Fatalf("request ref-it-2: %v", err)
	}
	if _, err := ref2.RecordFailure("PSP_TIMEOUT", t0.Add(5*time.Minute)); err != nil {
		t.Fatalf("record ref-it-2 failure: %v", err)
	}
	if err := refundRepo.Save(ctx, ref2); err != nil {
		t.Fatalf("save ref-it-2: %v", err)
	}
}

// envelopeOf sends one skill call and returns the decoded answer envelope (a
// test-local shape) and everything the agent put on the wire.
func envelopeOf(t *testing.T, client *a2aclient.Client, skill string, args map[string]any) (map[string]any, string) {
	t.Helper()
	msg := a2a.NewMessage(a2a.MessageRoleUser, a2a.NewDataPart(map[string]any{"skill": skill, "arguments": args}))
	resp, err := client.SendMessage(context.Background(), &a2a.SendMessageRequest{Message: msg})
	if err != nil {
		t.Fatalf("%s: %v", skill, err)
	}
	reply, ok := resp.(*a2a.Message)
	if !ok || len(reply.Parts) < 2 {
		t.Fatalf("%s: reply = %T", skill, resp)
	}
	data, ok := reply.Parts[0].Content.(a2a.Data)
	if !ok {
		t.Fatalf("%s: first part is %T", skill, reply.Parts[0].Content)
	}
	raw, _ := json.Marshal(data.Value)
	var env map[string]any
	if err := json.Unmarshal(raw, &env); err != nil {
		t.Fatal(err)
	}
	return env, string(raw)
}

// dataOf returns the envelope's data object, nil-safe: a problem-only
// envelope carries none.
func dataOf(env map[string]any) map[string]any {
	d, _ := env["data"].(map[string]any)
	return d
}

// listOf unwraps one of the envelope's skill-union members to its list: the
// answer nests the list DTO (authorizations/refunds) inside the union field
// of the same name, the shape the adapter's own tests pin.
func listOf(env map[string]any, key string) []any {
	member, _ := dataOf(env)[key].(map[string]any)
	l, _ := member[key].([]any)
	return l
}

// assertSkillAnswers asserts the documented answers (ADR 0005 and the Agent
// Card's skill descriptions): one authorization by id with its allocations and
// expiry, an order's authorizations without the session's declined attempts, a
// session's authorizations including them, an empty list for an unknown order,
// a 404 problem for an unknown authorization, and both refund reads carrying
// state, reason, group, attempts and failure code — with none of the provider
// references or PSP tokens the aggregates hold crossing the boundary.
func assertSkillAnswers(t *testing.T, client *a2aclient.Client) {
	t.Helper()
	var wire strings.Builder

	env, raw := envelopeOf(t, client, "get_payment_status", map[string]any{"authorizationId": "pay-it-1"})
	wire.WriteString(raw)
	auths := listOf(env, "authorizations")
	if env["problem"] != nil || len(auths) != 1 {
		t.Fatalf("get_payment_status by id = %s", raw)
	}
	auth := auths[0].(map[string]any)
	if auth["state"] != "AUTHORIZED" || auth["checkoutSessionId"] != "cs-it-1" || auth["orderId"] != "ord-it-1" ||
		auth["currency"] != "BRL" || auth["totalMinor"] != 5000.0 || auth["expiresAt"] != "2026-11-20T10:00:00Z" {
		t.Fatalf("get_payment_status by id = %s", raw)
	}
	if allocs := auth["allocations"].([]any); len(allocs) != 2 ||
		allocs[0].(map[string]any)["groupNo"] != 1.0 || allocs[0].(map[string]any)["amountMinor"] != 3000.0 ||
		allocs[0].(map[string]any)["state"] != "PENDING" || len(allocs[0].(map[string]any)["trackingNumbers"].([]any)) != 0 ||
		allocs[1].(map[string]any)["amountMinor"] != 2000.0 {
		t.Fatalf("get_payment_status allocations = %v", auth["allocations"])
	}

	env, raw = envelopeOf(t, client, "get_payment_status", map[string]any{"orderId": "ord-it-1"})
	wire.WriteString(raw)
	auths = listOf(env, "authorizations")
	if env["problem"] != nil || len(auths) != 1 || auths[0].(map[string]any)["authorizationId"] != "pay-it-1" {
		t.Fatalf("an order's authorizations are exactly the bound one, got %s", raw)
	}

	env, raw = envelopeOf(t, client, "get_payment_status", map[string]any{"checkoutSessionId": "cs-it-1"})
	wire.WriteString(raw)
	auths = listOf(env, "authorizations")
	if env["problem"] != nil || len(auths) != 2 {
		t.Fatalf("a checkout session lists its declined attempts too, got %s", raw)
	}
	declined := auths[1].(map[string]any)
	if declined["authorizationId"] != "pay-it-2" || declined["state"] != "DECLINED" || declined["declineReason"] != "insufficient_funds" {
		t.Fatalf("the declined attempt = %v", declined)
	}
	if _, has := declined["expiresAt"]; has {
		t.Fatalf("a declined attempt holds nothing that can expire: %v", declined)
	}

	env, raw = envelopeOf(t, client, "get_payment_status", map[string]any{"orderId": "ord-it-ghost"})
	wire.WriteString(raw)
	if auths = listOf(env, "authorizations"); env["problem"] != nil || len(auths) != 0 {
		t.Fatalf("an unknown order is an empty list, not a problem, got %s", raw)
	}

	env, raw = envelopeOf(t, client, "get_payment_status", map[string]any{"authorizationId": "pay-it-ghost"})
	wire.WriteString(raw)
	problem, _ := env["problem"].(map[string]any)
	if problem["status"] != 404.0 || problem["type"] != "/problems/not-found" {
		t.Fatalf("an unknown authorization must be a 404 not-found problem, got %s", raw)
	}

	env, raw = envelopeOf(t, client, "get_refund_status", map[string]any{"refundId": "ref-it-2"})
	wire.WriteString(raw)
	refunds := listOf(env, "refunds")
	if env["problem"] != nil || len(refunds) != 1 {
		t.Fatalf("get_refund_status by id = %s", raw)
	}
	r2 := refunds[0].(map[string]any)
	if r2["state"] != "FAILED" || r2["reason"] != "RETURNED_TO_SENDER" || r2["groupNo"] != 2.0 || r2["amountMinor"] != 500.0 ||
		r2["attempts"] != 1.0 || r2["failureCode"] != "PSP_TIMEOUT" || r2["authorizationId"] != "pay-it-1" || r2["orderId"] != "ord-it-1" {
		t.Fatalf("the failed refund = %v", r2)
	}

	env, raw = envelopeOf(t, client, "get_refund_status", map[string]any{"orderId": "ord-it-1"})
	wire.WriteString(raw)
	refunds = listOf(env, "refunds")
	if env["problem"] != nil || len(refunds) != 2 {
		t.Fatalf("an order's refunds = %s", raw)
	}
	r1 := refunds[0].(map[string]any)
	if r1["refundId"] != "ref-it-1" || r1["state"] != "SUCCEEDED" || r1["attempts"] != 1.0 {
		t.Fatalf("an order's refunds are oldest first and carry the attempts, got %s", raw)
	}
	if _, has := r1["failureCode"]; has {
		t.Fatalf("a succeeded refund carries no failure code: %v", r1)
	}

	for _, secret := range []string{"tok_visa", "tok_mastercard", "hold-1", "pr-1"} {
		if strings.Contains(wire.String(), secret) {
			t.Fatalf("the answers leak %q across the boundary", secret)
		}
	}
}
