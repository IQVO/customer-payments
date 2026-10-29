package memory

import (
	"context"
	"fmt"
	"sort"
	"time"

	"github.com/IQVO/customer-payments/internal/application/parked"
	"github.com/IQVO/customer-payments/internal/application/ports"
	"github.com/IQVO/customer-payments/internal/domain/paymentauthorization"
	"github.com/IQVO/customer-payments/internal/domain/refund"
)

var (
	_ ports.RefundRepository = (*RefundRepository)(nil)
	_ ports.ParkedFacts      = (*ParkedFacts)(nil)
)

// RefundRepository is the in-memory ports.RefundRepository.
type RefundRepository struct{ s *Store }

// Get implements ports.RefundRepository.
func (r *RefundRepository) Get(ctx context.Context, id refund.ID) (*refund.Refund, error) {
	defer r.s.enter(ctx)()
	snap, ok := r.s.refunds[id]
	if !ok {
		return nil, fmt.Errorf("refund %s: %w", id, ports.ErrNotFound)
	}
	return refund.Restore(snap)
}

// Save implements ports.RefundRepository.
func (r *RefundRepository) Save(ctx context.Context, f *refund.Refund) error {
	defer r.s.enter(ctx)()
	stored, exists := r.s.refunds[f.ID()]
	if f.LoadedVersion() == 0 && exists || f.LoadedVersion() > 0 && (!exists || stored.Version != f.LoadedVersion()) {
		return fmt.Errorf("refund %s loaded at version %d: %w", f.ID(), f.LoadedVersion(), ports.ErrConcurrentUpdate)
	}
	r.s.refunds[f.ID()] = f.Snapshot()
	f.MarkPersisted()
	return nil
}

func (r *RefundRepository) sorted(match func(refund.Snapshot) bool) []refund.Snapshot {
	var out []refund.Snapshot
	for _, snap := range r.s.refunds {
		if match(snap) {
			out = append(out, snap)
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if !out[i].CreatedAt.Equal(out[j].CreatedAt) {
			return out[i].CreatedAt.Before(out[j].CreatedAt)
		}
		return out[i].Request.ID < out[j].Request.ID
	})
	return out
}

func (r *RefundRepository) list(match func(refund.Snapshot) bool) ([]*refund.Refund, error) {
	out := []*refund.Refund{}
	for _, snap := range r.sorted(match) {
		f, err := refund.Restore(snap)
		if err != nil {
			return nil, err
		}
		out = append(out, f)
	}
	return out, nil
}

// ListByOrder implements ports.RefundRepository.
func (r *RefundRepository) ListByOrder(ctx context.Context, orderID string) ([]*refund.Refund, error) {
	defer r.s.enter(ctx)()
	return r.list(func(s refund.Snapshot) bool { return s.Request.OrderID == orderID })
}

// ListByAuthorization implements ports.RefundRepository.
func (r *RefundRepository) ListByAuthorization(ctx context.Context, id paymentauthorization.ID) ([]*refund.Refund, error) {
	defer r.s.enter(ctx)()
	return r.list(func(s refund.Snapshot) bool { return s.Request.AuthorizationID == string(id) })
}

// DueRequested implements ports.RefundRepository.
func (r *RefundRepository) DueRequested(ctx context.Context, createdBy time.Time, limit int) ([]refund.ID, error) {
	defer r.s.enter(ctx)()
	var out []refund.ID
	for _, snap := range r.sorted(func(s refund.Snapshot) bool {
		return s.State == refund.StateRequested && !s.CreatedAt.After(createdBy)
	}) {
		if len(out) == limit {
			break
		}
		out = append(out, snap.Request.ID)
	}
	return out, nil
}

// ParkedFacts is the in-memory ports.ParkedFacts.
type ParkedFacts struct{ s *Store }

// Park implements ports.ParkedFacts.
func (p *ParkedFacts) Park(ctx context.Context, f parked.Fact) error {
	defer p.s.enter(ctx)()
	if _, seen := p.s.parked[f.EventID]; !seen {
		p.s.parked[f.EventID] = f
	}
	return nil
}

func (p *ParkedFacts) sorted(match func(parked.Fact) bool) []parked.Fact {
	var out []parked.Fact
	for _, f := range p.s.parked {
		if match(f) {
			out = append(out, f)
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if !out[i].ParkedAt.Equal(out[j].ParkedAt) {
			return out[i].ParkedAt.Before(out[j].ParkedAt)
		}
		return out[i].EventID < out[j].EventID
	})
	return out
}

// TakeForOrder implements ports.ParkedFacts.
func (p *ParkedFacts) TakeForOrder(ctx context.Context, orderID string) ([]parked.Fact, error) {
	defer p.s.enter(ctx)()
	out := p.sorted(func(f parked.Fact) bool { return f.OrderID == orderID })
	for _, f := range out {
		delete(p.s.parked, f.EventID)
	}
	return out, nil
}

// Count implements ports.ParkedFacts.
func (p *ParkedFacts) Count(ctx context.Context) (int, error) {
	defer p.s.enter(ctx)()
	return len(p.s.parked), nil
}

// Unescalated implements ports.ParkedFacts.
func (p *ParkedFacts) Unescalated(ctx context.Context, parkedBy time.Time, limit int) ([]parked.Fact, error) {
	defer p.s.enter(ctx)()
	out := p.sorted(func(f parked.Fact) bool { return f.EscalatedAt.IsZero() && !f.ParkedAt.After(parkedBy) })
	if len(out) > limit {
		out = out[:limit]
	}
	return out, nil
}

// MarkEscalated implements ports.ParkedFacts.
func (p *ParkedFacts) MarkEscalated(ctx context.Context, eventID string, at time.Time) error {
	defer p.s.enter(ctx)()
	f, ok := p.s.parked[eventID]
	if !ok {
		return fmt.Errorf("parked fact %s: %w", eventID, ports.ErrNotFound)
	}
	f.EscalatedAt = at
	p.s.parked[eventID] = f
	return nil
}

// Purge implements ports.ParkedFacts.
func (p *ParkedFacts) Purge(ctx context.Context, parkedBy time.Time) (int, error) {
	defer p.s.enter(ctx)()
	n := 0
	for id, f := range p.s.parked {
		if !f.ParkedAt.After(parkedBy) {
			delete(p.s.parked, id)
			n++
		}
	}
	return n, nil
}
