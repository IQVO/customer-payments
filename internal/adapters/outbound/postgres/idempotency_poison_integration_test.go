//go:build integration

package postgres_test

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	"github.com/IQVO/customer-payments/internal/adapters/outbound/postgres"
	"github.com/IQVO/customer-payments/internal/adapters/outbound/postgres/pgtx"
	"github.com/IQVO/customer-payments/internal/application/idempotency"
	"github.com/IQVO/customer-payments/internal/testing/pgtest"
)

// poisonTx simulates what a mapped conflict does to the store's transaction: a
// statement fails on the server (a unique violation, like a lost race on a
// unique index), the repository maps it to a typed error and the handler answers
// 4xx, but the transaction is ABORTED from then on. The store must still record
// and return that truthful 4xx.
func poisonTx(ctx context.Context, t *testing.T) {
	t.Helper()
	tx, ok := pgtx.From(ctx)
	if !ok {
		t.Fatal("the store did not bind a transaction into the handler context")
	}
	const dup = `INSERT INTO idempotency_keys (route, key, request_hash, status, content_type, body, created_at)
		VALUES ('poison', 'dup', 'h', 0, '', ''::bytea, now())`
	for range 2 {
		if _, err := tx.Exec(ctx, dup); err != nil {
			return // the second insert aborts the transaction (23505)
		}
	}
	t.Fatal("the duplicate insert did not abort the transaction")
}

func TestIdempotencyA409OverAnAbortedTransactionIsCommitted(t *testing.T) {
	pool := pgtest.NewPool(t)
	store := postgres.NewIdempotencyStore(pool)
	ctx := context.Background()
	at := time.Now().UTC().Truncate(time.Microsecond)
	var runs atomic.Int64
	conflict := func(ctx context.Context) (idempotency.Record, error) {
		runs.Add(1)
		poisonTx(ctx, t)
		return idempotency.Record{Status: 409, ContentType: "application/problem+json", Body: []byte(`{"status":409}`)}, nil
	}

	rec, replayed, err := store.Do(ctx, "k-poison", "h1", at, conflict)
	if err != nil || replayed || rec.Status != 409 {
		t.Fatalf("first = %+v replayed=%v err=%v, want the truthful 409", rec, replayed, err)
	}
	var status int
	if err := pool.QueryRow(ctx, `SELECT status FROM idempotency_keys WHERE route = '' AND key = 'k-poison'`).Scan(&status); err != nil || status != 409 {
		t.Fatalf("the 409 outcome was not committed: status=%d err=%v", status, err)
	}
	var leaked int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM idempotency_keys WHERE route = 'poison'`).Scan(&leaked); err != nil || leaked != 0 {
		t.Fatalf("the handler's partial writes survived: %d rows, err=%v", leaked, err)
	}

	rec, replayed, err = store.Do(ctx, "k-poison", "h1", at, conflict)
	if err != nil || !replayed || rec.Status != 409 || string(rec.Body) != `{"status":409}` {
		t.Fatalf("replay = %+v replayed=%v err=%v, want the cached 409", rec, replayed, err)
	}
	if runs.Load() != 1 {
		t.Fatalf("handler ran %d times, a replay must never re-run it", runs.Load())
	}
}

func TestIdempotencyA2xxOverAnAbortedTransactionIsNeverCached(t *testing.T) {
	pool := pgtest.NewPool(t)
	store := postgres.NewIdempotencyStore(pool)
	ctx := context.Background()
	at := time.Now().UTC().Truncate(time.Microsecond)
	var calls atomic.Int64
	handler := func(ctx context.Context) (idempotency.Record, error) {
		if calls.Add(1) == 1 {
			poisonTx(ctx, t) // only the first attempt dies inside the transaction
		}
		return idempotency.Record{Status: 201, Body: []byte(`{"id":"a1"}`)}, nil
	}

	if rec, _, err := store.Do(ctx, "k-lie", "h1", at, handler); err == nil {
		t.Fatalf("first = %+v, want an error: the writes never happened", rec)
	}
	var n int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM idempotency_keys WHERE key = 'k-lie'`).Scan(&n); err != nil || n != 0 {
		t.Fatalf("a failed success left %d idempotency rows (err=%v): the key could not retry", n, err)
	}

	rec, replayed, err := store.Do(ctx, "k-lie", "h1", at, handler)
	if err != nil || replayed || rec.Status != 201 {
		t.Fatalf("retry = %+v replayed=%v err=%v, want the real work to succeed", rec, replayed, err)
	}
	if calls.Load() != 2 {
		t.Fatalf("handler ran %d times, want 2", calls.Load())
	}
}
