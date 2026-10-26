package mcp

import (
	"context"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/IQVO/customer-payments/internal/application/ports"
	"github.com/IQVO/customer-payments/internal/application/usecases"
	"github.com/IQVO/customer-payments/internal/application/views"
)

// Deps is everything the MCP tools need, injected by the composition root. It
// carries the SAME read use cases the REST adapter uses (GET
// /payment-authorizations, /payment-authorizations/{id}, /refunds,
// /refunds/{id}); it holds no write use case at all: POST /payment-authorizations,
// :void, POST /refunds and :retry stay REST-only.
type Deps struct {
	GetAuthorization   *usecases.GetAuthorization
	ListAuthorizations *usecases.ListAuthorizations
	GetRefund          *usecases.GetRefund
	ListRefunds        *usecases.ListRefunds
}

// NewDeps wires the four read use cases over the two read repositories they
// need. Handing the repositories over this way keeps cmd/mcp from ever naming
// the shared usecases.Deps bundle, which also carries the write seams (a unit
// of work, the outbox, the payment provider) no MCP binary may reach.
func NewDeps(auths ports.AuthorizationRepository, refunds ports.RefundRepository) Deps {
	deps := usecases.Deps{Authorizations: auths, Refunds: refunds}
	return Deps{
		GetAuthorization:   &usecases.GetAuthorization{Deps: deps},
		ListAuthorizations: &usecases.ListAuthorizations{Deps: deps},
		GetRefund:          &usecases.GetRefund{Deps: deps},
		ListRefunds:        &usecases.ListRefunds{Deps: deps},
	}
}

// maxIDLength mirrors the REST adapter's path-id guard: an id that cannot name
// a resource is a miss, never a server error.
const maxIDLength = 128

type authorizationIDInput struct {
	AuthorizationID string `json:"authorizationId" jsonschema:"the authorization id, e.g. pay-3f9c1a2b4d5e"`
}

type refundIDInput struct {
	RefundID string `json:"refundId" jsonschema:"the refund id, e.g. ref-3f9c1a2b4d5e"`
}

type listAuthorizationsInput struct {
	OrderID           string `json:"orderId,omitempty" jsonschema:"optional: the order id; give exactly one of orderId and checkoutSessionId"`
	CheckoutSessionID string `json:"checkoutSessionId,omitempty" jsonschema:"optional: the checkout session id; give exactly one of orderId and checkoutSessionId"`
}

type listRefundsInput struct {
	OrderID string `json:"orderId" jsonschema:"the order whose refunds to list"`
}

// --- outputs (the REST bodies, camelCase like the REST contract) --------------

type allocationView struct {
	GroupNo         int      `json:"groupNo"`
	AmountMinor     int64    `json:"amountMinor"`
	State           string   `json:"state"`
	TrackingNumbers []string `json:"trackingNumbers"`
}

type authorizationView struct {
	AuthorizationID   string           `json:"authorizationId"`
	CheckoutSessionID string           `json:"checkoutSessionId"`
	OrderID           string           `json:"orderId,omitempty"`
	State             string           `json:"state"`
	Currency          string           `json:"currency"`
	TotalMinor        int64            `json:"totalMinor"`
	Allocations       []allocationView `json:"allocations"`
	ExpiresAt         string           `json:"expiresAt,omitempty"`
	DeclineReason     string           `json:"declineReason,omitempty"`
}

type authorizationList struct {
	Authorizations []authorizationView `json:"authorizations"`
}

type refundView struct {
	RefundID        string `json:"refundId"`
	AuthorizationID string `json:"authorizationId"`
	OrderID         string `json:"orderId"`
	GroupNo         int    `json:"groupNo,omitempty"`
	AmountMinor     int64  `json:"amountMinor"`
	Reason          string `json:"reason"`
	State           string `json:"state"`
	Attempts        int    `json:"attempts"`
	FailureCode     string `json:"failureCode,omitempty"`
}

type refundList struct {
	Refunds []refundView `json:"refunds"`
}

func toAuthorization(v views.Authorization) authorizationView {
	out := authorizationView{
		AuthorizationID: v.AuthorizationID, CheckoutSessionID: v.CheckoutSessionID, OrderID: v.OrderID, State: v.State,
		Currency: v.Currency, TotalMinor: v.TotalMinor, DeclineReason: v.DeclineReason, Allocations: []allocationView{},
	}
	if !v.ExpiresAt.IsZero() {
		out.ExpiresAt = v.ExpiresAt.UTC().Format("2006-01-02T15:04:05Z")
	}
	for _, a := range v.Allocations {
		tracking := a.TrackingNumbers
		if tracking == nil {
			tracking = []string{}
		}
		out.Allocations = append(out.Allocations, allocationView{GroupNo: a.GroupNo, AmountMinor: a.AmountMinor, State: a.State, TrackingNumbers: tracking})
	}
	return out
}

