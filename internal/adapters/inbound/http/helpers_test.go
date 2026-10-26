package http_test

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	httpadapter "github.com/IQVO/customer-payments/internal/adapters/inbound/http"
	"github.com/IQVO/customer-payments/internal/adapters/outbound/memory"
	"github.com/IQVO/customer-payments/internal/application/parked"
	"github.com/IQVO/customer-payments/internal/application/usecases"
)

var t0 = time.Date(2026, 10, 24, 9, 0, 0, 0, time.UTC)

type env struct {
	t        *testing.T
	store    *memory.Store
	clock    *memory.Clock
	provider *memory.Provider
	deps     usecases.Deps
	ready    *httpadapter.Readiness
	srv      *httptest.Server
	api      *httpadapter.Server
	router   http.Handler
	gauge    func(context.Context) (int, error)
	keys     int
}

func newEnv(t *testing.T) *env {
	t.Helper()
	clock := memory.NewClock(t0)
	s := memory.NewStore(clock)
	prov := memory.NewProvider(clock)
	deps := usecases.Deps{
		Authorizations: s.Authorizations(), Refunds: s.Refunds(), Parked: s.Parked(), Inbox: s.Inbox(), Outbox: s.Outbox(),
		UoW: s.UnitOfWork(), Clock: clock, IDs: &memory.SequentialIDs{}, Provider: prov,
	}
	ready := httpadapter.NewReadiness(0, 0)
	api := &httpadapter.Server{
		AuthorizePayment: usecases.AuthorizePayment{Deps: deps}, VoidAuthorization: usecases.VoidAuthorization{Deps: deps},
		GetAuthorization: usecases.GetAuthorization{Deps: deps}, ListAuthorizations: usecases.ListAuthorizations{Deps: deps},
		RequestRefund: usecases.RequestRefund{Deps: deps}, RetryRefund: usecases.RetryRefund{Deps: deps},
		GetRefund: usecases.GetRefund{Deps: deps}, ListRefunds: usecases.ListRefunds{Deps: deps},
		IdempotencyStore: s.Idempotency(), Clock: clock, Readiness: ready,
	}
	e := &env{t: t, store: s, clock: clock, provider: prov, deps: deps, ready: ready, api: api}
	e.reroute()
	e.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { e.router.ServeHTTP(w, r) }))
	t.Cleanup(e.srv.Close)
	return e
}

type reply struct {
	Status int
	Header http.Header
	Body   string
}

func (r reply) json(t *testing.T) map[string]any {
	t.Helper()
	var m map[string]any
	if err := json.Unmarshal([]byte(r.Body), &m); err != nil {
		t.Fatalf("body is not JSON: %q (%v)", r.Body, err)
	}
	return m
}

// do sends one request; key "" sends no Idempotency-Key.
func (e *env) do(method, path, key, body string) reply {
	e.t.Helper()
	req, err := http.NewRequest(method, e.srv.URL+path, strings.NewReader(body))
	if err != nil {
		e.t.Fatal(err)
	}
	if key != "" {
		req.Header.Set("Idempotency-Key", key)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		e.t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	b, _ := io.ReadAll(resp.Body)
	return reply{Status: resp.StatusCode, Header: resp.Header, Body: string(b)}
}

// post sends a POST with a fresh Idempotency-Key.
func (e *env) post(path, body string) reply {
	e.t.Helper()
	e.keys++
	return e.do(http.MethodPost, path, "key-"+strconv.Itoa(e.keys), body)
}

func (e *env) get(path string) reply { e.t.Helper(); return e.do(http.MethodGet, path, "", "") }

func (e *env) wantStatus(r reply, status int) {
	e.t.Helper()
	if r.Status != status {
		e.t.Fatalf("status = %d, want %d: %s", r.Status, status, r.Body)
	}
}

func (e *env) wantProblem(r reply, status int, slug string) {
	e.t.Helper()
	e.wantStatus(r, status)
	if ct := r.Header.Get("Content-Type"); ct != "application/problem+json" {
		e.t.Fatalf("content-type = %q", ct)
	}
	if got := r.json(e.t)["type"]; got != "/problems/"+slug {
		e.t.Fatalf("problem type = %v, want /problems/%s (%s)", got, slug, r.Body)
	}
}

const authorizeBody = `{"checkoutSessionId":"cs-1","paymentMethodToken":"tok_visa","currency":"BRL","totalMinor":10000,` +
	`"allocations":[{"groupNo":1,"amountMinor":7000},{"groupNo":2,"amountMinor":3000}]}`

// authorized authorizes the default body and returns the authorization id.
func (e *env) authorized() string {
	e.t.Helper()
	r := e.post("/payment-authorizations", authorizeBody)
	e.wantStatus(r, http.StatusCreated)
	return r.json(e.t)["authorizationId"].(string)
}

// shipped binds ord-1 and captures both groups through the consumer.
func (e *env) shipped() string {
	e.t.Helper()
	id := e.authorized()
	c := usecases.Consumer{Deps: e.deps}
	ev := usecases.CheckoutCompletedEvent{SessionID: "cs-1", OrderID: "ord-1", AuthorizationID: id}
	if err := c.CheckoutCompleted(context.Background(), "evt-cc", "t", ev); err != nil {
		e.t.Fatal(err)
	}
	for g := 1; g <= 2; g++ {
		f := usecases.Fact{Kind: parked.Shipped, OrderID: "ord-1", GroupNo: g, TrackingNumbers: []string{fmt.Sprintf("TRK%d", g)}}
		if err := c.ApplyFact(context.Background(), fmt.Sprintf("evt-s%d", g), "t", f); err != nil {
			e.t.Fatal(err)
		}
	}
	return id
}

// reroute rebuilds the router after a test changed the server's settings.
func (e *env) reroute() {
	e.api.ParkedFacts = e.gauge
	e.router = httpadapter.NewRouter(e.api)
}
