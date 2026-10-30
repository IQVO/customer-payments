// Package a2a is the inbound Agent2Agent adapter: it exposes this bounded
// context to the fleet's agent ecosystem as a third driving adapter over the
// SAME application-layer read use cases the REST and MCP adapters use. It is
// built on the official A2A Go SDK (a2aproject/a2a-go) and speaks JSON-RPC 2.0
// over HTTP, with the Agent Card served at the well-known path.
//
// Per ADR 0005, A2A is the DELEGATION channel between bounded contexts: an
// agent client sends a goal-shaped skill request as a structured message part
// and this agent answers from its own state through its own use cases. It is
// not a data-sync channel: facts between contexts still travel exclusively as
// CloudEvents on Kafka, published through the transactional outbox.
//
// This agent is READ-ONLY, exactly like the MCP adapter (its charter reads
// "Nothing here changes data"): both skills read authorizations or refunds.
// Authorizing, voiding and refunding stay on REST and on the event consumers,
// so this package holds no write use case, no unit of work, no outbox and no
// payment provider, and it never contains an outbound A2A client. Like the MCP
// adapter it never exposes a PSP token, a provider authorization id or any
// payment instrument.
//
// The package depends inward on the application layer (use cases, the ports
// package's ErrNotFound sentinel) and the domain only, never on an outbound
// adapter. The composition root (cmd/a2a) wires the concrete repositories.
// Domain structs never leak across the message boundary.
package a2a

import (
	"context"
	"iter"
	"net/http"

	"github.com/a2aproject/a2a-go/v2/a2a"
	"github.com/a2aproject/a2a-go/v2/a2asrv"

	"github.com/IQVO/customer-payments/internal/application/ports"
	"github.com/IQVO/customer-payments/internal/application/usecases"
)

// Config carries the deployment-specific identity of this agent: the absolute
// URL clients use to reach its JSON-RPC endpoint (advertised on the Agent
// Card) and the card version. It is supplied by the composition root from
// A2A_AGENT_URL; the card MUST advertise a URL the fleet's agent clients can
// actually dial (in-cluster: http://<release>-a2a.<ns>.svc.cluster.local:8093/).
type Config struct {
	AgentURL string
	Version  string
}

// Deps is everything the A2A skills need, injected by the composition root.
// It carries the SAME four read use cases the REST and MCP adapters use and
// holds no write use case at all.
type Deps struct {
	GetAuthorization   *usecases.GetAuthorization
	ListAuthorizations *usecases.ListAuthorizations
	GetRefund          *usecases.GetRefund
	ListRefunds        *usecases.ListRefunds
}

// NewDeps wires the four read use cases over the two read repositories they
// need. Taking the repositories (not the shared usecases.Deps bundle, which
// also carries the unit of work, the outbox and the payment provider) keeps
// cmd/a2a from ever reaching a write seam.
func NewDeps(auths ports.AuthorizationRepository, refunds ports.RefundRepository) Deps {
	deps := usecases.Deps{Authorizations: auths, Refunds: refunds}
	return Deps{
		GetAuthorization:   &usecases.GetAuthorization{Deps: deps},
		ListAuthorizations: &usecases.ListAuthorizations{Deps: deps},
		GetRefund:          &usecases.GetRefund{Deps: deps},
		ListRefunds:        &usecases.ListRefunds{Deps: deps},
	}
}

