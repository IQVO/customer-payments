package memory

import (
	"context"

	"github.com/IQVO/customer-payments/internal/application/idempotency"
	"github.com/IQVO/customer-payments/internal/application/ports"
	"github.com/IQVO/customer-payments/internal/domain/shared"
)

var (
	_ ports.Inbox            = (*Inbox)(nil)
	_ ports.EventOutbox      = (*Outbox)(nil)
	_ ports.UnitOfWork       = (*UnitOfWork)(nil)
	_ ports.IdempotencyStore = (*IdempotencyStore)(nil)
)

// Inbox is the in-memory ports.Inbox.
type Inbox struct{ s *Store }

// Claim implements ports.Inbox.
func (i *Inbox) Claim(ctx context.Context, eventID, eventType string) (bool, error) {
	defer i.s.enter(ctx)()
	if _, seen := i.s.inbox[eventID]; seen {
		return false, nil
	}
	i.s.inbox[eventID] = eventType
	return true, nil
}

// Outbox is the in-memory ports.EventOutbox: it keeps the appended events.
type Outbox struct{ s *Store }

// Append implements ports.EventOutbox.
func (o *Outbox) Append(ctx context.Context, events ...shared.Event) error {
	defer o.s.enter(ctx)()
	o.s.outbox = append(o.s.outbox, events...)
	return nil
}

// UnitOfWork is the in-memory ports.UnitOfWork: it holds the store for the
// whole unit and restores the state taken at the start when the unit fails or
// panics.
type UnitOfWork struct{ s *Store }

// Do implements ports.UnitOfWork.
func (u *UnitOfWork) Do(ctx context.Context, fn func(ctx context.Context) error) (err error) {
	if ctx.Value(txKey{u.s}) != nil {
		return fn(ctx)
	}
	u.s.mu.Lock()
	defer u.s.mu.Unlock()
	before := u.s.capture()
	committed := false
	defer func() {
		if !committed {
			u.s.restore(before)
		}
	}()
	if err = fn(context.WithValue(ctx, txKey{u.s}, true)); err != nil {
		return err
	}
	committed = true
	return nil
}

// IdempotencyStore is the in-memory ports.IdempotencyStore.
type IdempotencyStore struct{ s *Store }

func (i *IdempotencyStore) fresh(rec idempotency.Record) bool {
	return rec.CreatedAt.After(i.s.now().Add(-idempotency.TTL))
}

// Get implements ports.IdempotencyStore.
func (i *IdempotencyStore) Get(ctx context.Context, route, key string) (idempotency.Record, error) {
	defer i.s.enter(ctx)()
	rec, ok := i.s.idempotency[idemKey{route, key}]
	if !ok || !i.fresh(rec) {
		return idempotency.Record{}, ports.ErrNotFound
	}
	return rec, nil
}

// Put implements ports.IdempotencyStore: it stores the record unless a fresh one
// exists.
func (i *IdempotencyStore) Put(ctx context.Context, route, key string, rec idempotency.Record) (bool, error) {
	defer i.s.enter(ctx)()
	if old, ok := i.s.idempotency[idemKey{route, key}]; ok && i.fresh(old) {
		return false, nil
	}
	i.s.idempotency[idemKey{route, key}] = rec
	return true, nil
}
