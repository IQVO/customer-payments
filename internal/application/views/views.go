// Package views holds the read shapes use cases return; the HTTP adapter maps
// them to JSON. They carry no behaviour.
package views

import (
	"time"

	"github.com/IQVO/customer-payments/internal/domain/paymentauthorization"
	"github.com/IQVO/customer-payments/internal/domain/refund"
)

// Allocation is one allocation of an authorization.
type Allocation struct {
	GroupNo         int
	AmountMinor     int64
	State           string
	TrackingNumber  string
	TrackingNumbers []string
}

// Authorization is the PaymentAuthorization read shape.
type Authorization struct {
	AuthorizationID         string
	CheckoutSessionID       string
	OrderID                 string
	State                   string
	Currency                string
	TotalMinor              int64
	Allocations             []Allocation
	ProviderAuthorizationID string
	ExpiresAt               time.Time
	DeclineReason           string
	Version                 int64
}

// AuthorizationFrom maps the aggregate.
func AuthorizationFrom(p *paymentauthorization.PaymentAuthorization) Authorization {
	v := Authorization{
		AuthorizationID: string(p.ID()), CheckoutSessionID: p.CheckoutSessionID(), OrderID: p.OrderID(),
		State: string(p.State()), Currency: p.Currency(), TotalMinor: p.TotalMinor(),
		ProviderAuthorizationID: p.ProviderAuthorizationID(), ExpiresAt: p.ExpiresAt(),
		DeclineReason: p.DeclineReason(), Version: p.Version(),
	}
	for _, a := range p.Allocations() {
		v.Allocations = append(v.Allocations, Allocation{
			GroupNo: a.GroupNo, AmountMinor: a.AmountMinor, State: string(a.State),
			TrackingNumber: a.TrackingNumber(), TrackingNumbers: a.TrackingNumbers,
		})
	}
	return v
}

// Refund is the Refund read shape.
type Refund struct {
	RefundID         string
	AuthorizationID  string
	OrderID          string
	GroupNo          int
	Currency         string
	AmountMinor      int64
	Reason           string
	State            string
	Attempts         int
	FailureCode      string
	ProviderRefundID string
	Version          int64
}

// RefundFrom maps the aggregate.
func RefundFrom(r *refund.Refund) Refund {
	return Refund{
		RefundID: string(r.ID()), AuthorizationID: r.AuthorizationID(), OrderID: r.OrderID(), GroupNo: r.GroupNo(),
		Currency: r.Currency(), AmountMinor: r.AmountMinor(), Reason: string(r.Reason()), State: string(r.State()),
		Attempts: r.Attempts(), FailureCode: r.FailureCode(), ProviderRefundID: r.ProviderRefundID(), Version: r.Version(),
	}
}
