//go:build integration

package main

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	inboundhttp "github.com/IQVO/customer-payments/internal/adapters/inbound/http"
	"github.com/IQVO/customer-payments/internal/adapters/outbound/clock"
	"github.com/IQVO/customer-payments/internal/adapters/outbound/ids"
	outboundkafka "github.com/IQVO/customer-payments/internal/adapters/outbound/kafka"
	"github.com/IQVO/customer-payments/internal/adapters/outbound/paymentprovider"
	"github.com/IQVO/customer-payments/internal/adapters/outbound/postgres"
	"github.com/IQVO/customer-payments/internal/application/usecases"
	"github.com/IQVO/customer-payments/internal/testing/pgtest"
	"github.com/IQVO/customer-payments/internal/testing/providerfake"
)

// The Idempotency-Key contract (REST guideline 2) over the real router, the real
// Postgres adapters (testcontainers) and the real provider client against a local
// fake of the provider API. No Kafka: the outbox rows are only counted.

type idemRig struct {
	url  string
	rows func(t *testing.T, query string, args ...any) int // runs a count(*) query
	fake *providerfake.Fake
}

func newIdemRig(t *testing.T) *idemRig {
	t.Helper()
	pool := pgtest.NewPool(t)
	fake := providerfake.New(t)
	client, err := paymentprovider.New(fake.URL(), nil)
	if err != nil {
		t.Fatal(err)
	}
	outbox := postgres.NewOutboxRepo(pool, outboundkafka.NewEncoder())
	d := usecases.Deps{
		Authorizations: postgres.NewAuthorizationRepository(pool), Refunds: postgres.NewRefundRepository(pool),
		Parked: postgres.NewParkedFacts(pool), Inbox: postgres.NewInbox(pool), Outbox: outbox,
		UoW: postgres.NewUnitOfWork(pool), Clock: clock.System{}, IDs: ids.Generator{}, Provider: client,
	}
	srv := &inboundhttp.Server{
		AuthorizePayment: usecases.AuthorizePayment{Deps: d}, VoidAuthorization: usecases.VoidAuthorization{Deps: d},
		GetAuthorization: usecases.GetAuthorization{Deps: d}, ListAuthorizations: usecases.ListAuthorizations{Deps: d},
		RequestRefund: usecases.RequestRefund{Deps: d}, RetryRefund: usecases.RetryRefund{Deps: d},
		GetRefund: usecases.GetRefund{Deps: d}, ListRefunds: usecases.ListRefunds{Deps: d},
		IdempotencyStore: postgres.NewIdempotencyStore(pool), Clock: clock.System{},
	}
	ts := httptest.NewServer(inboundhttp.NewRouter(srv, slog.New(slog.DiscardHandler), ""))
	t.Cleanup(ts.Close)
	rows := func(t *testing.T, query string, args ...any) int {
		t.Helper()
		var n int
		if err := pool.QueryRow(context.Background(), query, args...).Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n
	}
	return &idemRig{url: ts.URL, rows: rows, fake: fake}
}

type idemReply struct {
	status   int
	body     string
	replayed string
	retired  string
}

