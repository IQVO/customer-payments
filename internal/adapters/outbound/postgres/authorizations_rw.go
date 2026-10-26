package postgres

import (
	"context"
	"fmt"
	"time"

	"github.com/IQVO/customer-payments/internal/application/ports"
	"github.com/IQVO/customer-payments/internal/domain/paymentauthorization"
)

// loadAuthorizations reads the rows matching the clause (which may carry
// ORDER BY and LIMIT) and their allocations.
func loadAuthorizations(ctx context.Context, q querier, lock, clause string, args ...any) ([]*paymentauthorization.PaymentAuthorization, error) {
	rows, err := q.Query(ctx, `
		SELECT authorization_id, checkout_session_id, order_id, payment_method_token, currency, total_minor, terminal,
		       decline_reason, provider_authorization_id, expires_at, created_at, reauthorizations, version
		FROM payment_authorizations `+clause+lock, args...)
	if err != nil {
		return nil, fmt.Errorf("read authorizations: %w", err)
	}
	var snaps []paymentauthorization.Snapshot
	for rows.Next() {
		var (
			s                          paymentauthorization.Snapshot
			order, terminal, dec, prov *string
			expires                    *time.Time
			created                    time.Time
		)
		if err := rows.Scan(&s.ID, &s.CheckoutSessionID, &order, &s.PaymentMethodToken, &s.Currency, &s.TotalMinor, &terminal,
			&dec, &prov, &expires, &created, &s.Reauthorizations, &s.Version); err != nil {
			rows.Close()
			return nil, fmt.Errorf("scan authorization: %w", err)
		}
		s.OrderID, s.DeclineReason, s.ProviderAuthorizationID = strOrEmpty(order), strOrEmpty(dec), strOrEmpty(prov)
		s.Terminal = paymentauthorization.State(strOrEmpty(terminal))
		s.ExpiresAt, s.CreatedAt = timeOrZero(expires), created.UTC()
		snaps = append(snaps, s)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return nil, fmt.Errorf("read authorizations: %w", err)
	}
	rows.Close()
	out := make([]*paymentauthorization.PaymentAuthorization, 0, len(snaps))
	for _, s := range snaps {
		if s.Allocations, err = loadAllocations(ctx, q, s.ID); err != nil {
			return nil, err
		}
		p, err := paymentauthorization.Restore(s)
		if err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, nil
}

func loadAllocations(ctx context.Context, q querier, id paymentauthorization.ID) ([]paymentauthorization.Allocation, error) {
	rows, err := q.Query(ctx, `
		SELECT group_no, amount_minor, state, captured_minor, tracking_numbers, provider_capture_id
		FROM allocations WHERE authorization_id = $1 ORDER BY group_no`, string(id))
	if err != nil {
		return nil, fmt.Errorf("read allocations of %s: %w", id, err)
	}
	defer rows.Close()
	var out []paymentauthorization.Allocation
	for rows.Next() {
		var a paymentauthorization.Allocation
		var state string
		var capID *string
		if err := rows.Scan(&a.GroupNo, &a.AmountMinor, &state, &a.CapturedMinor, &a.TrackingNumbers, &capID); err != nil {
			return nil, fmt.Errorf("scan allocation of %s: %w", id, err)
		}
		a.State, a.ProviderCaptureID = paymentauthorization.AllocState(state), strOrEmpty(capID)
		if len(a.TrackingNumbers) == 0 {
			a.TrackingNumbers = nil
		}
		out = append(out, a)
	}
	return out, rows.Err()
}

// Save implements ports.AuthorizationRepository.
func (r *AuthorizationRepository) Save(ctx context.Context, p *paymentauthorization.PaymentAuthorization) error {
	s := p.Snapshot()
	err := inTx(ctx, r.pool, func(q querier) error {
		if err := writeAuthorization(ctx, q, p, s); err != nil {
			return err
		}
		if _, err := q.Exec(ctx, `DELETE FROM allocations WHERE authorization_id = $1`, string(s.ID)); err != nil {
			return fmt.Errorf("replace allocations of %s: %w", s.ID, err)
		}
		for _, a := range s.Allocations {
			tracking := a.TrackingNumbers
			if tracking == nil {
				tracking = []string{}
			}
			if _, err := q.Exec(ctx, `
				INSERT INTO allocations (authorization_id, group_no, amount_minor, state, captured_minor, tracking_numbers, provider_capture_id)
				VALUES ($1, $2, $3, $4, $5, $6, $7)`,
				string(s.ID), a.GroupNo, a.AmountMinor, string(a.State), a.CapturedMinor, tracking, nullStr(a.ProviderCaptureID)); err != nil {
				return fmt.Errorf("insert allocation %d of %s: %w", a.GroupNo, s.ID, err)
			}
		}
		return nil
	})
	if err != nil {
		return err
	}
	p.MarkPersisted()
	return nil
}

// writeAuthorization inserts the row of a new authorization or updates the one
// at the loaded version; anything else, including a second live authorization
// for the checkout session, is ports.ErrConcurrentUpdate.
func writeAuthorization(ctx context.Context, q querier, p *paymentauthorization.PaymentAuthorization, s paymentauthorization.Snapshot) error {
	conflict := fmt.Errorf("authorization %s loaded at version %d: %w", s.ID, p.LoadedVersion(), ports.ErrConcurrentUpdate)
	args := []any{
		string(s.ID), s.CheckoutSessionID, nullStr(s.OrderID), s.PaymentMethodToken, s.Currency, s.TotalMinor,
		string(p.State()), nullStr(string(s.Terminal)), nullStr(s.DeclineReason), nullStr(s.ProviderAuthorizationID),
		nullTime(s.ExpiresAt), s.CreatedAt.UTC(), s.Reauthorizations, s.Version,
	}
	if p.LoadedVersion() == 0 {
		_, err := q.Exec(ctx, `
			INSERT INTO payment_authorizations (authorization_id, checkout_session_id, order_id, payment_method_token, currency,
				total_minor, state, terminal, decline_reason, provider_authorization_id, expires_at, created_at, reauthorizations, version)
			VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14)`, args...)
		if isUniqueViolation(err) {
			return conflict
		}
		return err
	}
	tag, err := q.Exec(ctx, `
		UPDATE payment_authorizations SET checkout_session_id = $2, order_id = $3, payment_method_token = $4, currency = $5,
			total_minor = $6, state = $7, terminal = $8, decline_reason = $9, provider_authorization_id = $10,
			expires_at = $11, created_at = $12, reauthorizations = $13, version = $14
		WHERE authorization_id = $1 AND version = $15`, append(args, p.LoadedVersion())...)
	if isUniqueViolation(err) {
		return conflict
	}
	if err != nil {
		return err
	}
	if tag.RowsAffected() != 1 {
		return conflict
	}
	return nil
}
