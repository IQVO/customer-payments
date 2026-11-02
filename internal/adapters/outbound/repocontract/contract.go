// Package repocontract is the repository contract suite: the behaviours every
// persistence adapter (the in-memory doubles and Postgres) must show. It is
// test support (it imports "testing") kept out of _test.go files so both
// adapters can run it; the shipped binary never links it (enforced by
// internal/architecture).
package repocontract

import (
	"context"
	"testing"
	"time"

	"github.com/IQVO/customer-payments/internal/application/ports"
)

// Env is what an adapter hands to the suite. Every test gets a fresh Env.
type Env struct {
	Authorizations ports.AuthorizationRepository
	Refunds        ports.RefundRepository
	Parked         ports.ParkedFacts
	Inbox          ports.Inbox
	Outbox         ports.EventOutbox
	UoW            ports.UnitOfWork
	Idempotency    ports.IdempotencyStore
	// Clock is the store's time authority: testIdempotency stamps records
	// with it, so the expired-record case expires relative to the SAME clock
	// the implementation under test judges TTL with (the memory double is
	// driven by a frozen clock; the Postgres store by the database's now()).
	Clock ports.Clock
	// OutboxCount returns how many events the outbox holds.
	OutboxCount func(t *testing.T) int
}

// Run executes the whole suite; newEnv must return an empty Env each time.
func Run(t *testing.T, newEnv func(t *testing.T) Env) {
	t.Helper()
	suites := map[string]func(*testing.T, Env){
		"authorizations": testAuthorizations,
		"sessions":       testSessionUniqueness,
		"lookups":        testLookups,
		"due":            testDueQueries,
		"refunds":        testRefunds,
		"pages":          testPages,
		"parked":         testParked,
		"inbox":          testInbox,
		"unitofwork":     testUnitOfWork,
		"idempotency":    testIdempotency,
	}
	for name, fn := range suites {
		t.Run(name, func(t *testing.T) { fn(t, newEnv(t)) })
	}
}

var t0 = time.Date(2026, 10, 24, 9, 0, 0, 0, time.UTC)

// at returns t0 plus m minutes.
func at(m int) time.Time { return t0.Add(time.Duration(m) * time.Minute) }

func ctx() context.Context { return context.Background() }

func fatalIf(t *testing.T, err error, what string) {
	t.Helper()
	if err != nil {
		t.Fatalf("%s: %v", what, err)
	}
}
