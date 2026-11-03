package mcp

import (
	"context"
	"fmt"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// investigateOrderPaymentSOP is the operational standard-operating-procedure
// the investigate_order_payment prompt hands to the model. Per the charter it
// standardises how to interpret what the read tools return, when to escalate
// to a human and what "done" means; it names the tools but never calls them,
// and it contains no write of any kind (this surface is read-only: a refund or
// a void is a REST operation a human performs, never a tool call here).
const investigateOrderPaymentSOP = `You are investigating where the payment of an order stands for a store operator. Use only the read tools of customer-payments; never assume state, and never claim money moved without reading it here.

Procedure:
1. Call list_payment_authorizations(orderId) (or get_payment_authorization(authorizationId) when you already have one). The list includes declined attempts of the checkout session. If you only have a checkout session id, call list_payment_authorizations(checkoutSessionId).
2. Read the state of each authorization: AUTHORIZED means the hold stands; PARTIALLY_CAPTURED/CAPTURED mean money was taken for the captured allocation groups (each allocation carries groupNo, amountMinor, its own PENDING/CAPTURED/RELEASED state and tracking numbers); RELEASED/VOIDED/EXPIRED mean the hold is gone without that money; DECLINED carries the declineReason.
3. If the order shows a capture, call list_refunds(orderId) (or get_refund(refundId)) to see whether any money came back: state REQUESTED/SUCCEEDED/FAILED, reason LOST/RETURNED_TO_SENDER/MANUAL, attempts and the last failureCode.

Interpretation:
- A customer says "I was charged but the package never came": look for a CAPTURED allocation whose group has a refund with state FAILED, or no refund at all. A FAILED refund with a failureCode (e.g. ACCOUNT_CLOSED) is a payment-provider refusal, not a lost request.
- DECLINED with no later AUTHORIZED attempt means the order was never paid: there is nothing to refund.
- Amounts are in minor units (cents) of the authorization's currency; a refund never exceeds the captured amount of its group.

Escalate to a human when: a refund is FAILED (retrying or issuing a manual refund is a REST/console decision an operator owns), an authorization is stuck REQUESTED-side across provider attempts, or the numbers a customer reports do not match what this context reads. This context makes the payment legible; it never moves money.

Done means: you have reported, for the order you were given, every authorization with its state and captured amount, every refund with its state and failure code, and one line saying what the operator should decide next (nothing to do, or the specific escalation).`

// registerPrompts adds the ONE workflow prompt (the operator SOP of this
// context). The argument is typed and optional; the prompt may name tools but
// never calls them and hides no write.
func (d Deps) registerPrompts(server *mcp.Server) {
	server.AddPrompt(&mcp.Prompt{
		Name:        "investigate_order_payment",
		Description: "Standard operating procedure for investigating where the payment of an order stands (authorizations, captures, declines) and how its refunds went, with the escalation rules an operator owns.",
		Arguments: []*mcp.PromptArgument{{
			Name:        "orderId",
			Description: "optional: the order under investigation, e.g. ord-3f9c1a2b4d5e; the procedure works for any order when omitted",
			Required:    false,
		}},
	}, func(ctx context.Context, req *mcp.GetPromptRequest) (*mcp.GetPromptResult, error) {
		sop := investigateOrderPaymentSOP
		if orderID := req.Params.Arguments["orderId"]; orderID != "" {
			sop = fmt.Sprintf("Order under investigation: %s\n\n%s", orderID, sop)
		}
		return &mcp.GetPromptResult{
			Description: "How to investigate an order's payment: read the authorizations and refunds, interpret the states, and know what escalates to a human.",
			Messages: []*mcp.PromptMessage{{
				Role:    "user",
				Content: &mcp.TextContent{Text: sop},
			}},
		}, nil
	})
}
