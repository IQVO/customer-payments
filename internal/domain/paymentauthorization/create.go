package paymentauthorization

import (
	"fmt"
	"slices"
	"time"

	"github.com/IQVO/customer-payments/internal/domain/shared"
)

// Outcome is what the payment provider answered to the authorize call.
type Outcome struct {
	// DeclineReason is non-empty when the provider declined the hold.
	DeclineReason string
	// ProviderAuthorizationID and ExpiresAt describe an approved hold.
	ProviderAuthorizationID string
	ExpiresAt               time.Time
}

// Check validates a draft without creating anything: the use case calls it
// before it reaches the provider, so a bad request never costs a hold.
func Check(d Draft) error {
	if !shared.ValidID(string(d.ID)) {
		return shared.Invalid("authorizationId %q is not a valid id", d.ID)
	}
	if !shared.ValidID(d.CheckoutSessionID) {
		return shared.Invalid("checkoutSessionId %q is not a valid id", d.CheckoutSessionID)
	}
	if d.PaymentMethodToken == "" || len(d.PaymentMethodToken) > MaxTokenLen {
		return shared.Invalid("paymentMethodToken must be 1 to %d characters", MaxTokenLen)
	}
	if !shared.ValidCurrency(d.Currency) {
		return shared.Invalid("currency %q is not an ISO 4217 code", d.Currency)
	}
	if err := shared.CheckAmount(d.TotalMinor); err != nil {
		return err
	}
	return checkAllocations(d)
}

func checkAllocations(d Draft) error {
	if len(d.Allocations) == 0 || len(d.Allocations) > MaxGroups {
		return shared.Invalid("between 1 and %d allocations are required", MaxGroups)
	}
	var sum int64
	seen := map[int]bool{}
	for _, a := range d.Allocations {
		if a.GroupNo < 1 || a.GroupNo > shared.MaxInt32 {
			return shared.Invalid("groupNo %d must be between 1 and %d", a.GroupNo, shared.MaxInt32)
		}
		if err := shared.CheckAmount(a.AmountMinor); err != nil {
			return err
		}
		if seen[a.GroupNo] {
			return fmt.Errorf("%w: group %d appears twice", ErrAllocationMismatch, a.GroupNo)
		}
		seen[a.GroupNo] = true
		sum += a.AmountMinor
	}
	if sum != d.TotalMinor {
		return fmt.Errorf("%w: allocations sum to %d, the total is %d", ErrAllocationMismatch, sum, d.TotalMinor)
	}
	return nil
}

// New records the outcome of the authorize call: an AUTHORIZED hold that
// raises PaymentAuthorized, or a DECLINED record that raises PaymentDeclined
// (H7).
func New(d Draft, out Outcome, now time.Time) (*PaymentAuthorization, []shared.Event, error) {
	if err := Check(d); err != nil {
		return nil, nil, err
	}
	now = shared.UTC(now)
	p := &PaymentAuthorization{
		id: d.ID, sessionID: d.CheckoutSessionID, token: d.PaymentMethodToken,
		currency: d.Currency, total: d.TotalMinor, createdAt: now, version: 1,
	}
	for _, a := range d.Allocations {
		p.allocations = append(p.allocations, Allocation{GroupNo: a.GroupNo, AmountMinor: a.AmountMinor, State: AllocPending})
	}
	slices.SortFunc(p.allocations, func(a, b Allocation) int { return a.GroupNo - b.GroupNo })
	if out.DeclineReason != "" {
		p.terminal, p.declineReason = StateDeclined, out.DeclineReason
		return p, []shared.Event{p.declined(out.DeclineReason, now)}, nil
	}
	if out.ProviderAuthorizationID == "" || !out.ExpiresAt.After(now) {
		return nil, nil, shared.Invalid("an approved hold needs a provider reference and a future expiry")
	}
	p.providerAuthID, p.expiresAt = out.ProviderAuthorizationID, shared.UTC(out.ExpiresAt)
	return p, []shared.Event{p.authorized(now)}, nil
}

// State returns the derived state (H3), or the terminal outcome.
func (p *PaymentAuthorization) State() State {
	if p.terminal != "" {
		return p.terminal
	}
	var pending, captured int
	for _, a := range p.allocations {
		switch a.State {
		case AllocPending:
			pending++
		case AllocCaptured:
			captured++
		case AllocReleased:
		}
	}
	switch {
	case captured > 0 && pending > 0:
		return StatePartiallyCaptured
	case captured > 0:
		return StateCaptured
	case pending > 0:
		return StateAuthorized
	}
	return StateReleased
}

// Live reports whether uncaptured money is still held: the state is
// AUTHORIZED or PARTIALLY_CAPTURED.
func (p *PaymentAuthorization) Live() bool {
	s := p.State()
	return s == StateAuthorized || s == StatePartiallyCaptured
}

// PendingMinor returns the sum of the PENDING allocations.
func (p *PaymentAuthorization) PendingMinor() int64 {
	var n int64
	for _, a := range p.allocations {
		if a.State == AllocPending {
			n += a.AmountMinor
		}
	}
	return n
}

// CapturedMinor returns the sum of the captured amounts.
func (p *PaymentAuthorization) CapturedMinor() int64 {
	var n int64
	for _, a := range p.allocations {
		n += a.CapturedMinor
	}
	return n
}

// Allocation returns the allocation of a group.
func (p *PaymentAuthorization) Allocation(groupNo int) (Allocation, bool) {
	for _, a := range p.Allocations() {
		if a.GroupNo == groupNo {
			return a, true
		}
	}
	return Allocation{}, false
}

// GroupOfTracking returns the group whose capture carried the tracking
// number.
func (p *PaymentAuthorization) GroupOfTracking(tracking string) (int, bool) {
	for _, a := range p.allocations {
		if slices.Contains(a.TrackingNumbers, tracking) {
			return a.GroupNo, true
		}
	}
	return 0, false
}