func (r *idemRig) post(t *testing.T, path, key, body string) idemReply {
	t.Helper()
	req, err := http.NewRequest(http.MethodPost, r.url+path, strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	if key != "" {
		req.Header.Set("Idempotency-Key", key)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	b, _ := io.ReadAll(resp.Body)
	return idemReply{status: resp.StatusCode, body: string(b), replayed: resp.Header.Get("Idempotency-Replayed"), retired: resp.Header.Get("Idempotent-Replayed")}
}

func (r *idemRig) authorizations(t *testing.T, session string) int {
	return r.rows(t, `SELECT count(*) FROM payment_authorizations WHERE checkout_session_id = $1`, session)
}

func wantIdem(t *testing.T, got idemReply, status int, slug string) {
	t.Helper()
	if got.status != status || (slug != "" && !strings.Contains(got.body, `"/problems/`+slug+`"`)) {
		t.Fatalf("answer = %d %s, want %d %s", got.status, got.body, status, slug)
	}
}

func TestIdempotencyFreshReplayReuseAndMissing(t *testing.T) {
	r := newIdemRig(t)
	body := authorizeBody("cs-i1")
	first := r.post(t, "/payment-authorizations", "key-1", body)
	wantIdem(t, first, 201, "")
	if first.replayed != "" || first.retired != "" {
		t.Fatalf("the first execution carries a replay header: %+v", first)
	}
	again := r.post(t, "/payment-authorizations", "key-1", body)
	if again.status != 201 || again.body != first.body || again.replayed != "true" || again.retired != "" {
		t.Fatalf("replay = %+v, want byte-identical 201 with Idempotency-Replayed: true", again)
	}
	wantIdem(t, r.post(t, "/payment-authorizations", "key-1", authorizeBody("cs-i1-other")), 409, "idempotency-key-reused")
	wantIdem(t, r.post(t, "/refunds", "key-1", `{"orderId":"ord-1","amountMinor":5,"reason":"MANUAL"}`), 409, "idempotency-key-reused")
	wantIdem(t, r.post(t, "/payment-authorizations", "", body), 400, "idempotency-key-required")
	wantIdem(t, r.post(t, "/payment-authorizations", strings.Repeat("k", 129), body), 400, "idempotency-key-invalid")
	if n := r.authorizations(t, "cs-i1"); n != 1 || r.fake.Effects("authorize") != 1 {
		t.Fatalf("authorizations = %d, provider effects = %d, want 1 and 1", n, r.fake.Effects("authorize"))
	}
}

func TestIdempotencyConcurrentRequestsRunOnce(t *testing.T) {
	r := newIdemRig(t)
	const callers = 8
	var wg sync.WaitGroup
	replies := make([]idemReply, callers)
	for i := range callers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			replies[i] = r.post(t, "/payment-authorizations", "key-race", authorizeBody("cs-race"))
		}()
	}
	wg.Wait()
	replays := 0
	for i, got := range replies {
		if got.status != 201 || got.body != replies[0].body {
			t.Fatalf("caller %d: %+v, want the same 201 for all", i, got)
		}
		if got.replayed == "true" {
			replays++
		}
	}
	if replays != callers-1 || r.authorizations(t, "cs-race") != 1 || r.fake.Effects("authorize") != 1 {
		t.Fatalf("replays = %d (want %d), rows = %d, provider effects = %d", replays, callers-1, r.authorizations(t, "cs-race"), r.fake.Effects("authorize"))
	}
}

func TestIdempotencyCachesClientErrorsAndNeverStoresServerErrors(t *testing.T) {
	r := newIdemRig(t)
	bad := strings.Replace(authorizeBody("cs-bad"), `"totalMinor":10000`, `"totalMinor":9999`, 1)
	first := r.post(t, "/payment-authorizations", "key-4xx", bad)
	wantIdem(t, first, 422, "allocation-mismatch")
	again := r.post(t, "/payment-authorizations", "key-4xx", bad)
	if again.status != 422 || again.body != first.body || again.replayed != "true" {
		t.Fatalf("a 4xx is cached and replayed: %+v", again)
	}

	// A 5xx rolls back the aggregate, the outbox rows and the key row together.
	r.fake.SetDown(true)
	wantIdem(t, r.post(t, "/payment-authorizations", "key-5xx", authorizeBody("cs-5xx")), 503, "payment-provider-unavailable")
	keys := r.rows(t, `SELECT count(*) FROM idempotency_keys WHERE key = 'key-5xx'`)
	if keys != 0 || r.authorizations(t, "cs-5xx") != 0 {
		t.Fatalf("a 5xx left rows behind: key rows %d, authorizations %d", keys, r.authorizations(t, "cs-5xx"))
	}
	r.fake.SetDown(false)
	retry := r.post(t, "/payment-authorizations", "key-5xx", authorizeBody("cs-5xx"))
	if retry.status != 201 || retry.replayed != "" {
		t.Fatalf("the retry after a 5xx must run again: %+v", retry)
	}
	// The key row, the aggregate and its outbox rows committed together.
	if r.rows(t, `SELECT count(*) FROM idempotency_keys WHERE key = 'key-5xx'`) != 1 || r.authorizations(t, "cs-5xx") != 1 ||
		r.rows(t, `SELECT count(*) FROM outbox_events`) < 1 {
		t.Fatal("the key row, the aggregate and the outbox rows must commit together")
	}
}
