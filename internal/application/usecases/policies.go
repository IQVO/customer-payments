package usecases

import (
	"context"
	"errors"

	"github.com/IQVO/customer-payments/internal/application/parked"
	"github.com/IQVO/customer-payments/internal/application/ports"
	"github.com/IQVO/customer-payments/internal/application/provider"
	"github.com/IQVO/customer-payments/internal/domain/shared"
)

// skippable are the outcomes a policy tolerates per authorization: another
// writer got in, the row vanished or is no longer in a state the policy acts
// on. The next pass sees the authorization again if it still matters.
func skippable(err error) bool {
	return errors.Is(err, ports.ErrNotFound) || errors.Is(err, ports.ErrConcurrentUpdate) ||
		errors.Is(err, shared.ErrIllegalTransition) || errors.Is(err, provider.ErrUnavailable)
}

// ReauthorizeBeforeExpiry is the policy ReauthorizeBeforeExpiry (H2): a live
// authorization within ReauthLead of its expiresAt gets a fresh provider hold
// for its pending allocations (PaymentReauthorized). A provider decline moves
// it to EXPIRED and raises PaymentDeclined {reason: REAUTH_DECLINED}; a hold
// that already lapsed expires without asking the provider.
type ReauthorizeBeforeExpiry struct{ Deps }

// Execute handles one batch and returns how many authorizations it handled.
func (u ReauthorizeBeforeExpiry) Execute(ctx context.Context) (int, error) {
	ids, err := u.Authorizations.DueForReauthorization(ctx, u.now().Add(u.reauthLead()), batchSize)
	if err != nil {
		return 0, err
	}
	handled := 0
	for _, id := range ids {
		_, err := transact(ctx, u.UoW, commandAttempts, func(ctx context.Context) (struct{}, error) {
			p, err := u.Authorizations.Get(ctx, id)
			if err != nil {
				return struct{}{}, err
			}
			return struct{}{}, u.renew(ctx, p)
		})
		switch {
		case err == nil:
			handled++
		case skippable(err):
			u.logger().WarnContext(ctx, "re-authorization skipped", "authorization_id", string(id), "error", err)
		default:
			return handled, err
		}
	}
	return handled, nil
}

func (u ReauthorizeBeforeExpiry) renew(ctx context.Context, p *pa) error {
	if !p.Live() {
		return nil
	}
	now := u.now()
	if !now.Before(p.ExpiresAt()) {
		events, err := p.Expire(false, now)
		if err != nil {
			return err
		}
		return u.save(ctx, p, events)
	}
	key := Key("reauthorize", string(p.ID()), 0, itoa(p.Reauthorizations()+1))
	hold, err := u.Provider.Reauthorize(ctx, key, p.ProviderAuthorizationID(), p.PendingMinor())
	var events []shared.Event
	switch {
	case errors.Is(err, provider.ErrDeclined):
		events, err = p.Expire(true, now)
	case err == nil:
		events, err = p.Reauthorize(hold.ID, hold.ExpiresAt, now)
	}
	if err != nil {
		return err
	}
	return u.save(ctx, p, events)
}

// VoidUnboundAuthorizations is the policy VoidUnboundAuthorizations: an
// AUTHORIZED authorization that no CheckoutCompleted bound within
// UnboundVoidAfter is voided, the backstop of checkout's own give-up.
type VoidUnboundAuthorizations struct{ Deps }

// Execute handles one batch and returns how many authorizations it voided.
func (u VoidUnboundAuthorizations) Execute(ctx context.Context) (int, error) {
	ids, err := u.Authorizations.DueForVoid(ctx, u.now().Add(-u.unboundVoidAfter()), batchSize)
	if err != nil {
		return 0, err
	}
	handled := 0
	for _, id := range ids {
		_, err := transact(ctx, u.UoW, commandAttempts, func(ctx context.Context) (struct{}, error) {
			p, err := u.Authorizations.Get(ctx, id)
			if err != nil || p.OrderID() != "" {
				return struct{}{}, err
			}
			_, err = u.voidLoaded(ctx, p)
			return struct{}{}, err
		})
		switch {
		case err == nil:
			handled++
		case skippable(err):
			u.logger().WarnContext(ctx, "void of unbound authorization skipped", "authorization_id", string(id), "error", err)
		default:
			return handled, err
		}
	}
	return handled, nil
}

// MaintainParkedFacts is the housekeeping of ParkedFulfillmentFacts: facts
// still unbound after parked.EscalateAfter are dead-lettered WITH an alert,
// never dropped (S4); facts older than parked.Retention are purged (H10).
type MaintainParkedFacts struct{ Deps }

// Maintained is what one pass did.
type Maintained struct {
	Escalated int
	Purged    int
	// Parked is the gauge of H10: how many facts are parked after the pass.
	Parked int
}

// Execute runs one pass.
func (u MaintainParkedFacts) Execute(ctx context.Context) (Maintained, error) {
	var m Maintained
	now := u.now()
	facts, err := u.Parked.Unescalated(ctx, now.Add(-parked.EscalateAfter), batchSize)
	if err != nil {
		return m, err
	}
	for _, f := range facts {
		if err := u.escalate(ctx, f); err != nil {
			return m, err
		}
		m.Escalated++
	}
	if m.Purged, err = u.Parked.Purge(ctx, now.Add(-parked.Retention)); err != nil {
		return m, err
	}
	m.Parked, err = u.Parked.Count(ctx)
	return m, err
}

func (u MaintainParkedFacts) escalate(ctx context.Context, f parked.Fact) error {
	if err := u.Escalator.Escalate(ctx, f); err != nil {
		return err
	}
	u.logger().ErrorContext(ctx, "ALERT fact for an unbound order dead-lettered",
		"order_id", f.OrderID, "kind", string(f.Kind), "event_id", f.EventID)
	return u.Parked.MarkEscalated(ctx, f.EventID, u.now())
}
