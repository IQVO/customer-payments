//go:build integration

package postgres_test

import (
	"context"
	"testing"
	"time"

	"github.com/IQVO/customer-payments/internal/adapters/outbound/postgres"
	"github.com/IQVO/customer-payments/internal/adapters/outbound/repocontract"
	"github.com/IQVO/customer-payments/internal/application/outbox"
	"github.com/IQVO/customer-payments/internal/domain/shared"
	"github.com/IQVO/customer-payments/internal/testing/pgtest"
)

// stubEncoder stores each event as a tiny fixed message: the contract suite
// proves the repositories and the outbox rows, not the wire format.
type stubEncoder struct{ n int }

func (e *stubEncoder) Encode(events ...shared.Event) ([]outbox.Message, error) {
	out := make([]outbox.Message, 0, len(events))
	for _, ev := range events {
		e.n++
		out = append(out, outbox.Message{
			EventID: ev.SubjectID() + "-" + time.Duration(e.n).String(), Topic: "t", EventType: ev.EventName(),
			Subject: ev.SubjectID(), Key: []byte(ev.SubjectID()), DataSchema: "urn:x", Value: []byte("{}"),
		})
	}
	return out, nil
}

func TestPostgresHonoursTheRepositoryContract(t *testing.T) {
	repocontract.Run(t, func(t *testing.T) repocontract.Env {
		pool := pgtest.NewPool(t)
		return repocontract.Env{
			Authorizations: postgres.NewAuthorizationRepository(pool), Refunds: postgres.NewRefundRepository(pool),
			Parked: postgres.NewParkedFacts(pool), Inbox: postgres.NewInbox(pool),
			Outbox: postgres.NewOutboxRepo(pool, &stubEncoder{}), UoW: postgres.NewUnitOfWork(pool),
			Idempotency: postgres.NewIdempotencyStore(pool),
			// The store judges TTL with the database's now(); the contract
			// stamps records with this wall clock.
			Clock: wallClock{},
			OutboxCount: func(t *testing.T) int {
				var n int
				if err := pool.QueryRow(context.Background(), `SELECT count(*) FROM outbox_events`).Scan(&n); err != nil {
					t.Fatal(err)
				}
				return n
			},
		}
	})
}

// wallClock is the real time, for the Postgres side of the contract.
type wallClock struct{}

func (wallClock) Now() time.Time { return time.Now().UTC() }
