package usecases

import (
	"context"
	"errors"
	"time"

	"github.com/IQVO/customer-payments/internal/application/parked"
	"github.com/IQVO/customer-payments/internal/application/ports"
	"github.com/IQVO/customer-payments/internal/domain/paymentauthorization"
	"github.com/IQVO/customer-payments/internal/domain/shared"
)

// ErrInvalidEvent marks a consumed event that can never be applied (an id the
// domain cannot hold, a missing field): deterministic, never retried.
var ErrInvalidEvent = errors.New("invalid event")

// Consumer applies the events customer-payments consumes. Every method claims
// the CloudEvents id and applies its effect in ONE unit of work, so a rollback
// un-claims the id and a redelivery converges.
type Consumer struct{ Deps }

// CheckoutCompletedEvent is what BindOrderOnCheckoutCompleted needs of
// CheckoutCompleted.
type CheckoutCompletedEvent struct {
	SessionID       string
	OrderID         string
	AuthorizationID string
}

// CheckoutCompleted is the policy BindOrderOnCheckoutCompleted: the order id
// is bound to the session's authorization, then every fact parked for the
// order is applied (S4).
func (c Consumer) CheckoutCompleted(ctx context.Context, eventID, eventType string, e CheckoutCompletedEvent) error {
	if !shared.ValidID(e.OrderID) || !shared.ValidID(e.AuthorizationID) {
		return ErrInvalidEvent
	}
	return c.once(ctx, eventID, eventType, func(ctx context.Context) error {
		p, err := c.Authorizations.Get(ctx, paymentauthorization.ID(e.AuthorizationID))
		if err != nil {
			return err
		}
		if e.SessionID != "" && e.SessionID != p.CheckoutSessionID() {
			return ErrInvalidEvent
		}
		changed, err := p.BindOrder(e.OrderID)
		if err != nil {
			return err
		}
		if changed {
			if err := c.Authorizations.Save(ctx, p); err != nil {
				return err
			}
		}
		return c.replayParked(ctx, p)
	})
}

// Fact is the part of a consumed fulfilment or tracking event the policies use.
type Fact struct {
	Kind            parked.Kind
	OrderID         string
	GroupNo         int
	TrackingNumbers []string
	OccurredAt      time.Time
}

// ApplyFact runs the policy that reacts to the fact: CaptureOnShipment for
// FulfillmentShipped, ReleaseOnCancellation for FulfillmentCancelled and
// FulfillmentUnsourceable, RefundOnLostPackage for PackageLost and
// RefundOnReturnToSender for PackageReturned. A fact for an order that has no
// authorization yet is parked and applied on bind (S4).
func (c Consumer) ApplyFact(ctx context.Context, eventID, eventType string, f Fact) error {
	if !shared.ValidID(f.OrderID) || f.GroupNo < 0 {
		return ErrInvalidEvent
	}
	return c.once(ctx, eventID, eventType, func(ctx context.Context) error {
		pf := parked.Fact{
			EventID: eventID, Kind: f.Kind, OrderID: f.OrderID, GroupNo: f.GroupNo,
			TrackingNumbers: f.TrackingNumbers, OccurredAt: f.OccurredAt, ParkedAt: c.now(),
		}
		p, err := c.Authorizations.FindByOrder(ctx, f.OrderID)
		if errors.Is(err, ports.ErrNotFound) {
			return c.Parked.Park(ctx, pf)
		}
		if err != nil {
			return err
		}
		wait, err := c.applyFact(ctx, p, pf)
		if err != nil {
			return err
		}
		if wait {
			return c.Parked.Park(ctx, pf)
		}
		if f.Kind == parked.Shipped {
			return c.replayParked(ctx, p)
		}
		return nil
	})
}

// once claims the event id and runs fn in the same unit of work.
func (c Consumer) once(ctx context.Context, eventID, eventType string, fn func(ctx context.Context) error) error {
	_, err := transact(ctx, c.UoW, consumerAttempts, func(ctx context.Context) (struct{}, error) {
		first, err := c.Inbox.Claim(ctx, eventID, eventType)
		if err != nil || !first {
			return struct{}{}, err
		}
		return struct{}{}, fn(ctx)
	})
	return err
}
