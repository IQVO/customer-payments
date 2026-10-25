package postgres

import (
	"context"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/IQVO/customer-payments/internal/adapters/outbound/postgres/pgtx"
	"github.com/IQVO/customer-payments/internal/application/ports"
	"github.com/IQVO/customer-payments/internal/domain/paymentauthorization"
	"github.com/IQVO/customer-payments/internal/domain/refund"
)

// RefundRepository is the Postgres ports.RefundRepository.
type RefundRepository struct{ pool *pgxpool.Pool }

var _ ports.RefundRepository = (*RefundRepository)(nil)

// NewRefundRepository constructs a RefundRepository over pool.
func NewRefundRepository(pool *pgxpool.Pool) *RefundRepository { return &RefundRepository{pool: pool} }

func (r *RefundRepository) find(ctx context.Context, lock bool, clause string, args ...any) ([]*refund.Refund, error) {
	suffix := ""
	if lock {
		if _, ok := pgtx.From(ctx); ok {
			suffix = " FOR UPDATE"
		}
	}
	rows, err := queryFor(ctx, r.pool).Query(ctx, `
		SELECT refund_id, authorization_id, order_id, group_no, currency, amount_minor, reason, state, attempts,
		       failure_code, provider_refund_id, created_at, version
		FROM refunds `+clause+suffix, args...)
	if err != nil {
		return nil, fmt.Errorf("read refunds: %w", err)
	}
	defer rows.Close()
	out := []*refund.Refund{}
	for rows.Next() {
		var s refund.Snapshot
		var reason, state string
		var code, prov *string
		var created time.Time
		q := &s.Request
		if err := rows.Scan(&q.ID, &q.AuthorizationID, &q.OrderID, &q.GroupNo, &q.Currency, &q.AmountMinor, &reason, &state,
			&s.Attempts, &code, &prov, &created, &s.Version); err != nil {
			return nil, fmt.Errorf("scan refund: %w", err)
		}
		q.Reason, s.State = refund.Reason(reason), refund.State(state)
		s.FailureCode, s.ProviderRefundID, s.CreatedAt = strOrEmpty(code), strOrEmpty(prov), created.UTC()
		f, err := refund.Restore(s)
		if err != nil {
			return nil, err
		}
		out = append(out, f)
	}
	return out, rows.Err()
}

// Get implements ports.RefundRepository.
func (r *RefundRepository) Get(ctx context.Context, id refund.ID) (*refund.Refund, error) {
	list, err := r.find(ctx, true, `WHERE refund_id = $1`, string(id))
	if err != nil {
		return nil, err
	}
	if len(list) == 0 {
		return nil, fmt.Errorf("refund %s: %w", id, ports.ErrNotFound)
	}
	return list[0], nil
}

// ListByOrder implements ports.RefundRepository.
func (r *RefundRepository) ListByOrder(ctx context.Context, orderID string) ([]*refund.Refund, error) {
	return r.find(ctx, false, `WHERE order_id = $1 ORDER BY created_at, refund_id`, orderID)
}

// ListByAuthorization implements ports.RefundRepository.
func (r *RefundRepository) ListByAuthorization(ctx context.Context, id paymentauthorization.ID) ([]*refund.Refund, error) {
	return r.find(ctx, false, `WHERE authorization_id = $1 ORDER BY created_at, refund_id`, string(id))
}

// DueRequested implements ports.RefundRepository.
func (r *RefundRepository) DueRequested(ctx context.Context, createdBy time.Time, limit int) ([]refund.ID, error) {
	rows, err := queryFor(ctx, r.pool).Query(ctx, `
		SELECT refund_id FROM refunds WHERE state = 'REQUESTED' AND created_at <= $1
		ORDER BY created_at, refund_id LIMIT $2`, createdBy.UTC(), limit)
	if err != nil {
		return nil, fmt.Errorf("scan requested refunds: %w", err)
	}
	defer rows.Close()
	var ids []refund.ID
	for rows.Next() {
		var id refund.ID
		if err := rows.Scan(&id); err != nil {
			return nil, fmt.Errorf("scan refund id: %w", err)
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}

// Save implements ports.RefundRepository.
func (r *RefundRepository) Save(ctx context.Context, f *refund.Refund) error {
	s := f.Snapshot()
	q := s.Request
	conflict := fmt.Errorf("refund %s loaded at version %d: %w", q.ID, f.LoadedVersion(), ports.ErrConcurrentUpdate)
	args := []any{
		string(q.ID), q.AuthorizationID, q.OrderID, q.GroupNo, q.Currency, q.AmountMinor, string(q.Reason), string(s.State),
		s.Attempts, nullStr(s.FailureCode), nullStr(s.ProviderRefundID), s.CreatedAt.UTC(), s.Version,
	}
	err := inTx(ctx, r.pool, func(tx querier) error {
		if f.LoadedVersion() == 0 {
			_, err := tx.Exec(ctx, `
				INSERT INTO refunds (refund_id, authorization_id, order_id, group_no, currency, amount_minor, reason, state,
					attempts, failure_code, provider_refund_id, created_at, version)
				VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13)`, args...)
			if isUniqueViolation(err) {
				return conflict
			}
			return err
		}
		tag, err := tx.Exec(ctx, `
			UPDATE refunds SET authorization_id = $2, order_id = $3, group_no = $4, currency = $5, amount_minor = $6, reason = $7,
				state = $8, attempts = $9, failure_code = $10, provider_refund_id = $11, created_at = $12, version = $13
			WHERE refund_id = $1 AND version = $14`, append(args, f.LoadedVersion())...)
		if err != nil {
			return err
		}
		if tag.RowsAffected() != 1 {
			return conflict
		}
		return nil
	})
	if err != nil {
		return err
	}
	f.MarkPersisted()
	return nil
}
