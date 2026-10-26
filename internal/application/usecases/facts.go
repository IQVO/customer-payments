package usecases

import (
	"context"
	"errors"
	"fmt"

	"github.com/IQVO/customer-payments/internal/application/parked"
	"github.com/IQVO/customer-payments/internal/domain/paymentauthorization"
	"github.com/IQVO/customer-payments/internal/domain/refund"
	"github.com/IQVO/customer-payments/internal/domain/shared"
)

// applyFact applies one fulfilment or tracking fact to the authorization
// bound to its order: the policies CaptureOnShipment, ReleaseOnCancellation,
// RefundOnLostPackage and RefundOnReturnToSender. It saves p and publishes
// what the domain raised. wait is true for a refund fact whose tracking
// number no capture carries yet: the fact has to be parked until the shipment
// is applied.
func (d Deps) applyFact(ctx context.Context, p *pa, f parked.Fact) (wait bool, err error) {
	switch f.Kind {
	case parked.Shipped:
		err = d.captureGroup(ctx, p, f.GroupNo, f.TrackingNumbers)
	case parked.Cancelled:
		err = d.releaseGroup(ctx, p, f.GroupNo, paymentauthorization.ReasonCancelled)
	case parked.Unsourceable:
		err = d.releaseGroup(ctx, p, f.GroupNo, paymentauthorization.ReasonUnsourceable)
	case parked.Lost:
		return d.refundPackage(ctx, p, f, refund.ReasonLost)
	case parked.Returned:
		return d.refundPackage(ctx, p, f, refund.ReasonReturnedToSender)
	default:
		err = fmt.Errorf("unknown fact kind %q", f.Kind)
	}
	return false, err
}

func (d Deps) save(ctx context.Context, p *pa, events []shared.Event) error {
	if err := d.Authorizations.Save(ctx, p); err != nil {
		return err
	}
	return d.publish(ctx, events)
}

// captureGroup is CaptureOnShipment: the group's allocation is captured at the
// provider and the tracking numbers are stored (H6). A captured group is left
// alone.
func (d Deps) captureGroup(ctx context.Context, p *pa, groupNo int, trackings []string) error {
	a, ok := p.Allocation(groupNo)
	if !ok {
		return fmt.Errorf("%w: group %d", paymentauthorization.ErrUnknownGroup, groupNo)
	}
	if a.State == paymentauthorization.AllocCaptured {
		return nil
	}
	now := d.now()
	if err := check(p, func(c *pa) error { _, err := c.Capture(groupNo, a.AmountMinor, trackings, "check", now); return err }); err != nil {
		return err
	}
	id, err := d.Provider.Capture(ctx, Key("capture", string(p.ID()), groupNo, ""), p.ProviderAuthorizationID(), a.AmountMinor)
	if err != nil {
		return err
	}
	events, err := p.Capture(groupNo, a.AmountMinor, trackings, id, now)
	if err != nil {
		return err
	}
	return d.save(ctx, p, events)
}

// releaseGroup is ReleaseOnCancellation: the group's pending allocation is
// let go and the provider hold shrinks (H4). Releasing a released group, or one
// of a voided or expired authorization, is a no-op.
func (d Deps) releaseGroup(ctx context.Context, p *pa, groupNo int, reason string) error {
	if a, ok := p.Allocation(groupNo); ok && a.State == paymentauthorization.AllocReleased {
		return nil
	}
	if err := check(p, func(c *pa) error { _, err := c.Release(groupNo, reason, d.now()); return err }); err != nil {
		return err
	}
	events, err := p.Release(groupNo, reason, d.now())
	if err != nil {
		return err
	}
	if err := d.shrinkHold(ctx, p, groupNo); err != nil {
		return err
	}
	return d.save(ctx, p, events)
}

// shrinkHold lowers the provider hold to what is still pending, or releases it
// when nothing is.
func (d Deps) shrinkHold(ctx context.Context, p *pa, groupNo int) error {
	id := string(p.ID())
	if pending := p.PendingMinor(); pending > 0 {
		return d.Provider.ReduceAuthorization(ctx, Key("reduce", id, groupNo, ""), p.ProviderAuthorizationID(), pending)
	}
	return d.Provider.Void(ctx, Key("void", id, groupNo, "release"), p.ProviderAuthorizationID())
}

// refundPackage is RefundOnLostPackage / RefundOnReturnToSender: the captured
// amount of the group that carried the tracking number goes back, less what
// was refunded or requested already. Nothing left to refund is a no-op.
func (d Deps) refundPackage(ctx context.Context, p *pa, f parked.Fact, reason refund.Reason) (bool, error) {
	if len(f.TrackingNumbers) != 1 {
		return false, fmt.Errorf("%w: a %s fact names exactly one tracking number", ErrInvalidEvent, f.Kind)
	}
	groupNo, ok := p.GroupOfTracking(f.TrackingNumbers[0])
	if !ok {
		return true, nil
	}
	a, _ := p.Allocation(groupNo)
	existing, err := d.Refunds.ListByAuthorization(ctx, p.ID())
	if err != nil {
		return false, err
	}
	left := a.CapturedMinor - limitsFor(p, existing, groupNo).RefundedMinor
	if left <= 0 {
		return false, nil
	}
	_, err = d.openRefund(ctx, p, groupNo, left, reason)
	if errors.Is(err, refund.ErrOverRefund) {
		d.logger().WarnContext(ctx, "refund skipped: over refund", "order_id", p.OrderID(), "group_no", groupNo)
		return false, nil
	}
	return false, err
}

// replayParked applies the facts that waited for p (an order that was just
// bound, or a shipment that was just captured). Facts that still cannot be
// applied go back to the parking lot with their original parking time.
func (d Deps) replayParked(ctx context.Context, p *pa) error {
	facts, err := d.Parked.TakeForOrder(ctx, p.OrderID())
	if err != nil {
		return err
	}
	for _, f := range facts {
		wait, err := d.applyFact(ctx, p, f)
		if err != nil {
			return err
		}
		if wait {
			if err := d.Parked.Park(ctx, f); err != nil {
				return err
			}
		}
	}
	return nil
}
