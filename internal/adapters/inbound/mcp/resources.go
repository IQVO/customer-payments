package mcp

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// mimeTypeJSON is the MIME type of every resource this server serves.
const mimeTypeJSON = "application/json"

// orderPaymentStatusTemplate is the ONE resource this server offers (Agentic
// Standard v2, AS-3; docs/adr/0007): a scoped read model this context owns —
// where the payment of ONE order stands and how its refunds went — built from
// the same ListAuthorizations and ListRefunds use cases the tools and REST
// use. Bounded by design: one order, the same per-order caps as the tools
// (at most 1000 rows each), never a dump of a table.
const orderPaymentStatusTemplate = "warehouse://customer-payments/orders/{orderId}/payment-status"

// orderPaymentStatus is the resource body: the authorization and refund views
// of one order, in the same camelCase shape the tools return.
type orderPaymentStatus struct {
	OrderID        string              `json:"orderId"`
	Authorizations []authorizationView `json:"authorizations"`
	Refunds        []refundView        `json:"refunds"`
}

// registerResources adds the scoped read-model resource. Per the read-only
// charter a resource only reads; the concrete orderId is read from the
// request URI at call time.
func (d Deps) registerResources(server *mcp.Server) {
	server.AddResourceTemplate(&mcp.ResourceTemplate{
		URITemplate: orderPaymentStatusTemplate,
		Name:        "order payment status",
		Description: "Every payment authorization and every refund of one order: states, totals in minor units, allocations per shipment group with capture state and tracking numbers, expiry, decline reasons, refund reasons, attempts and failure codes. Read-only; scoped to the one order named in the URI.",
		MIMEType:    mimeTypeJSON,
	}, func(ctx context.Context, req *mcp.ReadResourceRequest) (*mcp.ReadResourceResult, error) {
		orderID, err := orderIDFromURI(req.Params.URI)
		if err != nil {
			return nil, err
		}
		authorizations, err := d.ListAuthorizations.All(ctx, orderID, "")
		if err != nil {
			return nil, mapError(err)
		}
		refunds, err := d.ListRefunds.All(ctx, orderID)
		if err != nil {
			return nil, mapError(err)
		}
		body := orderPaymentStatus{OrderID: orderID, Authorizations: []authorizationView{}, Refunds: []refundView{}}
		for _, v := range authorizations {
			body.Authorizations = append(body.Authorizations, toAuthorization(v))
		}
		for _, v := range refunds {
			body.Refunds = append(body.Refunds, toRefund(v))
		}
		raw, err := json.Marshal(body)
		if err != nil {
			return nil, fmt.Errorf("marshal order payment status: %w", err)
		}
		return &mcp.ReadResourceResult{
			Contents: []*mcp.ResourceContents{{
				URI:      req.Params.URI,
				MIMEType: mimeTypeJSON,
				Text:     string(raw),
			}},
		}, nil
	})
}

// orderIDFromURI extracts the orderId of a
// warehouse://customer-payments/orders/{orderId}/payment-status URI. The
// segment is model-supplied and therefore untrusted: a malformed URI, an
// empty segment or one over the id guard is rejected (the same 1..128 guard
// as the tools), never silently defaulted.
func orderIDFromURI(uri string) (string, error) {
	const prefix = "warehouse://customer-payments/orders/"
	rest, ok := strings.CutPrefix(uri, prefix)
	if !ok {
		return "", fmt.Errorf("resource uri %q must start with %q", uri, prefix)
	}
	parts := strings.Split(rest, "/")
	if len(parts) != 2 || parts[1] != "payment-status" {
		return "", fmt.Errorf("resource uri %q must be %s", uri, orderPaymentStatusTemplate)
	}
	return pathID(parts[0])
}
