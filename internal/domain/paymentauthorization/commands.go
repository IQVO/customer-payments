package paymentauthorization

import (
	"fmt"
	"slices"
	"time"

	"github.com/IQVO/customer-payments/internal/domain/shared"
)

func illegal(action string, s State) error {
	return fmt.Errorf("%w: cannot %s an authorization that is %s", shared.ErrIllegalTransition, action, s)
}

func (p *PaymentAuthorization) index(groupNo int) (int, error) {
	for i := range p.allocations {
		if p.allocations[i].GroupNo == groupNo {
			return i, nil
		}
	}
	return 0, fmt.Errorf("%w: group %d", ErrUnknownGroup, groupNo)
}

// BindOrder binds the order id on CheckoutCompleted (BindOrder). Binding the
// same order again changes nothing; another order is ErrOrderAlreadyBound.
// It reports whether anything changed.
func (p *PaymentAuthorization) BindOrder(orderID string) (bool, error) {
	if !shared.ValidID(orderID) {
		return false, shared.Invalid("orderId %q is not a valid id", orderID)
	}
	if p.orderID == orderID {
		return false, nil
	}
	if p.orderID != "" {
		return false, fmt.Errorf("%w: already bound to %s", ErrOrderAlreadyBound, p.orderID)
	}
	if p.terminal == StateDeclined {
		return false, illegal("bind an order to", p.terminal)
	}
	p.orderID = orderID
	p.version++
	return true, nil
}

// Capture takes a group's allocation when it ships (CaptureAllocation) and
// stores the tracking numbers (H6). Capturing a captured group again changes
// nothing and raises nothing.
func (p *PaymentAuthorization) Capture(groupNo int, amountMinor int64, trackings []string, captureID string, now time.Time) ([]shared.Event, error) {
	i, err := p.index(groupNo)
	if err != nil {
		return nil, err
	}
	a := &p.allocations[i]
	if a.State == AllocCaptured {
		return nil, nil
	}
	if p.terminal == StateDeclined || p.terminal == StateVoided {
		return nil, illegal("capture an allocation of", p.terminal)
	}
	if p.terminal == StateExpired || !shared.UTC(now).Before(p.expiresAt) {
		return nil, fmt.Errorf("%w: the hold lapsed at %s", ErrCaptureAfterExpiry, p.expiresAt.Format(time.RFC3339))
	}
	if a.State == AllocReleased {
		return nil, illegal("capture a released allocation of", p.State())
	}
	if amountMinor < 1 || amountMinor > a.AmountMinor {
		return nil, fmt.Errorf("%w: %d against an allocation of %d", ErrOverCapture, amountMinor, a.AmountMinor)
	}
	a.State, a.CapturedMinor, a.ProviderCaptureID = AllocCaptured, amountMinor, captureID
	a.TrackingNumbers = slices.Clone(trackings)
	p.version++
	return []shared.Event{PaymentCaptured{
		Header: p.header(now), AuthorizationID: p.id, OrderID: p.orderID, GroupNo: groupNo, AmountMinor: amountMinor,
	}}, nil
}

// Release lets a pending allocation go (ReleaseAllocation); the reason is
// ReasonCancelled or ReasonUnsourceable. Releasing a released group changes
// nothing; a captured group cannot be released.
func (p *PaymentAuthorization) Release(groupNo int, reason string, now time.Time) ([]shared.Event, error) {
	if reason != ReasonCancelled && reason != ReasonUnsourceable {
		return nil, shared.Invalid("release reason %q is not CANCELLED or UNSOURCEABLE", reason)
	}
	i, err := p.index(groupNo)
	if err != nil {
		return nil, err
	}
	switch a := &p.allocations[i]; {
	case a.State == AllocReleased:
		return nil, nil
	case a.State == AllocCaptured:
		return nil, illegal("release a captured allocation of", p.State())
	case p.terminal != "":
		return nil, illegal("release an allocation of", p.terminal)
	}
	return p.releaseAt(i, reason, now), nil
}

func (p *PaymentAuthorization) releaseAt(i int, reason string, now time.Time) []shared.Event {
	a := &p.allocations[i]
	a.State = AllocReleased
	p.version++
	return []shared.Event{PaymentReleased{
		Header: p.header(now), AuthorizationID: p.id, OrderID: p.orderID, GroupNo: a.GroupNo, AmountMinor: a.AmountMinor, Reason: reason,
	}}
}

func (p *PaymentAuthorization) releasePending(reason string, now time.Time) []shared.Event {
	var events []shared.Event
	for i := range p.allocations {
		if p.allocations[i].State == AllocPending {
			events = append(events, p.releaseAt(i, reason, now)...)
		}
	}
	return events
}

// Void cancels the whole authorization before any capture (VoidAuthorization):
// every pending allocation is released with reason VOIDED (H1).
func (p *PaymentAuthorization) Void(now time.Time) ([]shared.Event, error) {
	if p.State() != StateAuthorized {
		return nil, illegal("void", p.State())
	}
	events := p.releasePending(ReasonVoided, now)
	p.terminal = StateVoided
	return events, nil
}

// Expire ends a hold that lapsed with money still uncaptured
// (ExpireAuthorization). A declined re-authorization (H2) also raises
// PaymentDeclined with reason REAUTH_DECLINED.
func (p *PaymentAuthorization) Expire(reauthDeclined bool, now time.Time) ([]shared.Event, error) {
	if !p.Live() {
		return nil, illegal("expire", p.State())
	}
	events := p.releasePending(ReasonExpired, now)
	p.terminal = StateExpired
	p.version++
	events = append(events, PaymentAuthorizationExpired{
		Header: p.header(now), AuthorizationID: p.id, OrderID: p.orderID, ExpiredAt: shared.UTC(now),
	})
	if reauthDeclined {
		events = append(events, PaymentDeclined{
			Header: p.header(now), AuthorizationID: p.id, CheckoutSessionID: p.sessionID, Reason: ReasonReauthDeclined,
		})
	}
	return events, nil
}

// Reauthorize renews the hold for the pending allocations
// (ReauthorizePayment, AUTHORIZED to AUTHORIZED): the new provider reference
// and a later expiry replace the old ones (H1).
func (p *PaymentAuthorization) Reauthorize(providerAuthID string, expiresAt, now time.Time) ([]shared.Event, error) {
	if !p.Live() {
		return nil, illegal("re-authorize", p.State())
	}
	expiresAt = shared.UTC(expiresAt)
	if providerAuthID == "" || !expiresAt.After(p.expiresAt) {
		return nil, shared.Invalid("a re-authorization needs a provider reference and an expiry after %s", p.expiresAt.Format(time.RFC3339))
	}
	p.providerAuthID, p.expiresAt = providerAuthID, expiresAt
	p.reauthorizedCount++
	p.version++
	return []shared.Event{PaymentReauthorized{
		Header: p.header(now), AuthorizationID: p.id, OrderID: p.orderID, ExpiresAt: expiresAt, ProviderAuthorizationID: providerAuthID,
	}}, nil
}
