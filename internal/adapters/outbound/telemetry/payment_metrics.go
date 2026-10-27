package telemetry

import (
	"context"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"

	"github.com/IQVO/customer-payments/internal/application/ports"
)

// meterName scopes this service's own instruments, keeping them distinct from
// the ones otelchi and the runtime collector register.
const meterName = "github.com/IQVO/customer-payments"

// authorizationsDecidedName is the Tier 2 business metric of the context (ADR
// 0004, the fleet's standard metrics convention): every authorization attempt,
// by outcome.
const authorizationsDecidedName = "payments.authorizations.decided"

const (
	outcomeKey = attribute.Key("outcome")
	reasonKey  = attribute.Key("reason")

	outcomeAuthorized = "authorized"
	outcomeDeclined   = "declined"
	outcomeFailed     = "failed"
)

// PaymentMetrics implements ports.PaymentMetrics against the global
// MeterProvider. Until Setup installs a real provider the global one is a
// no-op, so recording is cheap and safe in tests and local runs.
type PaymentMetrics struct {
	counter metric.Int64Counter
}

var _ ports.PaymentMetrics = (*PaymentMetrics)(nil)

// NewPaymentMetrics registers the counter. It only fails if the instrument name
// is invalid, which is a programming error, not a runtime condition. Call it
// after Setup so the instrument binds to the real MeterProvider.
func NewPaymentMetrics() (*PaymentMetrics, error) {
	counter, err := otel.Meter(meterName).Int64Counter(
		authorizationsDecidedName,
		metric.WithDescription("Payment authorization attempts by outcome (authorized, declined or failed; reason says why: "+
			"the provider's decline code such as CARD_DECLINED, or for a failure a slug such as payment-provider-unavailable). "+
			"A falling authorized share with a rising declined means shoppers' payment methods are being refused at checkout; "+
			"a rising failed with payment-provider-unavailable means the provider is unreachable and checkouts are blocked, "+
			"not that shoppers are doing anything wrong."),
		metric.WithUnit("{authorization}"),
	)
	if err != nil {
		return nil, err
	}
	return &PaymentMetrics{counter: counter}, nil
}

// AuthorizationAuthorized counts a hold that was placed.
func (m *PaymentMetrics) AuthorizationAuthorized(ctx context.Context) {
	m.counter.Add(ctx, 1, metric.WithAttributes(outcomeKey.String(outcomeAuthorized)))
}

// AuthorizationDeclined counts a hold the provider refused, with its code.
func (m *PaymentMetrics) AuthorizationDeclined(ctx context.Context, reason string) {
	m.counter.Add(ctx, 1, metric.WithAttributes(outcomeKey.String(outcomeDeclined), reasonKey.String(reason)))
}

// AuthorizationFailed counts an attempt that reached no decision, with why.
func (m *PaymentMetrics) AuthorizationFailed(ctx context.Context, reason string) {
	m.counter.Add(ctx, 1, metric.WithAttributes(outcomeKey.String(outcomeFailed), reasonKey.String(reason)))
}
