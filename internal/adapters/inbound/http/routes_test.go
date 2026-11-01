package http_test

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"testing"

	"github.com/IQVO/customer-payments/internal/application/provider"
)

var errUnavailable = provider.ErrUnavailable

func TestAuthorizeCreatesThenReturnsTheExisting(t *testing.T) {
	e := newEnv(t)
	first := e.post("/payment-authorizations", authorizeBody)
	e.wantStatus(first, http.StatusCreated)
	m := first.json(t)
	if m["state"] != "AUTHORIZED" || m["totalMinor"] != float64(10000) || m["orderId"] != nil || m["expiresAt"] == nil {
		t.Fatalf("body = %s", first.Body)
	}
	if len(m["allocations"].([]any)) != 2 {
		t.Fatalf("allocations = %v", m["allocations"])
	}
	again := e.post("/payment-authorizations", authorizeBody)
	e.wantStatus(again, http.StatusOK)
	if again.json(t)["authorizationId"] != m["authorizationId"] {
		t.Fatalf("a replay with a new key must return the existing authorization: %s", again.Body)
	}
}

func TestAuthorizeDeclinedIsRecorded(t *testing.T) {
	e := newEnv(t)
	e.provider.Fail("authorize", declined("CARD_DECLINED"))
	r := e.post("/payment-authorizations", authorizeBody)
	e.wantStatus(r, http.StatusCreated)
	if m := r.json(t); m["state"] != "DECLINED" || m["declineReason"] != "CARD_DECLINED" || m["providerAuthorizationId"] != nil {
		t.Fatalf("body = %s", r.Body)
	}
}

func declined(code string) error { return provider.Declined(code) }

func TestAuthorizeValidation(t *testing.T) {
	e := newEnv(t)
	bad := strings.Replace(authorizeBody, `"currency":"BRL"`, `"currency":"brl"`, 1)
	e.wantProblem(e.post("/payment-authorizations", bad), http.StatusUnprocessableEntity, "validation-failed")
	neg := strings.Replace(authorizeBody, `"amountMinor":7000`, `"amountMinor":-7000`, 1)
	e.wantProblem(e.post("/payment-authorizations", neg), http.StatusUnprocessableEntity, "negative-amount")
}

func TestGetAndListAuthorizations(t *testing.T) {
	e := newEnv(t)
	id := e.shipped()
	r := e.get("/payment-authorizations/" + id)
	e.wantStatus(r, http.StatusOK)
	m := r.json(t)
	first := m["allocations"].([]any)[0].(map[string]any)
	if m["state"] != "CAPTURED" || m["orderId"] != "ord-1" || first["trackingNumber"] != "TRK1" || first["state"] != "CAPTURED" {
		t.Fatalf("body = %s", r.Body)
	}
	e.wantProblem(e.get("/payment-authorizations/nope"), http.StatusNotFound, "not-found")
	for _, q := range []string{"?orderId=ord-1", "?checkoutSessionId=cs-1"} {
		l := e.get("/payment-authorizations" + q)
		e.wantStatus(l, http.StatusOK)
		if got := l.json(t)["items"].([]any); len(got) != 1 {
			t.Fatalf("%s: %s", q, l.Body)
		}
	}
	e.wantProblem(e.get("/payment-authorizations"), http.StatusUnprocessableEntity, "validation-failed")
	empty := e.get("/payment-authorizations?orderId=ord-9")
	e.wantStatus(empty, http.StatusOK)
	if !strings.Contains(empty.Body, `"items":[]`) {
		t.Fatalf("empty list = %s", empty.Body)
	}
}

func TestVoidRoute(t *testing.T) {
	e := newEnv(t)
	id := e.authorized()
	r := e.post("/payment-authorizations/"+id+"/voids", "")
	e.wantStatus(r, http.StatusOK)
	if r.json(t)["state"] != "VOIDED" {
		t.Fatalf("body = %s", r.Body)
	}
	e.wantProblem(e.post("/payment-authorizations/"+id+"/voids", ""), http.StatusConflict, "illegal-transition")
	e.wantProblem(e.post("/payment-authorizations/nope/voids", ""), http.StatusNotFound, "not-found")
}

