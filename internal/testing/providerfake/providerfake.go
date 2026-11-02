// Package providerfake is a local httptest fake of the payment provider API
// (the contract recorded in docs/adr/0002-customer-payments-assumptions.md,
// A12). Integration and wiring tests run against it because the
// payment-provider-simulator is a separate deliverable. Like the real thing it
// is idempotent on the Idempotency-Key header and never sees a card number.
package providerfake

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

// DeclineToken is the PSP token the fake always declines.
const DeclineToken = "tok_declined"

// Fake is the running fake.
type Fake struct {
	Server *httptest.Server
	// Validity is how long an approved hold lasts (default seven days).
	Validity time.Duration

	mu       sync.Mutex
	seq      int
	answers  map[string]answer
	down     bool
	declines map[string]string
	calls    []string
}

type answer struct {
	status int
	body   map[string]any
}

// New starts a fake; it stops with the test.
func New(t testing.TB) *Fake {
	t.Helper()
	f := &Fake{Validity: 7 * 24 * time.Hour, answers: map[string]answer{}, declines: map[string]string{}}
	f.Server = httptest.NewServer(http.HandlerFunc(f.serve))
	t.Cleanup(f.Server.Close)
	return f
}

// URL is the base URL for PAYMENT_PROVIDER_URL.
func (f *Fake) URL() string { return f.Server.URL }

// SetDown makes every call answer 503 (a provider outage).
func (f *Fake) SetDown(down bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.down = down
}

// Decline makes the operation (captures, refunds, reauthorize, reduce, void)
// answer 402 with the code; an empty code clears it.
func (f *Fake) Decline(operation, code string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if code == "" {
		delete(f.declines, operation)
		return
	}
	f.declines[operation] = code
}

// Calls returns "<operation> <path>" for every request, oldest first.
func (f *Fake) Calls() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.calls...)
}

// Effects counts the distinct idempotency keys that took effect for the
// operation (authorize, captures, refunds, voids, reauthorizations, reductions).
func (f *Fake) Effects(operation string) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	n := 0
	for k := range f.answers {
		if strings.HasPrefix(k, operation+"|") {
			n++
		}
	}
	return n
}

func operationOf(path string) string {
	if path == "/authorizations" {
		return "authorize"
	}
	parts := strings.Split(strings.Trim(path, "/"), "/")
	if len(parts) == 3 {
		return parts[2]
	}
	return "unknown"
}

func (f *Fake) serve(w http.ResponseWriter, r *http.Request) {
	op := operationOf(r.URL.Path)
	var in struct {
		AmountMinor        int64  `json:"amountMinor"`
		PaymentMethodToken string `json:"paymentMethodToken"`
	}
	_ = json.NewDecoder(r.Body).Decode(&in)
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, op+" "+r.URL.Path)
	reply := f.decide(r, op, in.PaymentMethodToken)
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(reply.status)
	_ = json.NewEncoder(w).Encode(reply.body)
}

func (f *Fake) decide(r *http.Request, op, token string) answer {
	if f.down {
		return answer{http.StatusServiceUnavailable, map[string]any{"error": "down"}}
	}
	key := op + "|" + r.Header.Get("Idempotency-Key")
	if a, ok := f.answers[key]; ok {
		return a
	}
	var a answer
	switch {
	case r.Method != http.MethodPost || r.Header.Get("Idempotency-Key") == "":
		return answer{http.StatusBadRequest, map[string]any{"error": "POST with an Idempotency-Key is required"}}
	case token == DeclineToken:
		a = answer{http.StatusPaymentRequired, map[string]any{"code": "CARD_DECLINED"}}
	case f.declines[op] != "":
		a = answer{http.StatusPaymentRequired, map[string]any{"code": f.declines[op]}}
	default:
		a = f.approve(op)
	}
	f.answers[key] = a
	return a
}

func (f *Fake) approve(op string) answer {
	f.seq++
	id := fmt.Sprintf("%s-%d", op, f.seq)
	switch op {
	case "authorize", "reauthorizations":
		return answer{http.StatusOK, map[string]any{"id": id, "expiresAt": time.Now().UTC().Add(f.Validity).Format(time.RFC3339Nano)}}
	case "captures", "refunds":
		return answer{http.StatusCreated, map[string]any{"id": id}}
	}
	return answer{http.StatusOK, map[string]any{"id": id}}
}
