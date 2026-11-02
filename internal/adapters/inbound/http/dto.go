package http

import (
	"time"

	"github.com/IQVO/customer-payments/internal/application/views"
	"github.com/IQVO/customer-payments/internal/domain/paymentauthorization"
)

// allocationIn is one allocation of the AuthorizeRequest.
type allocationIn struct {
	GroupNo     int   `json:"groupNo"`
	AmountMinor int64 `json:"amountMinor"`
}

// authorizeRequest is the body of POST /payment-authorizations.
type authorizeRequest struct {
	CheckoutSessionID  string         `json:"checkoutSessionId"`
	PaymentMethodToken string         `json:"paymentMethodToken"`
	Currency           string         `json:"currency"`
	TotalMinor         int64          `json:"totalMinor"`
	Allocations        []allocationIn `json:"allocations"`
}

func (r authorizeRequest) allocations() []paymentauthorization.AllocationInput {
	out := make([]paymentauthorization.AllocationInput, len(r.Allocations))
	for i, a := range r.Allocations {
		out[i] = paymentauthorization.AllocationInput(a)
	}
	return out
}

// refundRequest is the body of POST /refunds. groupNo is optional: absent or 0
// refunds at the order level.
type refundRequest struct {
	OrderID     string `json:"orderId"`
	GroupNo     int    `json:"groupNo"`
	AmountMinor int64  `json:"amountMinor"`
	Reason      string `json:"reason"`
}

type allocationOut struct {
	GroupNo         int      `json:"groupNo"`
	AmountMinor     int64    `json:"amountMinor"`
	State           string   `json:"state"`
	TrackingNumber  *string  `json:"trackingNumber"`
	TrackingNumbers []string `json:"trackingNumbers"`
}

type authorizationOut struct {
	AuthorizationID         string          `json:"authorizationId"`
	CheckoutSessionID       string          `json:"checkoutSessionId"`
	OrderID                 *string         `json:"orderId"`
	State                   string          `json:"state"`
	Currency                string          `json:"currency"`
	TotalMinor              int64           `json:"totalMinor"`
	Allocations             []allocationOut `json:"allocations"`
	ProviderAuthorizationID *string         `json:"providerAuthorizationId"`
	ExpiresAt               *time.Time      `json:"expiresAt,omitempty"`
	DeclineReason           string          `json:"declineReason,omitempty"`
}

type refundOut struct {
	RefundID         string `json:"refundId"`
	AuthorizationID  string `json:"authorizationId"`
	OrderID          string `json:"orderId"`
	GroupNo          int    `json:"groupNo,omitempty"`
	AmountMinor      int64  `json:"amountMinor"`
	Reason           string `json:"reason"`
	State            string `json:"state"`
	Attempts         int    `json:"attempts"`
	FailureCode      string `json:"failureCode,omitempty"`
	ProviderRefundID string `json:"providerRefundId,omitempty"`
}

func strPtr(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

func authorizationFrom(v views.Authorization) authorizationOut {
	out := authorizationOut{
		AuthorizationID: v.AuthorizationID, CheckoutSessionID: v.CheckoutSessionID, OrderID: strPtr(v.OrderID),
		State: v.State, Currency: v.Currency, TotalMinor: v.TotalMinor, Allocations: make([]allocationOut, 0, len(v.Allocations)),
		ProviderAuthorizationID: strPtr(v.ProviderAuthorizationID), DeclineReason: v.DeclineReason,
	}
	if !v.ExpiresAt.IsZero() {
		t := v.ExpiresAt.UTC()
		out.ExpiresAt = &t
	}
	for _, a := range v.Allocations {
		tracking := a.TrackingNumbers
		if tracking == nil {
			tracking = []string{}
		}
		out.Allocations = append(out.Allocations, allocationOut{
			GroupNo: a.GroupNo, AmountMinor: a.AmountMinor, State: a.State, TrackingNumber: strPtr(a.TrackingNumber), TrackingNumbers: tracking,
		})
	}
	return out
}

func refundFrom(v views.Refund) refundOut {
	return refundOut{
		RefundID: v.RefundID, AuthorizationID: v.AuthorizationID, OrderID: v.OrderID, GroupNo: v.GroupNo, AmountMinor: v.AmountMinor,
		Reason: v.Reason, State: v.State, Attempts: v.Attempts, FailureCode: v.FailureCode, ProviderRefundID: v.ProviderRefundID,
	}
}
