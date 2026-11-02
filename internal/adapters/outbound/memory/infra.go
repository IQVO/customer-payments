package memory

import (
	"context"
	"net/http"
	"time"

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

// Do implements ports.UnitOfWork. A Do inside a Do joins it but is atomic on its
// own (the state taken before the inner fn is restored when only the inner fn
// fails), like the Postgres savepoint.
func (u *UnitOfWork) Do(ctx context.Context, fn func(ctx context.Context) error) (err error) {
	if ctx.Value(txKey{u.s}) != nil {
		before := u.s.capture()
		if err = fn(ctx); err != nil {
			u.s.restore(before)
		}
		return err
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

// IdempotencyStore is the in-memory ports.IdempotencyStore. Records older than
// idempotency.TTL are treated as absent.
type IdempotencyStore struct{ s *Store }

// Do implements ports.IdempotencyStore. Like a unit of work it holds the store
// while fn runs, so same-key requests are serialised and a failed or 5xx fn
// leaves no write behind.
func (i *IdempotencyStore) Do(ctx context.Context, key, requestHash string, at time.Time,
	fn func(ctx context.Context) (idempotency.Record, error),
) (idempotency.Record, bool, error) {
	i.s.mu.Lock()
	defer i.s.mu.Unlock()
	if old, ok := i.s.idempotency[key]; ok && i.s.now().Sub(old.CreatedAt) <= idempotency.TTL {
		return old, true, nil
	}
	before := i.s.capture()
	stored := false
	defer func() {
		if !stored {
			i.s.restore(before)
		}
	}()
	rec, err := fn(context.WithValue(ctx, txKey{i.s}, true))
	if err != nil {
		return idempotency.Record{}, false, err
	}
	if rec.Status >= http.StatusInternalServerError {
		return rec, false, nil
	}
	rec.RequestHash, rec.CreatedAt = requestHash, at
	i.s.idempotency[key] = rec
	stored = true
	return rec, false, nil
}
