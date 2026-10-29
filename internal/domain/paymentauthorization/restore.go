package paymentauthorization

import (
	"errors"
	"fmt"
	"slices"
	"time"

	"github.com/IQVO/customer-payments/internal/domain/shared"
)

// Snapshot is the persisted form of a PaymentAuthorization. Repositories
// store it and Restore rebuilds the aggregate from it, validating every
// invariant.
type Snapshot struct {
	ID                      ID
	CheckoutSessionID       string
	OrderID                 string
	PaymentMethodToken      string
	Currency                string
	TotalMinor              int64
	Allocations             []Allocation
	Terminal                State
	DeclineReason           string
	ProviderAuthorizationID string
	ExpiresAt               time.Time
	CreatedAt               time.Time
	Reauthorizations        int
	Version                 int64
}

// Snapshot returns the persisted form of the aggregate.
func (p *PaymentAuthorization) Snapshot() Snapshot {
	return Snapshot{
		ID: p.id, CheckoutSessionID: p.sessionID, OrderID: p.orderID, PaymentMethodToken: p.token,
		Currency: p.currency, TotalMinor: p.total, Allocations: p.Allocations(), Terminal: p.terminal,
		DeclineReason: p.declineReason, ProviderAuthorizationID: p.providerAuthID, ExpiresAt: p.expiresAt,
		CreatedAt: p.createdAt, Reauthorizations: p.reauthorizedCount, Version: p.version,
	}
}

// Restore rebuilds a persisted authorization, validating every invariant that
// does not depend on the clock.
func Restore(s Snapshot) (*PaymentAuthorization, error) {
	if err := checkSnapshot(s); err != nil {
		return nil, shared.Corrupt("authorization %q: %v", s.ID, err)
	}
	allocs := slices.Clone(s.Allocations)
	for i := range allocs {
		allocs[i].TrackingNumbers = slices.Clone(allocs[i].TrackingNumbers)
	}
	return &PaymentAuthorization{
		id: s.ID, sessionID: s.CheckoutSessionID, orderID: s.OrderID, token: s.PaymentMethodToken,
		currency: s.Currency, total: s.TotalMinor, allocations: allocs, terminal: s.Terminal,
		declineReason: s.DeclineReason, providerAuthID: s.ProviderAuthorizationID,
		expiresAt: shared.UTC(s.ExpiresAt), createdAt: shared.UTC(s.CreatedAt),
		reauthorizedCount: s.Reauthorizations, version: s.Version, loaded: s.Version,
	}, nil
}

func checkSnapshot(s Snapshot) error {
	if !shared.ValidID(string(s.ID)) || !shared.ValidID(s.CheckoutSessionID) {
		return errors.New("authorizationId and checkoutSessionId must be valid ids")
	}
	if s.OrderID != "" && !shared.ValidID(s.OrderID) {
		return errors.New("orderId must be a valid id")
	}
	if s.Version < 1 {
		return errors.New("a version of at least 1 is required")
	}
	if err := checkTerminal(s); err != nil {
		return err
	}
	d := Draft{
		ID: s.ID, CheckoutSessionID: s.CheckoutSessionID, PaymentMethodToken: s.PaymentMethodToken,
		Currency: s.Currency, TotalMinor: s.TotalMinor,
	}
	for _, a := range s.Allocations {
		d.Allocations = append(d.Allocations, AllocationInput{GroupNo: a.GroupNo, AmountMinor: a.AmountMinor})
	}
	if err := Check(d); err != nil {
		return err
	}
	return checkAllocStates(s.Allocations)
}

func checkTerminal(s Snapshot) error {
	if s.Terminal != "" && s.Terminal != StateDeclined && s.Terminal != StateVoided && s.Terminal != StateExpired {
		return fmt.Errorf("terminal state %q is not VOIDED, EXPIRED or DECLINED", s.Terminal)
	}
	if s.Terminal == StateDeclined {
		if s.DeclineReason == "" {
			return errors.New("a declined authorization needs a reason")
		}
	} else if s.ProviderAuthorizationID == "" || s.ExpiresAt.IsZero() {
		return errors.New("an approved hold needs a provider reference and an expiry")
	}
	return nil
}

func checkAllocStates(allocs []Allocation) error {
	for i, a := range allocs {
		if i > 0 && allocs[i-1].GroupNo >= a.GroupNo {
			return errors.New("allocations must be ordered by group")
		}
		switch a.State {
		case AllocCaptured:
			if a.CapturedMinor < 1 || a.CapturedMinor > a.AmountMinor {
				return fmt.Errorf("group %d: captured %d against an allocation of %d", a.GroupNo, a.CapturedMinor, a.AmountMinor)
			}
		case AllocPending, AllocReleased:
			if a.CapturedMinor != 0 || len(a.TrackingNumbers) != 0 {
				return fmt.Errorf("group %d: a %s allocation carries a capture", a.GroupNo, a.State)
			}
		default:
			return fmt.Errorf("group %d: unknown allocation state %q", a.GroupNo, a.State)
		}
	}
	return nil
}
