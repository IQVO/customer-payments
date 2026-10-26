// Package http is the REST inbound adapter: every route of apis/openapi.yaml on
// a chi router, RFC 9457 problem documents for every error and the liveness and
// readiness endpoints. It has no auth middleware (fleet decision 2026-09-11;
// internal/architecture's TestNoAuthMiddlewareReintroduced).
package http

import (
	"context"
	"net/http"

	"github.com/go-chi/chi/v5"

	"github.com/IQVO/customer-payments/internal/application/ports"
	"github.com/IQVO/customer-payments/internal/application/usecases"
)

// Route names under which the Idempotency-Key records are stored.
const (
	routeAuthorize = "POST /payment-authorizations"
	routeVoid      = "POST /payment-authorizations/{authorizationId}:void"
	routeRefund    = "POST /refunds"
	routeRetry     = "POST /refunds/{refundId}:retry"
)

// Server holds the use cases the handlers call.
type Server struct {
	AuthorizePayment   usecases.AuthorizePayment
	VoidAuthorization  usecases.VoidAuthorization
	GetAuthorization   usecases.GetAuthorization
	ListAuthorizations usecases.ListAuthorizations
	RequestRefund      usecases.RequestRefund
	RetryRefund        usecases.RetryRefund
	GetRefund          usecases.GetRefund
	ListRefunds        usecases.ListRefunds

	// IdempotencyStore and Clock back the Idempotency-Key middleware of the
	// POST routes.
	IdempotencyStore ports.IdempotencyStore
	Clock            ports.Clock
	// ParkedFacts backs the gauge of GET /metrics (H10): how many fulfilment
	// facts wait for their order to be bound; nil omits the gauge.
	ParkedFacts func(ctx context.Context) (int, error)
	// Readiness backs GET /readyz; nil means always ready (unit tests).
	Readiness *Readiness
}

// NewRouter builds the chi router for every endpoint of apis/openapi.yaml.
func NewRouter(s *Server) http.Handler {
	r := chi.NewRouter()
	// A NUL (U+0000) in the path, the query or a body is a uniform client error,
	// never a Postgres 22021 -> 500; it runs before the Idempotency-Key
	// middleware so a rejected request never records its key.
	r.Use(rejectNUL)

	r.Get("/healthz", handleHealthz)
	r.Get("/readyz", s.handleReadyz)
	r.Get("/metrics", s.handleMetrics)

	r.With(s.idempotent(routeAuthorize)).Post("/payment-authorizations", s.handleAuthorize)
	r.Get("/payment-authorizations", s.handleListAuthorizations)
	r.Get("/payment-authorizations/{authorizationId}", s.handleGetAuthorization)
	r.With(s.idempotent(routeVoid)).Post("/payment-authorizations/{authorizationId}:void", s.handleVoid)

	r.With(s.idempotent(routeRefund)).Post("/refunds", s.handleRequestRefund)
	r.Get("/refunds", s.handleListRefunds)
	r.Get("/refunds/{refundId}", s.handleGetRefund)
	r.With(s.idempotent(routeRetry)).Post("/refunds/{refundId}:retry", s.handleRetryRefund)
	return r
}

func (s *Server) idempotent(route string) func(http.Handler) http.Handler {
	return RequireIdempotencyKey(s.IdempotencyStore, s.Clock, route)
}
