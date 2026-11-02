package http_test

import (
	"net/http"
	"strings"
	"testing"
)

func TestIdempotencyKeyRequiredInvalidAndReplay(t *testing.T) {
	e := newEnv(t)
	e.wantProblem(e.do(http.MethodPost, "/payment-authorizations", "", authorizeBody), http.StatusBadRequest, "idempotency-key-required")
	for _, bad := range []string{strings.Repeat("k", 129), "caf\u00e9", "a\tb"} {
		e.wantProblem(e.do(http.MethodPost, "/payment-authorizations", bad, authorizeBody), http.StatusBadRequest, "idempotency-key-invalid")
	}
	e.wantStatus(e.do(http.MethodPost, "/payment-authorizations", strings.Repeat("k", 128), strings.Replace(authorizeBody, "cs-1", "cs-long", 1)), http.StatusCreated)

	first := e.do(http.MethodPost, "/payment-authorizations", "k1", authorizeBody)
	e.wantStatus(first, http.StatusCreated)
	if first.Header.Get("Idempotency-Replayed") != "" {
		t.Fatal("the first execution must not carry Idempotency-Replayed")
	}
	again := e.do(http.MethodPost, "/payment-authorizations", "k1", authorizeBody)
	e.wantStatus(again, http.StatusCreated)
	if again.Body != first.Body || again.Header.Get("Idempotency-Replayed") != "true" || again.Header.Get("Idempotent-Replayed") != "" {
		t.Fatalf("replay differs: %q vs %q (%v)", again.Body, first.Body, again.Header)
	}
	other := strings.Replace(authorizeBody, "tok_visa", "tok_other", 1)
	e.wantProblem(e.do(http.MethodPost, "/payment-authorizations", "k1", other), http.StatusConflict, "idempotency-key-reused")
	if n := len(e.provider.Calls()); n != 2 {
		t.Fatalf("provider calls = %d, want 2 (one per distinct key)", n)
	}
}

// The key is global (STANDARD 2.3): the same key on another route is a different fingerprint.
func TestIdempotencyKeyIsGlobalAcrossRoutes(t *testing.T) {
	e := newEnv(t)
	id := e.authorized()
	e.wantStatus(e.do(http.MethodPost, "/payment-authorizations/"+id+"/voids", "shared", ""), http.StatusOK)
	e.wantProblem(e.do(http.MethodPost, "/refunds", "shared", `{"orderId":"ord-1","amountMinor":5,"reason":"MANUAL"}`), http.StatusConflict, "idempotency-key-reused")
	other := e.authorizedSession("cs-2")
	e.wantProblem(e.do(http.MethodPost, "/payment-authorizations/"+other+"/voids", "shared", ""), http.StatusConflict, "idempotency-key-reused")
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
	e.wantProblem(e.post("/payment-authorizations/"+long+"/voids", ""), http.StatusNotFound, "not-found")
	e.wantProblem(e.post("/refunds/"+long+":retry", ""), http.StatusNotFound, "not-found")
}
