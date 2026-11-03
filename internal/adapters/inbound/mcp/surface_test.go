package mcp_test

import (
	"encoding/json"
	"strings"
	"testing"

	sdk "github.com/modelcontextprotocol/go-sdk/mcp"
)

// orderStatusURI builds one concrete resource URI of the template.
func orderStatusURI(orderID string) string {
	return "warehouse://customer-payments/orders/" + orderID + "/payment-status"
}

// The server lists its one resource template (AS-3).
func TestSurface_ResourcesListed(t *testing.T) {
	h := newHarness(t)
	res, err := h.session.ListResourceTemplates(t.Context(), nil)
	if err != nil {
		t.Fatalf("list resource templates: %v", err)
	}
	found := false
	for _, tpl := range res.ResourceTemplates {
		if tpl.URITemplate == "warehouse://customer-payments/orders/{orderId}/payment-status" {
			found = true
			if tpl.MIMEType != "application/json" {
				t.Errorf("template MIMEType = %q, want application/json", tpl.MIMEType)
			}
		}
	}
	if !found {
		t.Fatalf("resource templates = %+v, want the order payment status template", res.ResourceTemplates)
	}
}

// Reading the resource returns the bounded read model of ONE order: its
// authorizations and its refunds, in the same shape the tools return.
func TestSurface_OrderPaymentStatusRead(t *testing.T) {
	h := newHarness(t)
	h.seedAuthorization(t)
	h.seedRefund(t)

	res, err := h.session.ReadResource(t.Context(), &sdk.ReadResourceParams{URI: orderStatusURI("ord-1")})
	if err != nil {
		t.Fatalf("read resource: %v", err)
	}
	if len(res.Contents) != 1 {
		t.Fatalf("contents = %d, want 1", len(res.Contents))
	}
	c := res.Contents[0]
	if c.URI != orderStatusURI("ord-1") {
		t.Errorf("uri = %q, want %q", c.URI, orderStatusURI("ord-1"))
	}
	if c.MIMEType != "application/json" {
		t.Errorf("mimeType = %q, want application/json", c.MIMEType)
	}
	var body struct {
		OrderID        string `json:"orderId"`
		Authorizations []any  `json:"authorizations"`
		Refunds        []any  `json:"refunds"`
	}
	if err := json.Unmarshal([]byte(c.Text), &body); err != nil {
		t.Fatalf("resource body is not JSON: %v (%s)", err, c.Text)
	}
	if body.OrderID != "ord-1" {
		t.Errorf("orderId = %q, want ord-1", body.OrderID)
	}
	if len(body.Authorizations) != 1 || len(body.Refunds) != 1 {
		t.Fatalf("authorizations = %d, refunds = %d, want 1 and 1 (body %s)", len(body.Authorizations), len(body.Refunds), c.Text)
	}
	if !strings.Contains(c.Text, "PARTIALLY_CAPTURED") || !strings.Contains(c.Text, "ACCOUNT_CLOSED") {
		t.Errorf("body does not carry the seeded authorization and refund states: %s", c.Text)
	}
}

// An order with no payment answers an EMPTY read model, not an error — same
// contract as the list tools.
func TestSurface_OrderPaymentStatusEmptyOrder(t *testing.T) {
	h := newHarness(t)
	res, err := h.session.ReadResource(t.Context(), &sdk.ReadResourceParams{URI: orderStatusURI("ord-none")})
	if err != nil {
		t.Fatalf("read resource of an unknown order: %v", err)
	}
	if !strings.Contains(res.Contents[0].Text, `"authorizations":[]`) || !strings.Contains(res.Contents[0].Text, `"refunds":[]`) {
		t.Errorf("unknown order must answer empty lists, got %s", res.Contents[0].Text)
	}
}

// The URI is model-supplied and untrusted: a malformed template, an empty or
// over-long orderId segment is a protocol error, never a silent default.
func TestSurface_ResourceRejectsMalformedURI(t *testing.T) {
	h := newHarness(t)
	for _, uri := range []string{
		"warehouse://customer-payments/orders//payment-status",
		"warehouse://customer-payments/orders/ord-1",
		"warehouse://customer-payments/orders/ord-1/payment-status/extra",
		"payments://orders/ord-1/payment-status",
		"warehouse://customer-payments/orders/" + strings.Repeat("a", 129) + "/payment-status",
	} {
		if _, err := h.session.ReadResource(t.Context(), &sdk.ReadResourceParams{URI: uri}); err == nil {
			t.Errorf("read resource %q: expected an error", uri)
		}
	}
}

// The server lists its one prompt with its typed argument (AS-3).
func TestSurface_PromptsListed(t *testing.T) {
	h := newHarness(t)
	res, err := h.session.ListPrompts(t.Context(), nil)
	if err != nil {
		t.Fatalf("list prompts: %v", err)
	}
	found := false
	for _, p := range res.Prompts {
		if p.Name != "investigate_order_payment" {
			continue
		}
		found = true
		if len(p.Arguments) != 1 || p.Arguments[0].Name != "orderId" || p.Arguments[0].Required {
			t.Errorf("prompt arguments = %+v, want one optional orderId", p.Arguments)
		}
	}
	if !found {
		t.Fatalf("prompts = %+v, want investigate_order_payment", res.Prompts)
	}
}

// Getting the prompt returns the operator SOP; the optional orderId argument
// is echoed into the procedure header. The prompt names tools, never calls
// them, and contains no write.
func TestSurface_PromptGet(t *testing.T) {
	h := newHarness(t)
	res, err := h.session.GetPrompt(t.Context(), &sdk.GetPromptParams{Name: "investigate_order_payment"})
	if err != nil {
		t.Fatalf("get prompt: %v", err)
	}
	if len(res.Messages) != 1 {
		t.Fatalf("messages = %d, want 1", len(res.Messages))
	}
	text := res.Messages[0].Content.(*sdk.TextContent).Text
	for _, needle := range []string{"list_payment_authorizations", "list_refunds", "Escalate to a human", "Done means"} {
		if !strings.Contains(text, needle) {
			t.Errorf("SOP does not mention %q", needle)
		}
	}

	res, err = h.session.GetPrompt(t.Context(), &sdk.GetPromptParams{
		Name:      "investigate_order_payment",
		Arguments: map[string]string{"orderId": "ord-1"},
	})
	if err != nil {
		t.Fatalf("get prompt with argument: %v", err)
	}
	text = res.Messages[0].Content.(*sdk.TextContent).Text
	if !strings.HasPrefix(text, "Order under investigation: ord-1") {
		t.Errorf("SOP with orderId = %q..., want the order header", text[:min(60, len(text))])
	}
}

// An unknown prompt is a protocol error.
func TestSurface_PromptGetUnknown(t *testing.T) {
	h := newHarness(t)
	if _, err := h.session.GetPrompt(t.Context(), &sdk.GetPromptParams{Name: "no_such_prompt"}); err == nil {
		t.Fatal("get unknown prompt: expected an error")
	}
}
