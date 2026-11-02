package usecases

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/IQVO/customer-payments/internal/application/pagination"
	"github.com/IQVO/customer-payments/internal/application/ports"
	"github.com/IQVO/customer-payments/internal/application/provider"
	"github.com/IQVO/customer-payments/internal/application/views"
	"github.com/IQVO/customer-payments/internal/domain/paymentauthorization"
	"github.com/IQVO/customer-payments/internal/domain/refund"
	"github.com/IQVO/customer-payments/internal/domain/shared"
)

// limitsFor computes the figures ErrOverRefund is judged against from the
// captured amounts of p and the refunds already made through it. FAILED
// refunds never count (H8).
func limitsFor(p *pa, existing []*refund.Refund, groupNo int) refund.Limits {
	l := refund.Limits{OrderCapturedMinor: p.CapturedMinor()}
	for _, f := range existing {
		if f.State() != refund.StateFailed {
			l.OrderRefundedMinor += f.AmountMinor()
		}
	}
	l.CapturedMinor, l.RefundedMinor = l.OrderCapturedMinor, l.OrderRefundedMinor
	if groupNo == 0 {
		return l
	}
	l.CapturedMinor, l.RefundedMinor = 0, 0
	if a, ok := p.Allocation(groupNo); ok {
		l.CapturedMinor = a.CapturedMinor
	}
	for _, f := range existing {
		if f.State() != refund.StateFailed && f.GroupNo() == groupNo {
			l.RefundedMinor += f.AmountMinor()
		}
	}
	return l
}

// openRefund creates the REQUESTED refund, saves it and tries the provider
// once. A provider that is unreachable leaves the refund REQUESTED for the
// policy ProcessRequestedRefunds.
func (d Deps) openRefund(ctx context.Context, p *pa, groupNo int, amount int64, reason refund.Reason) (*refund.Refund, error) {
	existing, err := d.Refunds.ListByAuthorization(ctx, p.ID())
	if err != nil {
		return nil, err
	}
	f, err := refund.New(refund.Request{
		ID: d.IDs.NewRefundID(), AuthorizationID: string(p.ID()), OrderID: p.OrderID(), GroupNo: groupNo,
		Currency: p.Currency(), AmountMinor: amount, Reason: reason,
	}, limitsFor(p, existing, groupNo), d.now())
	if err != nil {
		return nil, err
	}
	if err := d.Refunds.Save(ctx, f); err != nil {
		return nil, err
	}
	return f, d.settleRefund(ctx, p, f)
}

// settleRefund asks the provider to return the money of a REQUESTED refund
// and records the outcome.
func (d Deps) settleRefund(ctx context.Context, p *pa, f *refund.Refund) error {
	key := Key("refund", string(p.ID()), f.GroupNo(), fmt.Sprintf("%s:%d", f.ID(), f.Attempts()+1))
	id, err := d.Provider.Refund(ctx, key, p.ProviderAuthorizationID(), f.AmountMinor())
	var events []shared.Event
	switch {
	case err == nil:
		events, err = f.RecordSuccess(id, d.now())
	case errors.Is(err, provider.ErrDeclined):
		events, err = f.RecordFailure(provider.Code(err), d.now())
		if err == nil && f.Exhausted() {
			d.logger().ErrorContext(ctx, "ALERT refund failed for good: operator action needed",
				"refund_id", string(f.ID()), "order_id", f.OrderID(), "code", f.FailureCode(), "attempts", f.Attempts())
		}
	default:
		d.logger().WarnContext(ctx, "refund left REQUESTED: payment provider unavailable", "refund_id", string(f.ID()), "error", err)
		return nil
	}
	if err != nil {
		return err
	}
	if err := d.Refunds.Save(ctx, f); err != nil {
		return err
	}
	return d.publish(ctx, events)
}

// RequestRefund is the command RequestRefund (POST /refunds), the ops route:
// a MANUAL (or explicit LOST / RETURNED_TO_SENDER) refund of an order, or of
// one of its groups.
type RequestRefund struct{ Deps }

// RequestRefundInput is the body of POST /refunds.
type RequestRefundInput struct {
	OrderID     string
	GroupNo     int
	AmountMinor int64
	Reason      string
}

// Execute requests the refund and tries the provider once. An order without an
// authorization is ports.ErrNotFound; refunds beyond the captured total are
// refund.ErrOverRefund.
func (u RequestRefund) Execute(ctx context.Context, in RequestRefundInput) (views.Refund, error) {
	return transact(ctx, u.UoW, commandAttempts, func(ctx context.Context) (views.Refund, error) {
		if !shared.ValidID(in.OrderID) {
			return views.Refund{}, shared.Invalid("orderId %q is not a valid id", in.OrderID)
		}
		p, err := u.Authorizations.FindByOrder(ctx, in.OrderID)
		if err != nil {
			return views.Refund{}, err
		}
		f, err := u.openRefund(ctx, p, in.GroupNo, in.AmountMinor, refund.Reason(in.Reason))
		if err != nil {
			return views.Refund{}, err
		}
		return views.RefundFrom(f), nil
	})
}

