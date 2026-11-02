package paymentprovider

import (
	"fmt"
	"sync"
	"time"

	"github.com/IQVO/customer-payments/internal/application/provider"
)

// Breaker defaults (portfolio rec 2026-11-02: circuit breaker on the provider
// ACL). Threshold consecutive transport failures open the breaker; Cooldown is
// how long it stays open before one half-open probe is allowed through.
const (
	DefaultBreakerThreshold = 5
	DefaultBreakerCooldown  = 30 * time.Second
)

// breaker is a small circuit breaker around the provider transport. States:
//
//   - closed: calls pass through; threshold consecutive transport failures
//     open the breaker.
//   - open: calls fail fast with provider.ErrUnavailable (the 503
//     payment-provider-unavailable problem) instead of piling onto a dead
//     provider, until the cooldown elapses.
//   - half-open: after the cooldown exactly one probe call passes; its
//     outcome closes the breaker (success) or restarts the cooldown
//     (failure). Further calls while the probe is in flight fail fast.
//
// Only transport failures (send or reading the answer) count: a provider that
// answers, even with a 5xx, proves it is alive, so HTTP status never trips
// the breaker.
type breaker struct {
	threshold int
	cooldown  time.Duration
	now       func() time.Time

	mu       sync.Mutex
	fails    int
	openedAt time.Time // zero while closed
}

func newBreaker(threshold int, cooldown time.Duration, now func() time.Time) *breaker {
	if now == nil {
		now = time.Now
	}
	return &breaker{threshold: threshold, cooldown: cooldown, now: now}
}

// allow reports whether a call may pass. In the open state it returns an
// error wrapping provider.ErrUnavailable, so it maps to the same 503 problem
// as an unreachable provider.
func (b *breaker) allow() error {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.openedAt.IsZero() {
		return nil
	}
	if b.now().Sub(b.openedAt) < b.cooldown {
		return fmt.Errorf("%w: circuit open since %s (retry after the %s cooldown)",
			provider.ErrUnavailable, b.openedAt.Format(time.RFC3339), b.cooldown)
	}
	return nil // half-open: let the probe through
}

// success records a call that reached the provider and closes the breaker.
func (b *breaker) success() {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.fails = 0
	b.openedAt = time.Time{}
}

// failure records one transport failure and opens the breaker once the
// consecutive failures reach the threshold (or immediately when a half-open
// probe fails, restarting the cooldown).
func (b *breaker) failure() {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.fails++
	if b.fails >= b.threshold {
		b.openedAt = b.now()
	}
}

// state is the breaker's state for tests and introspection.
func (b *breaker) state() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.openedAt.IsZero() {
		return "closed"
	}
	if b.now().Sub(b.openedAt) < b.cooldown {
		return "open"
	}
	return "half-open"
}
