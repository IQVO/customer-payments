package memory

import (
	"context"
	"fmt"
	"sort"
	"time"

	"github.com/IQVO/customer-payments/internal/application/pagination"
	"github.com/IQVO/customer-payments/internal/application/ports"
	"github.com/IQVO/customer-payments/internal/domain/paymentauthorization"
)

var _ ports.AuthorizationRepository = (*AuthorizationRepository)(nil)

// AuthorizationRepository is the in-memory ports.AuthorizationRepository.
type AuthorizationRepository struct{ s *Store }

func (r *AuthorizationRepository) restore(snap paymentauthorization.Snapshot) (*paymentauthorization.PaymentAuthorization, error) {
	return paymentauthorization.Restore(snap)
}

// sorted returns the snapshots that match, oldest first (ties by id).
func (r *AuthorizationRepository) sorted(match func(paymentauthorization.Snapshot) bool) []paymentauthorization.Snapshot {
	var out []paymentauthorization.Snapshot
	for _, snap := range r.s.auths {
		if match(snap) {
			out = append(out, snap)
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if !out[i].CreatedAt.Equal(out[j].CreatedAt) {
			return out[i].CreatedAt.Before(out[j].CreatedAt)
		}
		return out[i].ID < out[j].ID
	})
	return out
}

func (r *AuthorizationRepository) first(what string, match func(paymentauthorization.Snapshot) bool) (*paymentauthorization.PaymentAuthorization, error) {
	found := r.sorted(match)
	if len(found) == 0 {
		return nil, fmt.Errorf("authorization %s: %w", what, ports.ErrNotFound)
	}
	return r.restore(found[0])
}

// Get implements ports.AuthorizationRepository.
func (r *AuthorizationRepository) Get(ctx context.Context, id paymentauthorization.ID) (*paymentauthorization.PaymentAuthorization, error) {
	defer r.s.enter(ctx)()
	snap, ok := r.s.auths[id]
	if !ok {
		return nil, fmt.Errorf("authorization %s: %w", id, ports.ErrNotFound)
	}
	return r.restore(snap)
}

// FindLiveBySession implements ports.AuthorizationRepository.
func (r *AuthorizationRepository) FindLiveBySession(ctx context.Context, sessionID string) (*paymentauthorization.PaymentAuthorization, error) {
	defer r.s.enter(ctx)()
	return r.first("of session "+sessionID, func(s paymentauthorization.Snapshot) bool {
		return s.CheckoutSessionID == sessionID && s.Terminal != paymentauthorization.StateDeclined
	})
}

// ListBySession implements ports.AuthorizationRepository.
func (r *AuthorizationRepository) ListBySession(ctx context.Context, sessionID string) ([]*paymentauthorization.PaymentAuthorization, error) {
	defer r.s.enter(ctx)()
	return r.restoreAll(r.sorted(func(s paymentauthorization.Snapshot) bool { return s.CheckoutSessionID == sessionID }))
}

// PageBySession implements ports.AuthorizationRepository: (created-at, id)
// ascending, strictly after the keyset, at most limit rows.
func (r *AuthorizationRepository) PageBySession(ctx context.Context, sessionID string, after *pagination.Keyset, limit int) ([]*paymentauthorization.PaymentAuthorization, error) {
	defer r.s.enter(ctx)()
	snaps := r.sorted(func(s paymentauthorization.Snapshot) bool {
		return s.CheckoutSessionID == sessionID && after.Admits(s.CreatedAt, string(s.ID))
	})
	if limit >= 0 && len(snaps) > limit {
		snaps = snaps[:limit]
	}
	return r.restoreAll(snaps)
}

func (r *AuthorizationRepository) restoreAll(snaps []paymentauthorization.Snapshot) ([]*paymentauthorization.PaymentAuthorization, error) {
	out := make([]*paymentauthorization.PaymentAuthorization, 0, len(snaps))
	for _, snap := range snaps {
		p, err := r.restore(snap)
		if err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, nil
}

// FindByOrder implements ports.AuthorizationRepository.
func (r *AuthorizationRepository) FindByOrder(ctx context.Context, orderID string) (*paymentauthorization.PaymentAuthorization, error) {
	defer r.s.enter(ctx)()
	return r.first("of order "+orderID, func(s paymentauthorization.Snapshot) bool { return s.OrderID == orderID })
}

// FindByTracking implements ports.AuthorizationRepository.
func (r *AuthorizationRepository) FindByTracking(ctx context.Context, tracking string) (*paymentauthorization.PaymentAuthorization, error) {
	defer r.s.enter(ctx)()
	return r.first("with tracking "+tracking, func(s paymentauthorization.Snapshot) bool {
		for _, a := range s.Allocations {
			for _, t := range a.TrackingNumbers {
				if t == tracking {
					return true
				}
			}
		}
		return false
	})
}

// Save implements ports.AuthorizationRepository.
func (r *AuthorizationRepository) Save(ctx context.Context, p *paymentauthorization.PaymentAuthorization) error {
	defer r.s.enter(ctx)()
	stored, exists := r.s.auths[p.ID()]
	if p.LoadedVersion() == 0 && exists || p.LoadedVersion() > 0 && (!exists || stored.Version != p.LoadedVersion()) {
		return fmt.Errorf("authorization %s loaded at version %d: %w", p.ID(), p.LoadedVersion(), ports.ErrConcurrentUpdate)
	}
	if p.LoadedVersion() == 0 && p.State() != paymentauthorization.StateDeclined {
		for _, o := range r.s.auths {
			if o.CheckoutSessionID == p.CheckoutSessionID() && o.Terminal != paymentauthorization.StateDeclined {
				return fmt.Errorf("session %s already has authorization %s: %w", o.CheckoutSessionID, o.ID, ports.ErrConcurrentUpdate)
			}
		}
	}
	r.s.auths[p.ID()] = p.Snapshot()
	p.MarkPersisted()
	return nil
}

func (r *AuthorizationRepository) ids(match func(paymentauthorization.Snapshot) bool, limit int) []paymentauthorization.ID {
	var out []paymentauthorization.ID
	for _, snap := range r.sorted(match) {
		if len(out) == limit {
			break
		}
		out = append(out, snap.ID)
	}
	return out
}

// DueForReauthorization implements ports.AuthorizationRepository.
func (r *AuthorizationRepository) DueForReauthorization(ctx context.Context, expiresBy time.Time, limit int) ([]paymentauthorization.ID, error) {
	defer r.s.enter(ctx)()
	return r.ids(func(s paymentauthorization.Snapshot) bool {
		p, err := r.restore(s)
		return err == nil && p.Live() && !s.ExpiresAt.After(expiresBy)
	}, limit), nil
}

// DueForVoid implements ports.AuthorizationRepository.
func (r *AuthorizationRepository) DueForVoid(ctx context.Context, createdBy time.Time, limit int) ([]paymentauthorization.ID, error) {
	defer r.s.enter(ctx)()
	return r.ids(func(s paymentauthorization.Snapshot) bool {
		p, err := r.restore(s)
		return err == nil && p.State() == paymentauthorization.StateAuthorized && s.OrderID == "" && !s.CreatedAt.After(createdBy)
	}, limit), nil
}
