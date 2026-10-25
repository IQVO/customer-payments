package postgres

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/IQVO/customer-payments/internal/adapters/outbound/postgres/pgtx"
	"github.com/IQVO/customer-payments/internal/application/ports"
	"github.com/IQVO/customer-payments/internal/domain/paymentauthorization"
)

// pgUniqueViolation is the SQLSTATE of a unique constraint violation.
const pgUniqueViolation = "23505"

func isUniqueViolation(err error) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && pgErr.Code == pgUniqueViolation
}

// nullTime stores the zero time as NULL.
func nullTime(t time.Time) *time.Time {
	if t.IsZero() {
		return nil
	}
	u := t.UTC()
	return &u
}

// timeOrZero reads a nullable timestamp as UTC (zero when NULL).
func timeOrZero(t *time.Time) time.Time {
	if t == nil {
		return time.Time{}
	}
	return t.UTC()
}

func strOrEmpty(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}

func nullStr(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

// readTx runs fn on the transaction carried by ctx (so a read inside a unit of
// work sees its own writes and, with lock, takes row locks), or on a read-only
// REPEATABLE READ transaction that gives fn one consistent snapshot.
func readTx(ctx context.Context, pool *pgxpool.Pool, fn func(q querier, lock string) error) error {
	if tx, ok := pgtx.From(ctx); ok {
		return fn(tx, " FOR UPDATE")
	}
	tx, err := pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly})
	if err != nil {
		return fmt.Errorf("begin read: %w", err)
	}
	defer func() { _ = tx.Rollback(context.WithoutCancel(ctx)) }()
	return fn(tx, "")
}

// AuthorizationRepository is the Postgres ports.AuthorizationRepository.
type AuthorizationRepository struct{ pool *pgxpool.Pool }

var _ ports.AuthorizationRepository = (*AuthorizationRepository)(nil)

// NewAuthorizationRepository constructs an AuthorizationRepository over pool.
func NewAuthorizationRepository(pool *pgxpool.Pool) *AuthorizationRepository {
	return &AuthorizationRepository{pool: pool}
}

// find loads the authorizations matching the clause (which may carry ORDER BY
// and LIMIT), locking the rows inside a unit of work.
func (r *AuthorizationRepository) find(ctx context.Context, clause string, args ...any) ([]*paymentauthorization.PaymentAuthorization, error) {
	var out []*paymentauthorization.PaymentAuthorization
	err := readTx(ctx, r.pool, func(q querier, lock string) error {
		list, err := loadAuthorizations(ctx, q, lock, clause, args...)
		out = list
		return err
	})
	return out, err
}

func (r *AuthorizationRepository) one(ctx context.Context, what, clause string, args ...any) (*paymentauthorization.PaymentAuthorization, error) {
	list, err := r.find(ctx, clause+" LIMIT 1", args...)
	if err != nil {
		return nil, err
	}
	if len(list) == 0 {
		return nil, fmt.Errorf("authorization %s: %w", what, ports.ErrNotFound)
	}
	return list[0], nil
}

// Get implements ports.AuthorizationRepository. Inside a unit of work the row
// is locked (FOR UPDATE) so concurrent writers of one authorization serialise.
func (r *AuthorizationRepository) Get(ctx context.Context, id paymentauthorization.ID) (*paymentauthorization.PaymentAuthorization, error) {
	return r.one(ctx, string(id), `WHERE authorization_id = $1`, string(id))
}

// FindLiveBySession implements ports.AuthorizationRepository.
func (r *AuthorizationRepository) FindLiveBySession(ctx context.Context, sessionID string) (*paymentauthorization.PaymentAuthorization, error) {
	return r.one(ctx, "of session "+sessionID, `WHERE checkout_session_id = $1 AND state <> 'DECLINED'`, sessionID)
}

// ListBySession implements ports.AuthorizationRepository.
func (r *AuthorizationRepository) ListBySession(ctx context.Context, sessionID string) ([]*paymentauthorization.PaymentAuthorization, error) {
	return r.find(ctx, `WHERE checkout_session_id = $1 ORDER BY created_at, authorization_id`, sessionID)
}

// FindByOrder implements ports.AuthorizationRepository.
func (r *AuthorizationRepository) FindByOrder(ctx context.Context, orderID string) (*paymentauthorization.PaymentAuthorization, error) {
	return r.one(ctx, "of order "+orderID, `WHERE order_id = $1 AND state <> 'DECLINED'`, orderID)
}

// FindByTracking implements ports.AuthorizationRepository.
func (r *AuthorizationRepository) FindByTracking(ctx context.Context, tracking string) (*paymentauthorization.PaymentAuthorization, error) {
	return r.one(ctx, "with tracking "+tracking,
		`WHERE authorization_id IN (SELECT authorization_id FROM allocations WHERE tracking_numbers @> ARRAY[$1]::text[])`, tracking)
}

func (r *AuthorizationRepository) ids(ctx context.Context, query string, at time.Time, limit int) ([]paymentauthorization.ID, error) {
	rows, err := queryFor(ctx, r.pool).Query(ctx, query, at.UTC(), limit)
	if err != nil {
		return nil, fmt.Errorf("scan authorizations: %w", err)
	}
	defer rows.Close()
	var ids []paymentauthorization.ID
	for rows.Next() {
		var id paymentauthorization.ID
		if err := rows.Scan(&id); err != nil {
			return nil, fmt.Errorf("scan authorization id: %w", err)
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}

// DueForReauthorization implements ports.AuthorizationRepository.
func (r *AuthorizationRepository) DueForReauthorization(ctx context.Context, expiresBy time.Time, limit int) ([]paymentauthorization.ID, error) {
	return r.ids(ctx, `SELECT authorization_id FROM payment_authorizations
		WHERE state IN ('AUTHORIZED', 'PARTIALLY_CAPTURED') AND expires_at <= $1
		ORDER BY expires_at, authorization_id LIMIT $2`, expiresBy, limit)
}

// DueForVoid implements ports.AuthorizationRepository.
func (r *AuthorizationRepository) DueForVoid(ctx context.Context, createdBy time.Time, limit int) ([]paymentauthorization.ID, error) {
	return r.ids(ctx, `SELECT authorization_id FROM payment_authorizations
		WHERE state = 'AUTHORIZED' AND order_id IS NULL AND created_at <= $1
		ORDER BY created_at, authorization_id LIMIT $2`, createdBy, limit)
}
