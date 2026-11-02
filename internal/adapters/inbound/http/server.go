// Package http is the REST inbound adapter: every route of apis/openapi.yaml on
// a chi router, RFC 9457 problem documents for every error and the liveness and
// readiness endpoints. It has no auth middleware (fleet decision 2026-09-11;
// internal/architecture's TestNoAuthMiddlewareReintroduced).
package http

import (
	"context"
	"log/slog"
	"net/http"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
	"github.com/riandyrn/otelchi"
	otelchimetric "github.com/riandyrn/otelchi/metric"

	"github.com/IQVO/customer-payments/internal/adapters/inbound/requestlog"
	"github.com/IQVO/customer-payments/internal/application/ports"
	"github.com/IQVO/customer-payments/internal/application/usecases"
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

	// IdempotencyStore and Clock back the Idempotency-Key middleware of every
	// POST route.
	IdempotencyStore ports.IdempotencyStore
	Clock            ports.Clock
	// ParkedFacts backs the gauge of GET /metrics (H10): how many fulfilment
	// facts wait for their order to be bound; nil omits the gauge.
	ParkedFacts func(ctx context.Context) (int, error)
	// Readiness backs GET /readyz; nil means always ready (unit tests).
	Readiness *Readiness
}

// DefaultServiceName names the server spans and the HTTP metrics when the
// composition root passes no service name.
const DefaultServiceName = "customer-payments"

// NewRouter builds the chi router for every endpoint of apis/openapi.yaml. A nil
// logger defaults to slog.Default(); an empty serviceName to DefaultServiceName.
//
// Middleware order is the fleet observability standard: RequestID, then otelchi
// (the server span, named after the chi route pattern, so one span name per
// route and not per authorization id), then the request-duration histogram, then
// RequestLogger (it runs inside the span, so its record carries trace_id and
// span_id), then Recoverer. telemetry.Setup must have run before this call:
// otelchimetric binds the global MeterProvider when it is constructed.
func NewRouter(s *Server, logger *slog.Logger, serviceName string) http.Handler {
	if serviceName == "" {
		serviceName = DefaultServiceName
	}
	r := chi.NewRouter()
	r.Use(middleware.RequestID)
	r.Use(otelchi.Middleware(serviceName, otelchi.WithChiRoutes(r)))
	r.Use(otelchimetric.NewServerRequestDuration(otelchimetric.NewBaseConfig(serviceName)))
	r.Use(requestlog.RequestLogger(logger))
	r.Use(middleware.Recoverer)
	// A NUL (U+0000) in the path, the query or a body is a uniform client error,
	// never a Postgres 22021 -> 500; it runs before the Idempotency-Key
	// middleware so a rejected request never records its key.
	r.Use(rejectNUL)

	r.Get("/healthz", handleHealthz)
	r.Get("/readyz", s.handleReadyz)
	r.Get("/metrics", s.handleMetrics)

	idempotent := RequireIdempotencyKey(s.IdempotencyStore, s.Clock)

	r.With(idempotent).Post("/payment-authorizations", s.handleAuthorize)
	r.Get("/payment-authorizations", s.handleListAuthorizations)
	r.Get("/payment-authorizations/{authorizationId}", s.handleGetAuthorization)
	r.With(idempotent).Post("/payment-authorizations/{authorizationId}/voids", s.handleVoid)

	r.With(idempotent).Post("/refunds", s.handleRequestRefund)
	r.Get("/refunds", s.handleListRefunds)
	r.Get("/refunds/{refundId}", s.handleGetRefund)
	r.With(idempotent).Post("/refunds/{refundId}:retry", s.handleRetryRefund)
	return r
}
