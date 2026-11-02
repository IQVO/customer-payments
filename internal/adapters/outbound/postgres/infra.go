package postgres

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/IQVO/customer-payments/internal/application/ports"
)

// Inbox is the Postgres ports.Inbox over processed_events.
type Inbox struct{ pool *pgxpool.Pool }

var _ ports.Inbox = (*Inbox)(nil)

// NewInbox constructs an Inbox over pool.
func NewInbox(pool *pgxpool.Pool) *Inbox { return &Inbox{pool: pool} }

// Claim implements ports.Inbox. Called with a unit-of-work ctx the claim rolls
// back with the unit.
func (i *Inbox) Claim(ctx context.Context, eventID, eventType string) (bool, error) {
	first := false
	err := inTx(ctx, i.pool, func(q querier) error {
		tag, err := q.Exec(ctx, `INSERT INTO processed_events (event_id, event_type) VALUES ($1, $2) ON CONFLICT (event_id) DO NOTHING`, eventID, eventType)
		first = err == nil && tag.RowsAffected() == 1
		return err
	})
	if err != nil {
		return false, fmt.Errorf("claim event %s: %w", eventID, err)
	}
	return first, nil
}
