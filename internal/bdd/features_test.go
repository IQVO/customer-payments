package bdd

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/cucumber/godog"

	httpadapter "github.com/IQVO/customer-payments/internal/adapters/inbound/http"
	"github.com/IQVO/customer-payments/internal/adapters/outbound/memory"
	"github.com/IQVO/customer-payments/internal/application/usecases"
)

var t0 = time.Date(2026, 10, 24, 9, 0, 0, 0, time.UTC)

// TestFeatures runs the Gherkin scenarios of ../../features against the real
// chi router over the use cases wired to the in-memory doubles.
func TestFeatures(t *testing.T) {
	suite := godog.TestSuite{
		ScenarioInitializer: initializeScenario,
		Options:             &godog.Options{Format: "pretty", Paths: []string{"../../features"}, TestingT: t, Strict: true},
	}
	if suite.Run() != 0 {
		t.Fatal("non-zero status returned, failed to run feature tests")
	}
}

// world is the state of one scenario.
type world struct {
	deps     usecases.Deps
	provider *memory.Provider
	srv      *httptest.Server
	seq      int
	keys     int

	firstAuthID string
	lastOrder   string
	authID      string
	status      int
	body        string
}

func newWorld() *world {
	clock := memory.NewClock(t0)
	s := memory.NewStore(clock)
	prov := memory.NewProvider(clock)
	deps := usecases.Deps{
		Authorizations: s.Authorizations(), Refunds: s.Refunds(), Parked: s.Parked(), Inbox: s.Inbox(), Outbox: s.Outbox(),
		UoW: s.UnitOfWork(), Clock: clock, IDs: &memory.SequentialIDs{}, Provider: prov,
	}
	api := &httpadapter.Server{
		AuthorizePayment: usecases.AuthorizePayment{Deps: deps}, VoidAuthorization: usecases.VoidAuthorization{Deps: deps},
		GetAuthorization: usecases.GetAuthorization{Deps: deps}, ListAuthorizations: usecases.ListAuthorizations{Deps: deps},
		RequestRefund: usecases.RequestRefund{Deps: deps}, RetryRefund: usecases.RetryRefund{Deps: deps},
		GetRefund: usecases.GetRefund{Deps: deps}, ListRefunds: usecases.ListRefunds{Deps: deps},
		IdempotencyStore: s.Idempotency(), Clock: clock, Readiness: httpadapter.NewReadiness(0, 0),
	}
	return &world{deps: deps, provider: prov, srv: httptest.NewServer(httpadapter.NewRouter(api))}
}

func (w *world) consumer() usecases.Consumer { return usecases.Consumer{Deps: w.deps} }

func (w *world) eventID() string {
	w.seq++
	return "ev-" + strconv.Itoa(w.seq)
}

// do sends one request and records the status and body; a body sends a fresh
// Idempotency-Key on POST.
func (w *world) do(method, path, body string) error {
	req, err := http.NewRequest(method, w.srv.URL+path, strings.NewReader(body))
	if err != nil {
		return err
	}
	if method == http.MethodPost {
		w.keys++
		req.Header.Set("Idempotency-Key", "key-"+strconv.Itoa(w.keys))
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()
	b, _ := io.ReadAll(resp.Body)
	w.status, w.body = resp.StatusCode, string(b)
	return nil
}

func (w *world) json() map[string]any {
	var m map[string]any
	_ = json.Unmarshal([]byte(w.body), &m)
	return m
}
