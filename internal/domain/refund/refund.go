// Package refund is the Refund aggregate: money returned for one order or one
// shipment group, because a package was lost or returned to sender, or by an
// operator's decision.
//
// States: REQUESTED, then SUCCEEDED or FAILED (RecordRefundOutcome). A FAILED
// refund is retryable (H8): RetryRefund moves it back to REQUESTED, at most
// MaxAttempts provider attempts in all, after which it stays FAILED and the
// RefundFailed event says so (Final) for the operator alert.
//
// Invariant: refunds of an order never exceed its captured total
// (ErrOverRefund). The amounts that count are REQUESTED and SUCCEEDED ones,
// never FAILED (H8); the caller passes them in Limits.
package refund

import (
	"errors"
	"fmt"
	"time"

	"github.com/IQVO/customer-payments/internal/domain/shared"
)

const entity = "refund"

// ErrOverRefund: refunds never exceed the captured total (409 over-refund).
var ErrOverRefund = errors.New("over refund")

// MaxAttempts is how many provider attempts a refund gets (H8).
const MaxAttempts = 5

// ID identifies a refund.
type ID string

// State is the lifecycle state of a refund.
type State string

// The three states.
const (
	StateRequested State = "REQUESTED"
	StateSucceeded State = "SUCCEEDED"
	StateFailed    State = "FAILED"
)

// Reason says why money goes back.
type Reason string

// The three reasons.
const (
	ReasonLost             Reason = "LOST"
	ReasonReturnedToSender Reason = "RETURNED_TO_SENDER"
	ReasonManual           Reason = "MANUAL"
)

// Valid reports whether r is one of the three reasons.
func (r Reason) Valid() bool {
	return r == ReasonLost || r == ReasonReturnedToSender || r == ReasonManual
}

// Request is the validated input of RequestRefund.
type Request struct {
	ID              ID
	AuthorizationID string
	OrderID         string
	// GroupNo is the refunded shipment group; 0 for an order-level refund.
	GroupNo     int
	Currency    string
	AmountMinor int64
	Reason      Reason
}

// Limits are the figures ErrOverRefund is judged against, for the scope of the
// request (the group when GroupNo > 0, else the order).
type Limits struct {
	CapturedMinor int64
	// RefundedMinor sums the REQUESTED and SUCCEEDED refunds of the scope.
	RefundedMinor int64
}

// Refund is the aggregate root.
type Refund struct {
	req              Request
	state            State
	attempts         int
	failureCode      string
	providerRefundID string
	createdAt        time.Time
	version          int64
	loaded           int64
}

// New creates a REQUESTED refund after checking the request and the limits.
func New(r Request, l Limits, now time.Time) (*Refund, error) {
	if err := checkRequest(r); err != nil {
		return nil, err
	}
	if l.RefundedMinor+r.AmountMinor > l.CapturedMinor {
		return nil, fmt.Errorf("%w: %d already refunded or requested plus %d exceeds the %d captured",
			ErrOverRefund, l.RefundedMinor, r.AmountMinor, l.CapturedMinor)
	}
	return &Refund{req: r, state: StateRequested, createdAt: shared.UTC(now), version: 1}, nil
}

func checkRequest(r Request) error {
	if !shared.ValidID(string(r.ID)) || !shared.ValidID(r.AuthorizationID) || !shared.ValidID(r.OrderID) {
		return shared.Invalid("refundId, authorizationId and orderId must be valid ids")
	}
	if r.GroupNo < 0 || r.GroupNo > shared.MaxInt32 {
		return shared.Invalid("groupNo %d must be between 0 and %d", r.GroupNo, shared.MaxInt32)
	}
	if !shared.ValidCurrency(r.Currency) {
		return shared.Invalid("currency %q is not an ISO 4217 code", r.Currency)
	}
	if r.AmountMinor < 1 || r.AmountMinor > shared.MaxMinor {
		return shared.Invalid("amountMinor %d must be between 1 and %d", r.AmountMinor, shared.MaxMinor)
	}
	if !r.Reason.Valid() {
		return shared.Invalid("reason %q is not LOST, RETURNED_TO_SENDER or MANUAL", r.Reason)
	}
	return nil
}

// ID returns the refundId.
func (r *Refund) ID() ID { return r.req.ID }

// AuthorizationID returns the authorization the money returns through (H12).
func (r *Refund) AuthorizationID() string { return r.req.AuthorizationID }

// OrderID returns the refunded order.
func (r *Refund) OrderID() string { return r.req.OrderID }

// GroupNo returns the refunded group, 0 for an order-level refund (H12).
func (r *Refund) GroupNo() int { return r.req.GroupNo }

// Currency returns the ISO 4217 currency.
func (r *Refund) Currency() string { return r.req.Currency }

// AmountMinor returns the refunded amount in minor units.
func (r *Refund) AmountMinor() int64 { return r.req.AmountMinor }

// Reason returns why the money goes back.
func (r *Refund) Reason() Reason { return r.req.Reason }

// State returns the lifecycle state.
func (r *Refund) State() State { return r.state }

// Attempts returns how many provider attempts were recorded.
func (r *Refund) Attempts() int { return r.attempts }

// FailureCode returns the code of the last failure, "" if none.
func (r *Refund) FailureCode() string { return r.failureCode }

// ProviderRefundID returns the provider's reference of a SUCCEEDED refund.
func (r *Refund) ProviderRefundID() string { return r.providerRefundID }

// CreatedAt returns when the refund was requested.
func (r *Refund) CreatedAt() time.Time { return r.createdAt }

// Version returns the aggregate version; every change adds one.
func (r *Refund) Version() int64 { return r.version }

// LoadedVersion returns the version the aggregate had when it was loaded (0
// for a new one): the version a repository expects to find stored.
func (r *Refund) LoadedVersion() int64 { return r.loaded }

// MarkPersisted records that the current version is stored.
func (r *Refund) MarkPersisted() { r.loaded = r.version }

// Exhausted reports whether a FAILED refund used all its attempts.
func (r *Refund) Exhausted() bool { return r.state == StateFailed && r.attempts >= MaxAttempts }
