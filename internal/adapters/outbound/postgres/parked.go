package postgres

import (
	"context"
	"fmt"
	"sort"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/IQVO/customer-payments/internal/application/parked"
	"github.com/IQVO/customer-payments/internal/application/ports"
)

// ParkedFacts is the Postgres ports.ParkedFacts over parked_facts.
type ParkedFacts struct{ pool *pgxpool.Pool }

var _ ports.ParkedFacts = (*ParkedFacts)(nil)

// NewParkedFacts constructs a ParkedFacts over pool.
func NewParkedFacts(pool *pgxpool.Pool) *ParkedFacts { return &ParkedFacts{pool: pool} }

const parkedColumns = `event_id, kind, order_id, group_no, tracking_numbers, occurred_at, parked_at, escalated_at`

// Park implements ports.ParkedFacts.
func (p *ParkedFacts) Park(ctx context.Context, f parked.Fact) error {
	tracking := f.TrackingNumbers
	if tracking == nil {
		tracking = []string{}
	}
	return inTx(ctx, p.pool, func(q querier) error {
		_, err := q.Exec(ctx, `
			INSERT INTO parked_facts (`+parkedColumns+`) VALUES ($1, $2, $3, $4, $5, $6, $7, $8)
			ON CONFLICT (event_id) DO NOTHING`,
			f.EventID, string(f.Kind), f.OrderID, f.GroupNo, tracking, f.OccurredAt.UTC(), f.ParkedAt.UTC(), nullTime(f.EscalatedAt))
		return err
	})
}

func scanFacts(ctx context.Context, q querier, query string, args ...any) ([]parked.Fact, error) {
	rows, err := q.Query(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("read parked facts: %w", err)
	}
	defer rows.Close()
	var out []parked.Fact
	for rows.Next() {
		var f parked.Fact
		var kind string
		var occurred, at time.Time
		var esc *time.Time
		if err := rows.Scan(&f.EventID, &kind, &f.OrderID, &f.GroupNo, &f.TrackingNumbers, &occurred, &at, &esc); err != nil {
			return nil, fmt.Errorf("scan parked fact: %w", err)
		}
		f.Kind, f.OccurredAt, f.ParkedAt, f.EscalatedAt = parked.Kind(kind), occurred.UTC(), at.UTC(), timeOrZero(esc)
		if len(f.TrackingNumbers) == 0 {
			f.TrackingNumbers = nil
		}
		out = append(out, f)
	}
	return out, rows.Err()
}

// TakeForOrder implements ports.ParkedFacts: one DELETE ... RETURNING.
func (p *ParkedFacts) TakeForOrder(ctx context.Context, orderID string) ([]parked.Fact, error) {
	var out []parked.Fact
	err := inTx(ctx, p.pool, func(q querier) error {
		facts, err := scanFacts(ctx, q, `DELETE FROM parked_facts WHERE order_id = $1 RETURNING `+parkedColumns, orderID)
		out = facts
		return err
	})
	sortFacts(out)
	return out, err
}

// Count implements ports.ParkedFacts.
func (p *ParkedFacts) Count(ctx context.Context) (int, error) {
	var n int
	if err := queryFor(ctx, p.pool).QueryRow(ctx, `SELECT count(*) FROM parked_facts`).Scan(&n); err != nil {
		return 0, fmt.Errorf("count parked facts: %w", err)
	}
	return n, nil
}

// Unescalated implements ports.ParkedFacts.
func (p *ParkedFacts) Unescalated(ctx context.Context, parkedBy time.Time, limit int) ([]parked.Fact, error) {
	return scanFacts(ctx, queryFor(ctx, p.pool), `
		SELECT `+parkedColumns+` FROM parked_facts WHERE escalated_at IS NULL AND parked_at <= $1
		ORDER BY parked_at, event_id LIMIT $2`, parkedBy.UTC(), limit)
}

// MarkEscalated implements ports.ParkedFacts.
func (p *ParkedFacts) MarkEscalated(ctx context.Context, eventID string, at time.Time) error {
	return inTx(ctx, p.pool, func(q querier) error {
		tag, err := q.Exec(ctx, `UPDATE parked_facts SET escalated_at = $2 WHERE event_id = $1`, eventID, at.UTC())
		if err != nil {
			return err
		}
		if tag.RowsAffected() != 1 {
			return fmt.Errorf("parked fact %s: %w", eventID, ports.ErrNotFound)
		}
		return nil
	})
}

// Purge implements ports.ParkedFacts.
func (p *ParkedFacts) Purge(ctx context.Context, parkedBy time.Time) (int, error) {
	n := 0
	err := inTx(ctx, p.pool, func(q querier) error {
		tag, err := q.Exec(ctx, `DELETE FROM parked_facts WHERE parked_at <= $1`, parkedBy.UTC())
		n = int(tag.RowsAffected())
		return err
	})
	return n, err
}

func sortFacts(f []parked.Fact) {
	sort.Slice(f, func(i, j int) bool {
		if !f[i].ParkedAt.Equal(f[j].ParkedAt) {
			return f[i].ParkedAt.Before(f[j].ParkedAt)
		}
		return f[i].EventID < f[j].EventID
	})
}
