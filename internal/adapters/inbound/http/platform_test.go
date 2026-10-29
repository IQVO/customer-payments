package http_test

import (
	"net/http"
	"strings"
	"testing"
)

func TestIdempotencyKeyRequiredAndReplay(t *testing.T) {
	e := newEnv(t)
	e.wantProblem(e.do(http.MethodPost, "/payment-authorizations", "", authorizeBody), http.StatusBadRequest, "idempotency-key-required")
	e.wantProblem(e.do(http.MethodPost, "/payment-authorizations", strings.Repeat("k", 129), authorizeBody), http.StatusBadRequest, "idempotency-key-required")

	first := e.do(http.MethodPost, "/payment-authorizations", "k1", authorizeBody)
	e.wantStatus(first, http.StatusCreated)
	again := e.do(http.MethodPost, "/payment-authorizations", "k1", authorizeBody)
	e.wantStatus(again, http.StatusCreated)
	if again.Body != first.Body || again.Header.Get("Idempotent-Replayed") != "true" {
		t.Fatalf("replay differs: %q vs %q", again.Body, first.Body)
	}
	other := strings.Replace(authorizeBody, "tok_visa", "tok_other", 1)
	e.wantProblem(e.do(http.MethodPost, "/payment-authorizations", "k1", other), http.StatusUnprocessableEntity, "idempotency-key-reused")
	if n := len(e.provider.Calls()); n != 1 {
		t.Fatalf("provider calls = %d, want 1", n)
	}
}

func TestIdempotencyRecordsClientErrorsNotServerErrors(t *testing.T) {
	e := newEnv(t)
	bad := strings.Replace(authorizeBody, `"totalMinor":10000`, `"totalMinor":9999`, 1)
	e.wantProblem(e.do(http.MethodPost, "/payment-authorizations", "k2", bad), http.StatusUnprocessableEntity, "allocation-mismatch")
	e.wantProblem(e.do(http.MethodPost, "/payment-authorizations", "k2", bad), http.StatusUnprocessableEntity, "allocation-mismatch")
	e.provider.Fail("authorize", errUnavailable)
	e.wantProblem(e.do(http.MethodPost, "/payment-authorizations", "k3", authorizeBody), http.StatusServiceUnavailable, "payment-provider-unavailable")
	e.provider.Fail("authorize", nil)
	e.wantStatus(e.do(http.MethodPost, "/payment-authorizations", "k3", authorizeBody), http.StatusCreated)
}

func TestOversizedBodyIsMalformed(t *testing.T) {
	e := newEnv(t)
	big := `{"checkoutSessionId":"` + strings.Repeat("a", 70<<10) + `"}`
	e.wantProblem(e.post("/payment-authorizations", big), http.StatusBadRequest, "malformed-request")
}

func TestMalformedBodies(t *testing.T) {
	e := newEnv(t)
	for _, body := range []string{``, `{`, `{"unknown":1}`, authorizeBody + `{}`} {
		e.wantProblem(e.post("/payment-authorizations", body), http.StatusBadRequest, "malformed-request")
	}
	e.wantProblem(e.post("/refunds", `{"orderId":1}`), http.StatusBadRequest, "malformed-request")
}

func TestRejectNUL(t *testing.T) {
	e := newEnv(t)
	e.wantProblem(e.get("/payment-authorizations/a%00b"), http.StatusNotFound, "resource-not-found")
	e.wantProblem(e.get("/payment-authorizations?orderId=a%00b"), http.StatusBadRequest, "malformed-request")
	e.wantProblem(e.post("/payment-authorizations", `{"checkoutSessionId":"a\u0000b"}`), http.StatusBadRequest, "malformed-request")
	e.wantProblem(e.post("/payment-authorizations", "{\"checkoutSessionId\":\"a\x00b\"}"), http.StatusBadRequest, "malformed-request")
}

func TestHealthAndReadiness(t *testing.T) {
	e := newEnv(t)
	e.wantStatus(e.get("/healthz"), http.StatusOK)
	e.wantStatus(e.get("/readyz"), http.StatusOK)
	e.ready.SetNotReady()
	e.wantStatus(e.get("/readyz"), http.StatusServiceUnavailable)
	e.wantStatus(e.get("/healthz"), http.StatusOK)
}

func TestOverlongPathIDIsNotFound(t *testing.T) {
	e := newEnv(t)
	long := strings.Repeat("a", 129)
	e.wantProblem(e.get("/payment-authorizations/"+long), http.StatusNotFound, "not-found")
	e.wantProblem(e.get("/refunds/"+long), http.StatusNotFound, "not-found")
	e.wantProblem(e.post("/payment-authorizations/"+long+":void", ""), http.StatusNotFound, "not-found")
	e.wantProblem(e.post("/refunds/"+long+":retry", ""), http.StatusNotFound, "not-found")
}
