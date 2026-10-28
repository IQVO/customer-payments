// Package ports declares the application's OUT ports: interfaces only
// (enforced by internal/architecture). Their non-interface vocabulary is the
// sentinels of errors.go and the data packages idempotency and parked.
//
// Optimistic concurrency: an authorization and a refund are saved under the
// rule "stored version == LoadedVersion()" (several commands may run between a
// load and a save); a create (LoadedVersion 0) expects that the id is not
// stored, and for an authorization that no other live (not DECLINED)
// authorization exists for its checkout session. Any mismatch, including an
// update of a row that no longer exists, is ErrConcurrentUpdate. After a
// successful Save the adapter calls MarkPersisted(). Inside a UnitOfWork every
// repository joins the transaction carried by the ctx the unit handed to its
// function.
package ports

import (
	"context"
	"time"

	"github.com/IQVO/customer-payments/internal/application/idempotency"
	"github.com/IQVO/customer-payments/internal/application/parked"
	"github.com/IQVO/customer-payments/internal/domain/paymentauthorization"
	"github.com/IQVO/customer-payments/internal/domain/refund"
	"github.com/IQVO/customer-payments/internal/domain/shared"
)

// AuthorizationRepository persists PaymentAuthorization aggregates.
type AuthorizationRepository interface {
	// Get returns the authorization, or ErrNotFound.
	Get(ctx context.Context, id paymentauthorization.ID) (*paymentauthorization.PaymentAuthorization, error)
	// FindLiveBySession returns the one authorization of the session that is
	// not DECLINED, or ErrNotFound.
	FindLiveBySession(ctx context.Context, sessionID string) (*paymentauthorization.PaymentAuthorization, error)
	// ListBySession returns every authorization of the session, oldest first
	// (declined attempts included).
	ListBySession(ctx context.Context, sessionID string) ([]*paymentauthorization.PaymentAuthorization, error)
	// FindByOrder returns the authorization bound to the order, or ErrNotFound.
	FindByOrder(ctx context.Context, orderID string) (*paymentauthorization.PaymentAuthorization, error)
	// FindByTracking returns the authorization one of whose allocations
	// carries the tracking number, or ErrNotFound.
	FindByTracking(ctx context.Context, tracking string) (*paymentauthorization.PaymentAuthorization, error)
	// Save persists the authorization under the concurrency rule of the
	// package comment.
	Save(ctx context.Context, p *paymentauthorization.PaymentAuthorization) error
	// DueForReauthorization returns the ids of up to limit live authorizations
	// whose expiresAt is at or before the instant, earliest first.
	DueForReauthorization(ctx context.Context, expiresBy time.Time, limit int) ([]paymentauthorization.ID, error)
	// DueForVoid returns the ids of up to limit AUTHORIZED, unbound
	// authorizations created at or before the instant, earliest first.
	DueForVoid(ctx context.Context, createdBy time.Time, limit int) ([]paymentauthorization.ID, error)
}

// RefundRepository persists Refund aggregates.
type RefundRepository interface {
	// Get returns the refund, or ErrNotFound.
	Get(ctx context.Context, id refund.ID) (*refund.Refund, error)
	// Save persists the refund under the concurrency rule of the package
	// comment.
	Save(ctx context.Context, r *refund.Refund) error
	// ListByOrder returns the refunds of the order, oldest first.
	ListByOrder(ctx context.Context, orderID string) ([]*refund.Refund, error)
	// ListByAuthorization returns the refunds made through the authorization,
	// oldest first.
	ListByAuthorization(ctx context.Context, id paymentauthorization.ID) ([]*refund.Refund, error)
	// DueRequested returns the ids of up to limit REQUESTED refunds created at
	// or before the instant, earliest first.
	DueRequested(ctx context.Context, createdBy time.Time, limit int) ([]refund.ID, error)
}

// ParkedFacts is the read model ParkedFulfillmentFacts.
type ParkedFacts interface {
	// Park stores the fact; a fact whose EventID is stored already is kept
	// once.
	Park(ctx context.Context, f parked.Fact) error
	// TakeForOrder removes and returns the parked facts of the order, oldest
	// first.
	TakeForOrder(ctx context.Context, orderID string) ([]parked.Fact, error)
	// Count returns how many facts are parked (the gauge of H10).
	Count(ctx context.Context) (int, error)
	// Unescalated returns up to limit facts parked at or before the instant
	// that were not escalated yet, oldest first.
	Unescalated(ctx context.Context, parkedBy time.Time, limit int) ([]parked.Fact, error)
	// MarkEscalated records that the fact was dead-lettered.
	MarkEscalated(ctx context.Context, eventID string, at time.Time) error
	// Purge deletes the facts parked at or before the instant and returns how
	// many it deleted.
	Purge(ctx context.Context, parkedBy time.Time) (int, error)
}

// Inbox makes consumers idempotent on the CloudEvents id.
type Inbox interface {
	// Claim records the event id and reports whether this is the first time
	// it is seen. Called with the ctx of a unit of work, the claim rolls back
	// with it.
	Claim(ctx context.Context, eventID, eventType string) (first bool, err error)
}

// EventOutbox is the write side of the transactional outbox. Called with the
// ctx handed to UnitOfWork.Do it joins the SAME transaction as the aggregate
// Save calls. Appending no events is a no-op.
type EventOutbox interface {
	Append(ctx context.Context, events ...shared.Event) error
}

// UnitOfWork runs fn as ONE atomic unit: every repository, inbox and outbox
// call fn makes with the ctx it is handed commits together when fn returns
// nil, or rolls back together when fn returns an error (or panics). Do is
// re-entrant; the error fn returns is returned unchanged.
type UnitOfWork interface {
	Do(ctx context.Context, fn func(ctx context.Context) error) error
}

// IdempotencyStore remembers the first response per (route, key) for 24 hours.
type IdempotencyStore interface {
	// Get returns the stored record, or ErrNotFound (expired records are
	// treated as absent).
	Get(ctx context.Context, route, key string) (idempotency.Record, error)
	// Put stores the record unless one exists; it reports whether it stored.
	Put(ctx context.Context, route, key string, rec idempotency.Record) (bool, error)
}

// Clock is the service's notion of "now"; use cases stamp aggregates with it.
type Clock interface {
	Now() time.Time
}

// IDGenerator mints the ids of new aggregates.
type IDGenerator interface {
	// NewAuthorizationID mints an authorizationId.
	NewAuthorizationID() paymentauthorization.ID
	// NewRefundID mints a refundId.
	NewRefundID() refund.ID
}

// Escalator dead-letters a parked fact whose order never got bound, with an
// alert (S4). It never drops the fact silently.
type Escalator interface {
	Escalate(ctx context.Context, f parked.Fact) error
}

// PaymentMetrics records the business outcome of every authorization attempt
// (Tier 2 of the fleet metrics convention). AuthorizePayment calls exactly one
// method per attempt that reached a decision: AuthorizationAuthorized when the
// hold was placed, AuthorizationDeclined when the provider refused it (reason is
// the provider decline code), AuthorizationFailed for any other failure (reason
// is a stable slug such as payment-provider-unavailable). A replay that returns
// the session's existing authorization is not an attempt. The implementation
// lives in internal/adapters/outbound/telemetry; a nil Deps.Metrics records
// nothing.
type PaymentMetrics interface {
	AuthorizationAuthorized(ctx context.Context)
	AuthorizationDeclined(ctx context.Context, reason string)
	AuthorizationFailed(ctx context.Context, reason string)
}
