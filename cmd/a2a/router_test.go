package main

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/a2aproject/a2a-go/v2/a2a"
	"github.com/a2aproject/a2a-go/v2/a2aclient"
	"github.com/a2aproject/a2a-go/v2/a2asrv"

	inbounda2a "github.com/IQVO/customer-payments/internal/adapters/inbound/a2a"
	"github.com/IQVO/customer-payments/internal/adapters/outbound/memory"
	"github.com/IQVO/customer-payments/internal/domain/paymentauthorization"
)

// newTestAgent builds the handler exactly as run() does (newAgent), over
// in-memory repositories holding one authorization. The memory adapter is a
// test double: it is imported by this _test file only, never by the binary.
func newTestAgent(t *testing.T) http.Handler {
	t.Helper()
	t0 := time.Date(2026, 10, 24, 10, 0, 0, 0, time.UTC)
	store := memory.NewStore(memory.NewClock(t0))
	p, _, err := paymentauthorization.New(paymentauthorization.Draft{
		ID: "pay-1", CheckoutSessionID: "cs-1", PaymentMethodToken: "tok_visa", Currency: "BRL", TotalMinor: 5000,
		Allocations: []paymentauthorization.AllocationInput{{GroupNo: 1, AmountMinor: 5000}},
	}, paymentauthorization.Outcome{ProviderAuthorizationID: "hold-1", ExpiresAt: t0.Add(24 * time.Hour)}, t0)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Authorizations().Save(context.Background(), p); err != nil {
		t.Fatal(err)
	}
	deps := inbounda2a.NewDeps(store.Authorizations(), store.Refunds())
	return newAgent(deps, testAgentURL, "test", quietLogger(), "customer-payments-a2a")
}

const getPaymentBody = `{"jsonrpc":"2.0","id":1,"method":"SendMessage","params":{"message":{"messageId":"m1","role":"ROLE_USER","parts":[{"data":{"skill":"get_payment_status","arguments":{"authorizationId":"pay-1"}}}]}}}`

func TestRouter_HealthzServesOK(t *testing.T) {
	h := newTestAgent(t)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/healthz", nil))

	if rec.Code != http.StatusOK {
		t.Fatalf("GET /healthz: got %d, want 200 (body %q)", rec.Code, rec.Body.String())
	}
	if got := rec.Body.String(); got != `{"status":"ok"}` {
		t.Fatalf("GET /healthz body = %q, want {\"status\":\"ok\"}", got)
	}
	if ct := rec.Header().Get("Content-Type"); ct != "application/json" {
		t.Fatalf("GET /healthz Content-Type = %q, want application/json", ct)
	}
}

func TestRouter_ServesTheAgentCardAtTheWellKnownPath(t *testing.T) {
	h := newTestAgent(t)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/.well-known/agent-card.json", nil))

	if rec.Code != http.StatusOK {
		t.Fatalf("GET agent card: got %d, want 200", rec.Code)
	}
	body := rec.Body.String()
	for _, want := range []string{`"name":"customer-payments"`, testAgentURL, `"get_payment_status"`, `"get_refund_status"`} {
		if !strings.Contains(body, want) {
			t.Errorf("agent card lacks %s: %s", want, body)
		}
	}
	if strings.Contains(body, "securitySchemes") {
		t.Errorf("the card must declare no security schemes: %s", body)
	}
}

// TestRouter_BothMountsReachTheJSONRPCHandler pins the fleet's
// <CTX>_A2A_ENDPOINT convention: the endpoint answers at "/" and at "/a2a",
// and the use cases behind it are the wired ones (the seeded authorization is
// read) with NO credentials.
func TestRouter_BothMountsReachTheJSONRPCHandler(t *testing.T) {
	h := newTestAgent(t)
	for _, path := range []string{"/", "/a2a"} {
		t.Run(path, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(getPaymentBody))
			req.Header.Set("Content-Type", "application/json")
			req.Header.Set("A2A-Version", "1.0")
			rec := httptest.NewRecorder()
			h.ServeHTTP(rec, req)

			raw, _ := io.ReadAll(rec.Body)
			if rec.Code == http.StatusNotFound || !strings.Contains(string(raw), `"jsonrpc"`) {
				t.Fatalf("POST %s: %d %q does not look like a JSON-RPC reply", path, rec.Code, raw)
			}
			if !strings.Contains(string(raw), `"authorizationId":"pay-1"`) || !strings.Contains(string(raw), `"totalMinor":5000`) {
				t.Fatalf("POST %s: reply %q lacks the seeded authorization read through the wired use case", path, raw)
			}
			if strings.Contains(string(raw), "tok_visa") || strings.Contains(string(raw), "hold-1") {
				t.Fatalf("POST %s: the reply carries PSP data: %q", path, raw)
			}
		})
	}
}

// The binary reads no API key: requests carrying any Authorization header
// behave identically to those without.
func TestRouter_NoAuthIsRequiredOrChecked(t *testing.T) {
	h := newTestAgent(t)
	for _, auth := range []string{"", "Bearer nonsense"} {
		req := httptest.NewRequest(http.MethodPost, "/a2a", strings.NewReader(getPaymentBody))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("A2A-Version", "1.0")
		if auth != "" {
			req.Header.Set("Authorization", auth)
		}
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"authorizationId":"pay-1"`) {
			t.Fatalf("Authorization=%q: %d %q, want the unauthenticated answer", auth, rec.Code, rec.Body.String())
		}
	}
}

// Only the documented paths exist.
func TestRouter_UnknownPathsAreNotTheAgent(t *testing.T) {
	h := newTestAgent(t)
	for _, path := range []string{"/mcp", "/a2a/extra", "/anything"} {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, path, strings.NewReader(getPaymentBody)))
		if rec.Code != http.StatusNotFound {
			t.Errorf("POST %s = %d, want 404", path, rec.Code)
		}
	}
}

// TestRouter_ACardFetchedFromTheBinaryIsDialable runs the discovery flow a
// delegating agent runs against the real router: fetch the card, build a
// client from it, call a skill.
func TestRouter_ACardFetchedFromTheBinaryIsDialable(t *testing.T) {
	srv := httptest.NewServer(newTestAgent(t))
	t.Cleanup(srv.Close)

	res, err := http.Get(srv.URL + a2asrv.WellKnownAgentCardPath)
	if err != nil || res.StatusCode != http.StatusOK {
		t.Fatalf("card fetch = %v / %v", res, err)
	}
	defer func() { _ = res.Body.Close() }()
	var card a2a.AgentCard
	if err := json.NewDecoder(res.Body).Decode(&card); err != nil {
		t.Fatal(err)
	}
	// The card advertises the configured URL, not the test server's: point the
	// client at the test server the way a gateway-rewritten route would.
	card.SupportedInterfaces[0].URL = srv.URL + "/a2a"
	client, err := a2aclient.NewFromCard(context.Background(), &card)
	if err != nil {
		t.Fatal(err)
	}
	resp, err := client.SendMessage(context.Background(), &a2a.SendMessageRequest{Message: a2a.NewMessage(a2a.MessageRoleUser, a2a.NewDataPart(map[string]any{"skill": "get_payment_status", "arguments": map[string]any{"checkoutSessionId": "cs-1"}}))})
	if err != nil {
		t.Fatal(err)
	}
	msg, ok := resp.(*a2a.Message)
	if !ok || len(msg.Parts) < 2 {
		t.Fatalf("reply = %T", resp)
	}
	if text, _ := msg.Parts[1].Content.(a2a.Text); !strings.Contains(string(text), "pay-1") {
		t.Fatalf("summary = %q, want the seeded authorization listed", text)
	}
}