func TestVoidProviderOutageIs503(t *testing.T) {
	e := newEnv(t)
	id := e.authorized()
	e.provider.Fail("void", errors.New("x"))
	e.wantStatus(e.post("/payment-authorizations/"+id+"/voids", ""), http.StatusInternalServerError)
	e.provider.Fail("void", errUnavailable)
	e.wantProblem(e.post("/payment-authorizations/"+id+"/voids", ""), http.StatusServiceUnavailable, "payment-provider-unavailable")
}

func TestRefundRoutes(t *testing.T) {
	e := newEnv(t)
	e.shipped()
	body := `{"orderId":"ord-1","amountMinor":2500,"reason":"MANUAL"}`
	r := e.post("/refunds", body)
	e.wantStatus(r, http.StatusCreated)
	m := r.json(t)
	if m["state"] != "SUCCEEDED" || m["authorizationId"] != "pay-1" || m["reason"] != "MANUAL" {
		t.Fatalf("body = %s", r.Body)
	}
	g := e.get("/refunds/" + m["refundId"].(string))
	e.wantStatus(g, http.StatusOK)
	l := e.get("/refunds?orderId=ord-1")
	e.wantStatus(l, http.StatusOK)
	if got := l.json(t)["items"].([]any); len(got) != 1 {
		t.Fatalf("list = %s", l.Body)
	}
	e.wantProblem(e.get("/refunds"), http.StatusUnprocessableEntity, "validation-failed")
	e.wantProblem(e.get("/refunds/nope"), http.StatusNotFound, "not-found")
}

func TestRefundErrors(t *testing.T) {
	e := newEnv(t)
	e.wantProblem(e.post("/refunds", `{"orderId":"ord-1","amountMinor":5,"reason":"MANUAL"}`), http.StatusNotFound, "not-found")
	e.shipped()
	e.wantProblem(e.post("/refunds", `{"orderId":"ord-1","amountMinor":10001,"reason":"MANUAL"}`), http.StatusConflict, "over-refund")
	e.wantProblem(e.post("/refunds", `{"orderId":"ord-1","amountMinor":5,"reason":"WHIM"}`), http.StatusUnprocessableEntity, "validation-failed")
}

func TestRefundRetryRoute(t *testing.T) {
	e := newEnv(t)
	e.shipped()
	e.provider.Fail("refund", declined("ACCOUNT_CLOSED"))
	r := e.post("/refunds", `{"orderId":"ord-1","groupNo":1,"amountMinor":500,"reason":"LOST"}`)
	e.wantStatus(r, http.StatusCreated)
	id := r.json(t)["refundId"].(string)
	if m := r.json(t); m["state"] != "FAILED" || m["failureCode"] != "ACCOUNT_CLOSED" || m["groupNo"] != float64(1) {
		t.Fatalf("body = %s", r.Body)
	}
	e.provider.Fail("refund", nil)
	ok := e.post("/refunds/"+id+":retry", "")
	e.wantStatus(ok, http.StatusOK)
	if ok.json(t)["state"] != "SUCCEEDED" {
		t.Fatalf("body = %s", ok.Body)
	}
	e.wantProblem(e.post("/refunds/"+id+":retry", ""), http.StatusConflict, "illegal-transition")
	e.wantProblem(e.post("/refunds/nope:retry", ""), http.StatusNotFound, "not-found")
}

func TestMetricsExposeTheParkedFactsGauge(t *testing.T) {
	e := newEnv(t)
	r := e.get("/metrics")
	e.wantStatus(r, http.StatusOK)
	if r.Body != "" {
		t.Fatalf("no gauge configured, body = %q", r.Body)
	}
	e.gauge = func(context.Context) (int, error) { return 3, nil }
	e.reroute()
	if r = e.get("/metrics"); !strings.Contains(r.Body, "customer_payments_parked_facts 3\n") {
		t.Fatalf("body = %q", r.Body)
	}
	e.gauge = func(context.Context) (int, error) { return 0, errors.New("db down") }
	e.reroute()
	e.wantStatus(e.get("/metrics"), http.StatusInternalServerError)
}
