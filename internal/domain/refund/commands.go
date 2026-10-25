package refund

import (
	"fmt"
	"time"

	"github.com/IQVO/customer-payments/internal/domain/shared"
)

func (r *Refund) header(now time.Time) shared.Header {
	return shared.Header{Version: r.version, At: shared.UTC(now)}
}

// RecordSuccess records that the provider returned the money
// (RecordRefundOutcome, REQUESTED to SUCCEEDED) and raises RefundIssued.
func (r *Refund) RecordSuccess(providerRefundID string, now time.Time) ([]shared.Event, error) {
	if r.state != StateRequested {
		return nil, illegal("record an outcome of", r.state)
	}
	if providerRefundID == "" {
		return nil, shared.Invalid("a successful refund needs the provider's refund id")
	}
	r.state, r.providerRefundID, r.failureCode = StateSucceeded, providerRefundID, ""
	r.attempts++
	r.version++
	return []shared.Event{RefundIssued{
		Header: r.header(now), RefundID: r.req.ID, OrderID: r.req.OrderID, AuthorizationID: r.req.AuthorizationID,
		GroupNo: r.req.GroupNo, AmountMinor: r.req.AmountMinor, Reason: r.req.Reason,
	}}, nil
}

// RecordFailure records that the provider refused the refund
// (RecordRefundOutcome, REQUESTED to FAILED) and raises RefundFailed.
func (r *Refund) RecordFailure(code string, now time.Time) ([]shared.Event, error) {
	if r.state != StateRequested {
		return nil, illegal("record an outcome of", r.state)
	}
	if code == "" {
		return nil, shared.Invalid("a failed refund needs a failure code")
	}
	r.state, r.failureCode = StateFailed, code
	r.attempts++
	r.version++
	return []shared.Event{RefundFailed{
		Header: r.header(now), RefundID: r.req.ID, OrderID: r.req.OrderID, AuthorizationID: r.req.AuthorizationID,
		GroupNo: r.req.GroupNo, Code: code, Attempts: r.attempts, Final: r.attempts >= MaxAttempts,
	}}, nil
}

// Retry moves a FAILED refund back to REQUESTED (RetryRefund, H8) while it has
// attempts left; an exhausted refund stays FAILED.
func (r *Refund) Retry() error {
	if r.state != StateFailed {
		return illegal("retry", r.state)
	}
	if r.Exhausted() {
		return fmt.Errorf("%w: all %d attempts were used", shared.ErrIllegalTransition, MaxAttempts)
	}
	r.state = StateRequested
	r.version++
	return nil
}

func illegal(action string, s State) error {
	return fmt.Errorf("%w: cannot %s a refund that is %s", shared.ErrIllegalTransition, action, s)
}
