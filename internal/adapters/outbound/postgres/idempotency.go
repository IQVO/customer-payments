package postgres

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/IQVO/customer-payments/internal/adapters/outbound/postgres/pgtx"
	"github.com/IQVO/customer-payments/internal/application/idempotency"
	"github.com/IQVO/customer-payments/internal/application/ports"
)

// IdempotencyStore is the Postgres ports.IdempotencyStore over idempotency_keys.
// The key is global (REST guideline 2.3): the table keeps its (route, key) primary
// key, and every row uses the empty route, so a key names one record whatever the
// route of the request.
type IdempotencyStore struct{ pool *pgxpool.Pool }

var _ ports.IdempotencyStore = (*IdempotencyStore)(nil)

// NewIdempotencyStore constructs an IdempotencyStore over pool.
func NewIdempotencyStore(pool *pgxpool.Pool) *IdempotencyStore { return &IdempotencyStore{pool: pool} }

func ttlSeconds() float64 { return idempotency.TTL.Seconds() }

// claimAttempts bounds how often Do re-claims a key whose live row vanished
// between the failed claim and the read (an expiry race).
const claimAttempts = 3

// httpServerError is the first status that is never stored (a 5xx).
const httpServerError = 500

// claimSQL inserts the key as a placeholder inside the request's transaction. A
// concurrent claimant of the same key blocks on the unique index until this
// transaction ends: it then finds the committed answer (no row is affected) or,
// after a rollback, claims the key itself. An expired row is taken over.
const claimSQL = `
	INSERT INTO idempotency_keys (route, key, request_hash, status, content_type, body, created_at)
	VALUES ('', $1, $2, 0, '', ''::bytea, $3)
	ON CONFLICT (route, key) DO UPDATE SET request_hash = EXCLUDED.request_hash, status = 0,
		content_type = '', body = ''::bytea, created_at = EXCLUDED.created_at
	WHERE idempotency_keys.created_at <= now() - make_interval(secs => $4)`

const storeSQL = `UPDATE idempotency_keys SET status = $2, content_type = $3, body = $4 WHERE route = '' AND key = $1`

const readSQL = `
	SELECT request_hash, status, content_type, body, created_at FROM idempotency_keys
	WHERE route = '' AND key = $1 AND created_at > now() - make_interval(secs => $2)`

// Do implements ports.IdempotencyStore: the claim, fn's writes (through the
// transaction carried by the ctx it is handed, so every unit of work joins it)
// and the stored answer commit together.
func (s *IdempotencyStore) Do(ctx context.Context, key, requestHash string, at time.Time,
	fn func(ctx context.Context) (idempotency.Record, error),
) (idempotency.Record, bool, error) {
	for range claimAttempts {
		rec, replayed, retry, err := s.attempt(ctx, key, requestHash, at, fn)
		if !retry {
			return rec, replayed, err
		}
	}
	return idempotency.Record{}, false, errors.New("claim idempotency key: the stored record kept expiring under the claim")
}

func (s *IdempotencyStore) attempt(ctx context.Context, key, requestHash string, at time.Time,
	fn func(ctx context.Context) (idempotency.Record, error),
) (rec idempotency.Record, replayed, retry bool, err error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return rec, false, false, fmt.Errorf("begin idempotent request: %w", err)
	}
	// A rollback after Commit is a harmless no-op; it also covers a panic in fn.
	defer func() { _ = tx.Rollback(context.WithoutCancel(ctx)) }()
	tag, err := tx.Exec(ctx, claimSQL, key, requestHash, at.UTC(), ttlSeconds())
	if err != nil {
		return rec, false, false, fmt.Errorf("claim idempotency key: %w", err)
	}
	if tag.RowsAffected() == 0 {
		_ = tx.Rollback(context.WithoutCancel(ctx))
		return s.replay(ctx, key)
	}
	if rec, err = fn(pgtx.With(ctx, tx)); err != nil {
		return idempotency.Record{}, false, false, err
	}
	if rec.Status >= httpServerError {
		return rec, false, false, nil // rolled back by the defer: a 5xx is never stored
	}
	body := rec.Body
	if body == nil {
		body = []byte{}
	}
	if _, err := tx.Exec(ctx, storeSQL, key, rec.Status, rec.ContentType, body); err != nil {
		return idempotency.Record{}, false, false, fmt.Errorf("store idempotency key: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return idempotency.Record{}, false, false, fmt.Errorf("commit idempotent request: %w", err)
	}
	rec.RequestHash, rec.CreatedAt = requestHash, at.UTC()
	return rec, false, false, nil
}

// replay reads the committed record that made the claim fail; it asks for a new
// claim when the record expired or was removed in between.
func (s *IdempotencyStore) replay(ctx context.Context, key string) (rec idempotency.Record, replayed, retry bool, err error) {
	err = s.pool.QueryRow(ctx, readSQL, key, ttlSeconds()).
		Scan(&rec.RequestHash, &rec.Status, &rec.ContentType, &rec.Body, &rec.CreatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return idempotency.Record{}, false, true, nil
	}
	if err != nil {
		return idempotency.Record{}, false, false, fmt.Errorf("read idempotency key: %w", err)
	}
	rec.CreatedAt = rec.CreatedAt.UTC()
	return rec, true, false, nil
}
