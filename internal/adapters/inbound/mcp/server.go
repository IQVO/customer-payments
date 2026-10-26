// Package mcp is the inbound Model Context Protocol adapter: it exposes this
// bounded context to the AI ecosystem as a second driving adapter over the
// SAME application-layer read use cases the REST adapter uses. It is built on
// the official MCP Go SDK and served over Streamable HTTP only.
//
// The surface is READ-ONLY (docs/adr/0003-mcp-server-read-only.md): every tool
// reads authorizations or refunds; authorizing, voiding and refunding stay on
// REST and on the event consumers, and governance_test.go fails the build on a
// write-verb tool name. The package depends inward on the application layer
// and the domain only (plus the ports package's ErrNotFound sentinel to keep
// ONE error vocabulary with REST); nothing else may depend on it
// (internal/architecture's TestMCPAdapterDependencyRule). There is no auth of
// any kind (fleet-wide revert 2026-09-11).
package mcp

import (
	"net/http"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// serverName and serverVersion identify this server in the MCP initialize
// handshake.
const (
	serverName    = "customer-payments-mcp"
	serverVersion = "1.0.0"
)

// instructions is what an MCP client is told about this server at initialize.
const instructions = "Customer payments of the digital storefront (read-only): read the payment authorization of a " +
	"checkout session or an order with get_payment_authorization and list_payment_authorizations (state, total, " +
	"allocations per shipment group with their capture state and tracking numbers, expiry), and follow refunds with " +
	"get_refund and list_refunds (state, reason, group, attempts, failure code). Nothing here changes data: authorizing, " +
	"voiding and refunding are REST operations of customer-payments, and captures and releases are driven by the " +
	"events it consumes. It never sees a card number, only PSP tokens, and no tool returns one."

// NewServer builds the MCP server for this bounded context with every tool
// registered.
func NewServer(deps Deps) *mcp.Server {
	server := mcp.NewServer(
		&mcp.Implementation{Name: serverName, Version: serverVersion},
		&mcp.ServerOptions{Instructions: instructions},
	)
	deps.registerTools(server)
	return server
}

// Handler returns the Streamable HTTP handler for the MCP server.
func Handler(server *mcp.Server) http.Handler {
	return mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server { return server }, nil)
}