// RetryRefund is the command RetryRefund (POST /refunds/{refundId}:retry, H8).
type RetryRefund struct{ Deps }

// Execute moves a FAILED refund back to REQUESTED and tries the provider.
func (u RetryRefund) Execute(ctx context.Context, id string) (views.Refund, error) {
	return transact(ctx, u.UoW, commandAttempts, func(ctx context.Context) (views.Refund, error) {
		f, err := u.Refunds.Get(ctx, refund.ID(id))
		if err != nil {
			return views.Refund{}, err
		}
		if err := f.Retry(); err != nil {
			return views.Refund{}, err
		}
		p, err := u.Authorizations.Get(ctx, paymentauthorization.ID(f.AuthorizationID()))
		if err != nil {
			return views.Refund{}, err
		}
		if err := u.Refunds.Save(ctx, f); err != nil {
			return views.Refund{}, err
		}
		if err := u.settleRefund(ctx, p, f); err != nil {
			return views.Refund{}, err
		}
		return views.RefundFrom(f), nil
	})
}

// GetRefund is the query GetRefund (GET /refunds/{refundId}).
type GetRefund struct{ Deps }

// Execute returns the refund, or ports.ErrNotFound.
func (u GetRefund) Execute(ctx context.Context, id string) (views.Refund, error) {
	f, err := u.Refunds.Get(ctx, refund.ID(id))
	if err != nil {
		return views.Refund{}, err
	}
	return views.RefundFrom(f), nil
}

// ListRefunds is the query ListRefunds (GET /refunds?orderId=&limit=&cursor=).
type ListRefunds struct{ Deps }

// RefundPage is one page of ListRefunds: at most the requested number of
// refunds, oldest first, and the keyset to resume after when more follow.
type RefundPage struct {
	Items []views.Refund
	// Next is nil on the last page.
	Next *pagination.Keyset
}

// Execute returns one page of the refunds of the order, ordered by
// (created-at, id) ascending, starting strictly after the keyset (nil is the
// first page). limit must be at least 1.
func (u ListRefunds) Execute(ctx context.Context, orderID string, after *pagination.Keyset, limit int) (RefundPage, error) {
	if orderID == "" {
		return RefundPage{}, shared.Invalid("orderId is required")
	}
	if err := requirePageLimit(limit); err != nil {
		return RefundPage{}, err
	}
	// One row more than the page tells whether another page exists.
	list, err := u.Refunds.ListByOrder(ctx, orderID, after, limit+1)
	if err != nil {
		return RefundPage{}, err
	}
	items, next := pageOf(list, limit, views.RefundFrom,
		func(f *refund.Refund) pagination.Keyset {
			return pagination.Keyset{CreatedAt: f.CreatedAt(), ID: string(f.ID())}
		})
	return RefundPage{Items: items, Next: next}, nil
}

// All follows the pages of Execute and returns at most ToolRowBound refunds:
// the complete answer for the MCP and A2A read tools.
func (u ListRefunds) All(ctx context.Context, orderID string) ([]views.Refund, error) {
	return collect(ctx, ToolRowBound, func(ctx context.Context, after *pagination.Keyset, limit int) ([]views.Refund, *pagination.Keyset, error) {
		page, err := u.Execute(ctx, orderID, after, limit)
		return page.Items, page.Next, err
	})
}

// refundSettleDelay is how long a REQUESTED refund is left alone before the
// policy ProcessRequestedRefunds picks it up.
const refundSettleDelay = 30 * time.Second

// ProcessRequestedRefunds is the policy that finishes refunds left REQUESTED
// because the provider was unreachable.
type ProcessRequestedRefunds struct{ Deps }

// Execute handles one batch and returns how many refunds it settled.
func (u ProcessRequestedRefunds) Execute(ctx context.Context) (int, error) {
	ids, err := u.Refunds.DueRequested(ctx, u.now().Add(-refundSettleDelay), batchSize)
	if err != nil {
		return 0, err
	}
	handled := 0
	for _, id := range ids {
		_, err := transact(ctx, u.UoW, commandAttempts, func(ctx context.Context) (struct{}, error) {
			f, err := u.Refunds.Get(ctx, id)
			if err != nil || f.State() != refund.StateRequested {
				return struct{}{}, err
			}
			p, err := u.Authorizations.Get(ctx, paymentauthorization.ID(f.AuthorizationID()))
			if err != nil {
				return struct{}{}, err
			}
			return struct{}{}, u.settleRefund(ctx, p, f)
		})
		if err != nil && !errors.Is(err, ports.ErrConcurrentUpdate) {
			return handled, err
		}
		if err == nil {
			handled++
		}
	}
	return handled, nil
}