// Card builds this agent's Agent Card (ADR 0005). The surface is two curated,
// goal-shaped READ skills derived from the four MCP tools of this context
// (get_payment_authorization, list_payment_authorizations, get_refund,
// list_refunds): the MCP charter is read-only, so the agent is read-only too
// and never widens what the context exposes. The card declares no security
// schemes on purpose: this surface is unauthenticated by the same fleet-wide
// decision as REST and MCP; schemes are added here, not in a redesign, if that
// decision is ever reversed.
func Card(cfg Config) *a2a.AgentCard {
	return &a2a.AgentCard{
		Name:        "customer-payments",
		Description: "Customer Payments agent for the storefront: the read-only source of truth for what has been authorized, captured, released and refunded for a checkout session or an order. It answers where a payment stands (state, total, allocations per shipment group with their capture state and tracking numbers, expiry, decline reason) and how a refund went (state, reason, group, attempts, failure code). It is read-only: it never authorizes, voids, captures or refunds, and it never sees or returns a card number, a PSP token or any payment instrument.",
		Version:     cfg.Version,
		Provider: &a2a.AgentProvider{
			Org: "IQVO",
			URL: "https://github.com/IQVO/customer-payments",
		},
		SupportedInterfaces: []*a2a.AgentInterface{
			a2a.NewAgentInterface(cfg.AgentURL, a2a.TransportProtocolJSONRPC),
		},
		DefaultInputModes:  []string{"application/json", "text/plain"},
		DefaultOutputModes: []string{"application/json", "text/plain"},
		Capabilities:       a2a.AgentCapabilities{Streaming: false},
		Skills: []a2a.AgentSkill{
			{
				ID:          skillGetPaymentStatus,
				Name:        "Where does a payment stand",
				Description: "Read the payment authorizations of one checkout session or one order, or one authorization by id. Give exactly one of authorizationId, orderId and checkoutSessionId. Each authorization carries its state (AUTHORIZED, PARTIALLY_CAPTURED, CAPTURED, RELEASED, VOIDED, EXPIRED or DECLINED), the checkout session and order it belongs to, the currency and total in minor units, every allocation per shipment group (groupNo, amountMinor, PENDING, CAPTURED or RELEASED, tracking numbers), when the hold expires and the decline reason. A checkout session lists declined attempts too. An unknown authorizationId answers a 404 problem; an order or session with no authorization answers an empty list. Persists nothing and returns no card data.",
				Tags:        []string{"payment", "authorization", "read"},
				InputModes:  []string{"application/json"},
				Examples: []string{
					`{"skill":"get_payment_status","arguments":{"orderId":"ord-1"}}`,
					`{"skill":"get_payment_status","arguments":{"checkoutSessionId":"cs-1"}}`,
					`{"skill":"get_payment_status","arguments":{"authorizationId":"pay-3f9c1a2b4d5e"}}`,
				},
			},
			{
				ID:          skillGetRefundStatus,
				Name:        "How did a refund go",
				Description: "Read the refunds of one order (oldest first), or one refund by id. Give exactly one of refundId and orderId. Each refund carries its state (REQUESTED, SUCCEEDED or FAILED), reason (LOST, RETURNED_TO_SENDER or MANUAL), order, authorization, group, amount in minor units, how many provider attempts were made and the last failure code. An unknown refundId answers a 404 problem; an order with no refund answers an empty list. Persists nothing; it never requests or retries a refund.",
				Tags:        []string{"payment", "refund", "read"},
				InputModes:  []string{"application/json"},
				Examples: []string{
					`{"skill":"get_refund_status","arguments":{"orderId":"ord-1"}}`,
					`{"skill":"get_refund_status","arguments":{"refundId":"ref-3f9c1a2b4d5e"}}`,
				},
			},
		},
	}
}

// NewExecutor returns the AgentExecutor that maps an incoming message to a
// skill and answers with a single agent Message (a message-only agent in A2A
// terms): both curated skills are transactional request/response reads, so
// there is no long-running Task lifecycle to track in this first A2A surface.
// The response carries the caller's contextId (and taskId when the client
// referenced one) so a delegating saga can group its interactions.
func NewExecutor(deps Deps) a2asrv.AgentExecutor {
	return a2asrv.AgentExecutorFunc(func(ctx context.Context, ec *a2asrv.ExecutorContext) iter.Seq2[a2a.Event, error] {
		return func(yield func(a2a.Event, error) bool) {
			yield(answer(ctx, deps, ec), nil)
		}
	})
}

// Handler wires the executor into the transport-agnostic A2A request handler
// and wraps it in the JSON-RPC 2.0 HTTP handler, the only transport this
// agent ships (ADR 0005), mirroring the MCP adapter's "Streamable HTTP only"
// minimalism.
func Handler(executor a2asrv.AgentExecutor) http.Handler {
	return a2asrv.NewJSONRPCHandler(a2asrv.NewHandler(executor))
}
