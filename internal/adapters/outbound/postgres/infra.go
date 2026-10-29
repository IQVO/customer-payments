package postgres

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/IQVO/customer-payments/internal/application/idempotency"
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

// IdempotencyStore is the Postgres ports.IdempotencyStore over idempotency_keys.
type IdempotencyStore struct{ pool *pgxpool.Pool }

var _ ports.IdempotencyStore = (*IdempotencyStore)(nil)

// NewIdempotencyStore constructs an IdempotencyStore over pool.
func NewIdempotencyStore(pool *pgxpool.Pool) *IdempotencyStore { return &IdempotencyStore{pool: pool} }

func ttlSeconds() float64 { return idempotency.TTL.Seconds() }

// Get implements ports.IdempotencyStore.
func (s *IdempotencyStore) Get(ctx context.Context, route, key string) (idempotency.Record, error) {
	var rec idempotency.Record
	err := queryFor(ctx, s.pool).QueryRow(ctx, `
		SELECT request_hash, status, content_type, body, created_at FROM idempotency_keys
		WHERE route = $1 AND key = $2 AND created_at > now() - make_interval(secs => $3)`, route, key, ttlSeconds()).
		Scan(&rec.RequestHash, &rec.Status, &rec.ContentType, &rec.Body, &rec.CreatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return idempotency.Record{}, ports.ErrNotFound
	}
	if err != nil {
		return idempotency.Record{}, fmt.Errorf("read idempotency key: %w", err)
	}
	rec.CreatedAt = rec.CreatedAt.UTC()
	return rec, nil
}

// Put implements ports.IdempotencyStore: it stores the record unless a fresh
// one exists; an expired record is replaced.
func (s *IdempotencyStore) Put(ctx context.Context, route, key string, rec idempotency.Record) (bool, error) {
	created := rec.CreatedAt
	if created.IsZero() {
		created = time.Now().UTC()
	}
	body := rec.Body
	if body == nil {
		body = []byte{}
	}
	stored := false
	err := inTx(ctx, s.pool, func(q querier) error {
		tag, err := q.Exec(ctx, `
			INSERT INTO idempotency_keys (route, key, request_hash, status, content_type, body, created_at) VALUES ($1, $2, $3, $4, $5, $6, $7)
			ON CONFLICT (route, key) DO UPDATE SET request_hash = EXCLUDED.request_hash, status = EXCLUDED.status,
				content_type = EXCLUDED.content_type, body = EXCLUDED.body, created_at = EXCLUDED.created_at
			WHERE idempotency_keys.created_at <= now() - make_interval(secs => $8)`,
			route, key, rec.RequestHash, rec.Status, rec.ContentType, body, created.UTC(), ttlSeconds())
		stored = err == nil && tag.RowsAffected() == 1
		return err
	})
	if err != nil {
		return false, fmt.Errorf("store idempotency key: %w", err)
	}
	return stored, nil
}
