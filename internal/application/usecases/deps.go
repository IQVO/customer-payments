// Package usecases holds the application's use cases: one per operation of
// apis/openapi.yaml, one per consumed event and one per clock-driven policy.
// Every command follows one shape inside ONE ports.UnitOfWork: load the
// aggregate, let the domain decide, call the payment provider with a
// deterministic key, save the aggregate and append the raised events to the
// outbox, then return a view. Domain, shared and port errors pass through
// unchanged (errors.Is keeps working): the HTTP adapter maps them to problem
// types.
package usecases

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strconv"
	"time"

	"github.com/IQVO/customer-payments/internal/application/ports"
	"github.com/IQVO/customer-payments/internal/domain/shared"
)

// Deps bundles the ports and the policy values the use cases need.
type Deps struct {
	Authorizations ports.AuthorizationRepository
	Refunds        ports.RefundRepository
	Parked         ports.ParkedFacts
	Inbox          ports.Inbox
	Outbox         ports.EventOutbox
	UoW            ports.UnitOfWork
	Clock          ports.Clock
	IDs            ports.IDGenerator
	Provider       ports.PaymentProvider
	Escalator      ports.Escalator

	// ReauthLead is how long before expiresAt the hold is renewed (default
	// DefaultReauthLead, 24 h).
	ReauthLead time.Duration
	// UnboundVoidAfter is how long an authorization may wait for its
	// CheckoutCompleted (default DefaultUnboundVoidAfter, 24 h).
	UnboundVoidAfter time.Duration
	// Log receives the events a consumer skipped and the operator alerts; nil
	// discards them.
	Log *slog.Logger
	// Metrics records the business outcome of each authorization attempt; nil
	// records nothing, so unit tests need no wiring.
	Metrics ports.PaymentMetrics
}

const (
	// commandAttempts is how often a command runs when another writer got in
	// between its load and its save.
	commandAttempts = 2
	// consumerAttempts is the same for consumed events.
	consumerAttempts = 5
	// batchSize bounds one pass of a clock-driven policy.
	batchSize = 200
)

// The policy defaults (H2, VoidUnboundAuthorizations).
const (
	DefaultReauthLead       = 24 * time.Hour
	DefaultUnboundVoidAfter = 24 * time.Hour
)

func (d Deps) now() time.Time { return d.Clock.Now().UTC() }

func (d Deps) logger() *slog.Logger {
	if d.Log != nil {
		return d.Log
	}
	return slog.New(slog.DiscardHandler)
}

// metrics returns the configured PaymentMetrics or a no-op.
func (d Deps) metrics() ports.PaymentMetrics {
	if d.Metrics != nil {
		return d.Metrics
	}
	return noMetrics{}
}

// noMetrics is the PaymentMetrics of a Deps without one.
type noMetrics struct{}

func (noMetrics) AuthorizationAuthorized(context.Context)       {}
func (noMetrics) AuthorizationDeclined(context.Context, string) {}
func (noMetrics) AuthorizationFailed(context.Context, string)   {}

func (d Deps) reauthLead() time.Duration {
	if d.ReauthLead > 0 {
		return d.ReauthLead
	}
	return DefaultReauthLead
}

func (d Deps) unboundVoidAfter() time.Duration {
	if d.UnboundVoidAfter > 0 {
		return d.UnboundVoidAfter
	}
	return DefaultUnboundVoidAfter
}

func (d Deps) publish(ctx context.Context, events []shared.Event) error {
	if err := d.Outbox.Append(ctx, events...); err != nil {
		return fmt.Errorf("append events to outbox: %w", err)
	}
	return nil
}

// Key builds the deterministic provider idempotency key of an operation on an
// authorization's group (0 for the whole authorization). The qualifier
// separates repeated operations of the same kind (a refund id, a
// re-authorization count, an attempt number).
func Key(operation, authorizationID string, groupNo int, qualifier string) string {
	k := operation + ":" + authorizationID + ":" + strconv.Itoa(groupNo)
	if qualifier != "" {
		k += ":" + qualifier
	}
	return k
}

// transact runs attempt in a unit of work. A ports.ErrConcurrentUpdate means
// another writer got in between the load and the save, so the whole attempt
// (load, domain call, provider call, save, outbox) runs again on fresh state,
// up to attempts times, before the conflict is reported. Provider calls are
// repeatable because their keys are deterministic.
func transact[T any](ctx context.Context, uow ports.UnitOfWork, attempts int, attempt func(ctx context.Context) (T, error)) (T, error) {
	var out T
	run := func(ctx context.Context) error {
		v, err := attempt(ctx)
		if err != nil {
			return err
		}
		out = v
		return nil
	}
	var err error
	for i := 0; i < attempts; i++ {
		if err = uow.Do(ctx, run); !errors.Is(err, ports.ErrConcurrentUpdate) {
			break
		}
	}
	if err != nil {
		var zero T
		return zero, err
	}
	return out, nil
}

func itoa(n int) string { return strconv.Itoa(n) }
