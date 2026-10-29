// Package memory holds in-memory TEST DOUBLES of the application's out ports.
// They are for unit, BDD and contract tests only: cmd/** never imports this
// package (enforced by internal/architecture) because the service runs on real
// Postgres and Kafka.
//
// Every repository round-trips its aggregate through Snapshot and Restore, so
// a loaded aggregate never aliases stored state, a corrupt row can not become a
// live aggregate and the doubles keep the persistence contract of the real
// adapters: a miss is ports.ErrNotFound, a stale or duplicate save is
// ports.ErrConcurrentUpdate (also a second live authorization for one checkout
// session: the partial unique index of the Postgres schema).
//
// One Store backs every double. Its UnitOfWork is atomic: it holds the store
// for the whole unit and restores the state taken at the start when the unit
// fails or panics, outbox, inbox and read model included.
package memory

import (
	"context"
	"sync"
	"time"

	"github.com/IQVO/customer-payments/internal/application/idempotency"
	"github.com/IQVO/customer-payments/internal/application/parked"
	"github.com/IQVO/customer-payments/internal/application/ports"
	"github.com/IQVO/customer-payments/internal/domain/paymentauthorization"
	"github.com/IQVO/customer-payments/internal/domain/refund"
	"github.com/IQVO/customer-payments/internal/domain/shared"
)

type idemKey struct{ route, key string }

// Store is the shared state of the in-memory doubles.
type Store struct {
	mu          sync.Mutex
	auths       map[paymentauthorization.ID]paymentauthorization.Snapshot
	refunds     map[refund.ID]refund.Snapshot
	parked      map[string]parked.Fact
	inbox       map[string]string
	outbox      []shared.Event
	idempotency map[idemKey]idempotency.Record
	clock       ports.Clock
}

// NewStore returns an empty store. The clock is the authority the
// IdempotencyStore judges record TTL against: the doubles exist to test a
// domain whose clock is frozen, so TTL must be measured on that same clock,
// never on the wall.
func NewStore(clock ports.Clock) *Store {
	return &Store{
		auths: map[paymentauthorization.ID]paymentauthorization.Snapshot{}, refunds: map[refund.ID]refund.Snapshot{},
		parked: map[string]parked.Fact{}, inbox: map[string]string{},
		idempotency: map[idemKey]idempotency.Record{}, clock: clock,
	}
}

// now is the store's time: the injected clock.
func (s *Store) now() time.Time { return s.clock.Now() }

// state is a copy of everything a unit of work can change. Stored values are
// replaced, never edited in place, so copying the maps is enough.
type state struct {
	auths   map[paymentauthorization.ID]paymentauthorization.Snapshot
	refunds map[refund.ID]refund.Snapshot
	parked  map[string]parked.Fact
	inbox   map[string]string
	outbox  []shared.Event
}

func cloneMap[K comparable, V any](m map[K]V) map[K]V {
	out := make(map[K]V, len(m))
	for k, v := range m {
		out[k] = v
	}
	return out
}

func (s *Store) capture() state {
	return state{
		auths: cloneMap(s.auths), refunds: cloneMap(s.refunds), parked: cloneMap(s.parked), inbox: cloneMap(s.inbox),
		outbox: append([]shared.Event(nil), s.outbox...),
	}
}

func (s *Store) restore(st state) {
	s.auths, s.refunds, s.parked, s.inbox, s.outbox = st.auths, st.refunds, st.parked, st.inbox, st.outbox
}

// txKey marks a ctx that carries the open unit of work of one store.
type txKey struct{ store *Store }

// enter locks the store for one repository call, unless ctx already carries the
// unit of work that holds it. The returned func releases the lock.
func (s *Store) enter(ctx context.Context) func() {
	if ctx.Value(txKey{s}) != nil {
		return func() {}
	}
	s.mu.Lock()
	return s.mu.Unlock
}

// Authorizations returns the AuthorizationRepository double.
func (s *Store) Authorizations() *AuthorizationRepository { return &AuthorizationRepository{s} }

// Refunds returns the RefundRepository double.
func (s *Store) Refunds() *RefundRepository { return &RefundRepository{s} }

// Parked returns the ParkedFacts double.
func (s *Store) Parked() *ParkedFacts { return &ParkedFacts{s} }

// Inbox returns the Inbox double.
func (s *Store) Inbox() *Inbox { return &Inbox{s} }

// Outbox returns the EventOutbox double.
func (s *Store) Outbox() *Outbox { return &Outbox{s} }

// UnitOfWork returns the UnitOfWork double.
func (s *Store) UnitOfWork() *UnitOfWork { return &UnitOfWork{s} }

// Idempotency returns the IdempotencyStore double.
func (s *Store) Idempotency() *IdempotencyStore { return &IdempotencyStore{s} }

// Counts is how many rows each table of the store holds.
type Counts struct {
	Authorizations, Refunds, Parked, Inbox, OutboxEvents int
	// VersionSum is the sum of every aggregate version: any saved change moves
	// it.
	VersionSum int64
}

// Counts returns the size of every table and the sum of the aggregate
// versions, so a test can prove that a failed use case stored and changed
// nothing.
func (s *Store) Counts() Counts {
	s.mu.Lock()
	defer s.mu.Unlock()
	var versions int64
	for _, p := range s.auths {
		versions += p.Version
	}
	for _, r := range s.refunds {
		versions += r.Version
	}
	return Counts{
		Authorizations: len(s.auths), Refunds: len(s.refunds), Parked: len(s.parked), Inbox: len(s.inbox),
		OutboxEvents: len(s.outbox), VersionSum: versions,
	}
}

// Events returns a copy of the events appended to the outbox, oldest first.
func (s *Store) Events() []shared.Event {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]shared.Event(nil), s.outbox...)
}