func toRefund(v views.Refund) refundView {
	return refundView{
		RefundID: v.RefundID, AuthorizationID: v.AuthorizationID, OrderID: v.OrderID, GroupNo: v.GroupNo, AmountMinor: v.AmountMinor,
		Reason: v.Reason, State: v.State, Attempts: v.Attempts, FailureCode: v.FailureCode,
	}
}

// --- handlers -----------------------------------------------------------------

// pathID applies the REST adapter's id guard: an empty or over-long id can
// never name a resource and reads as not-found.
func pathID(id string) (string, error) {
	if id == "" || len(id) > maxIDLength {
		return "", toolError("not-found", "an id is 1 to 128 characters; this one cannot name a resource")
	}
	return id, nil
}

func (d Deps) getPaymentAuthorization(ctx context.Context, in authorizationIDInput) (authorizationView, error) {
	id, err := pathID(in.AuthorizationID)
	if err != nil {
		return authorizationView{}, err
	}
	v, err := d.GetAuthorization.Execute(ctx, id)
	if err != nil {
		return authorizationView{}, mapError(err)
	}
	return toAuthorization(v), nil
}

func (d Deps) listPaymentAuthorizations(ctx context.Context, in listAuthorizationsInput) (authorizationList, error) {
	list, err := d.ListAuthorizations.Execute(ctx, in.OrderID, in.CheckoutSessionID)
	if err != nil {
		return authorizationList{}, mapError(err)
	}
	out := authorizationList{Authorizations: []authorizationView{}}
	for _, v := range list {
		out.Authorizations = append(out.Authorizations, toAuthorization(v))
	}
	return out, nil
}

func (d Deps) getRefund(ctx context.Context, in refundIDInput) (refundView, error) {
	id, err := pathID(in.RefundID)
	if err != nil {
		return refundView{}, err
	}
	v, err := d.GetRefund.Execute(ctx, id)
	if err != nil {
		return refundView{}, mapError(err)
	}
	return toRefund(v), nil
}

func (d Deps) listRefunds(ctx context.Context, in listRefundsInput) (refundList, error) {
	list, err := d.ListRefunds.Execute(ctx, in.OrderID)
	if err != nil {
		return refundList{}, mapError(err)
	}
	out := refundList{Refunds: []refundView{}}
	for _, v := range list {
		out.Refunds = append(out.Refunds, toRefund(v))
	}
	return out, nil
}

// registerTools adds the four read-only tools. Every tool is annotated
// read-only, idempotent and closed-world; TestToolSurface pins the set and
// fails the build on any write-verb name.
func (d Deps) registerTools(server *mcp.Server) {
	closedWorld := false
	readOnly := &mcp.ToolAnnotations{ReadOnlyHint: true, IdempotentHint: true, OpenWorldHint: &closedWorld}

	addTool(server, &mcp.Tool{
		Name: "get_payment_authorization",
		Description: "Read one payment authorization by id: its state (AUTHORIZED, PARTIALLY_CAPTURED, CAPTURED, RELEASED, VOIDED, " +
			"EXPIRED or DECLINED), the checkout session and order it belongs to, the currency and total in minor units, every " +
			"allocation per shipment group (groupNo, amountMinor, PENDING, CAPTURED or RELEASED, tracking numbers) and when the hold expires. " +
			"Read-only; an unknown id fails with not-found.",
		Annotations: readOnly,
	}, d.getPaymentAuthorization)
	addTool(server, &mcp.Tool{
		Name: "list_payment_authorizations",
		Description: "List the payment authorizations of one order (orderId) or of one checkout session (checkoutSessionId, declined " +
			"attempts included). Give exactly one of the two. Read-only.",
		Annotations: readOnly,
	}, d.listPaymentAuthorizations)
	addTool(server, &mcp.Tool{
		Name: "get_refund",
		Description: "Read one refund by id: its state (REQUESTED, SUCCEEDED or FAILED), reason (LOST, RETURNED_TO_SENDER or MANUAL), " +
			"order, group, amount in minor units, how many provider attempts were made and the last failure code. Read-only; an unknown id fails with not-found.",
		Annotations: readOnly,
	}, d.getRefund)
	addTool(server, &mcp.Tool{
		Name:        "list_refunds",
		Description: "List the refunds of one order, oldest first. Read-only.",
		Annotations: readOnly,
	}, d.listRefunds)
}

// addTool registers one tool. A handler error is returned to the SDK, which
// turns it into an isError tool result (never a transport failure).
func addTool[In, Out any](server *mcp.Server, tool *mcp.Tool, handle func(context.Context, In) (Out, error)) {
	mcp.AddTool(server, tool, func(ctx context.Context, _ *mcp.CallToolRequest, in In) (*mcp.CallToolResult, Out, error) {
		out, err := handle(ctx, in)
		if err != nil {
			var zero Out
			return nil, zero, err
		}
		return nil, out, nil
	})
}
