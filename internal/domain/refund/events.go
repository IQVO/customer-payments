package refund

import "github.com/IQVO/customer-payments/internal/domain/shared"

// RefundIssued is raised when the provider returned the money (H12: it names
// the group and the authorization).
type RefundIssued struct {
	shared.Header
	RefundID        ID
	OrderID         string
	AuthorizationID string
	GroupNo         int
	AmountMinor     int64
	Reason          Reason
}

// RefundFailed is raised when the provider refused the refund. Final marks the
// last attempt (H8): the refund stays FAILED and an operator is alerted.
type RefundFailed struct {
	shared.Header
	RefundID        ID
	OrderID         string
	AuthorizationID string
	GroupNo         int
	Code            string
	Attempts        int
	Final           bool
}

// EventName returns "RefundIssued".
func (RefundIssued) EventName() string { return "RefundIssued" }

// EventName returns "RefundFailed".
func (RefundFailed) EventName() string { return "RefundFailed" }

// Entity returns "refund".
func (RefundIssued) Entity() string { return entity }

// Entity returns "refund".
func (RefundFailed) Entity() string { return entity }

// SubjectID returns the refundId.
func (e RefundIssued) SubjectID() string { return string(e.RefundID) }

// SubjectID returns the refundId.
func (e RefundFailed) SubjectID() string { return string(e.RefundID) }
