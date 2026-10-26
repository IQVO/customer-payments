package mcp_test

import (
	"strings"
	"testing"
)

func TestGetPaymentAuthorization(t *testing.T) {
	h := newHarness(t)
	h.seedAuthorization(t)
	got := h.ok(t, "get_payment_authorization", map[string]any{"authorizationId": "pay-1"})
	if got["state"] != "PARTIALLY_CAPTURED" || got["orderId"] != "ord-1" || got["totalMinor"] != float64(10000) {
		t.Fatalf("authorization = %v", got)
	}
	first := got["allocations"].([]any)[0].(map[string]any)
	if first["state"] != "CAPTURED" || first["trackingNumbers"].([]any)[0] != "TRK1" {
		t.Fatalf("allocation = %v", first)
	}
	if strings.Contains(strings.ToLower(text(call(t, h.session, "get_payment_authorization", map[string]any{"authorizationId": "pay-1"}))), "tok_visa") {
		t.Fatal("the PSP token must never leave the service")
	}
}

func TestGetPaymentAuthorizationMisses(t *testing.T) {
	h := newHarness(t)
	failWith(t, h.session, "get_payment_authorization", map[string]any{"authorizationId": "nope"}, "not-found")
	failWith(t, h.session, "get_payment_authorization", map[string]any{"authorizationId": strings.Repeat("a", 129)}, "not-found")
	failWith(t, h.session, "get_refund", map[string]any{"refundId": ""}, "not-found")
	failWith(t, h.session, "get_refund", map[string]any{"refundId": "nope"}, "not-found")
}

func TestListPaymentAuthorizations(t *testing.T) {
	h := newHarness(t)
	h.seedAuthorization(t)
	for _, args := range []map[string]any{{"orderId": "ord-1"}, {"checkoutSessionId": "cs-1"}} {
		got := h.ok(t, "list_payment_authorizations", args)
		if list := got["authorizations"].([]any); len(list) != 1 {
			t.Fatalf("%v: %v", args, got)
		}
	}
	if got := h.ok(t, "list_payment_authorizations", map[string]any{"orderId": "ord-9"}); len(got["authorizations"].([]any)) != 0 {
		t.Fatalf("unknown order = %v", got)
	}
	failWith(t, h.session, "list_payment_authorizations", map[string]any{}, "validation-failed")
}

func TestRefundTools(t *testing.T) {
	h := newHarness(t)
	h.seedAuthorization(t)
	h.seedRefund(t)
	got := h.ok(t, "get_refund", map[string]any{"refundId": "ref-1"})
	if got["state"] != "FAILED" || got["failureCode"] != "ACCOUNT_CLOSED" || got["groupNo"] != float64(1) || got["attempts"] != float64(1) {
		t.Fatalf("refund = %v", got)
	}
	list := h.ok(t, "list_refunds", map[string]any{"orderId": "ord-1"})
	if len(list["refunds"].([]any)) != 1 {
		t.Fatalf("refunds = %v", list)
	}
	failWith(t, h.session, "list_refunds", map[string]any{"orderId": ""}, "validation-failed")
}
