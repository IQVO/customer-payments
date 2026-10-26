// Package pgtx carries an open pgx transaction in a context.Context, so the
// Postgres repositories participate in a ports.UnitOfWork without knowing about
// each other. postgres.UnitOfWork stores the tx with With; every repository
// reads it back with From.
package pgtx

import (
	"context"

	"github.com/jackc/pgx/v5"
)

type ctxKey struct{}

// With returns a copy of ctx that carries tx.
func With(ctx context.Context, tx pgx.Tx) context.Context {
	return context.WithValue(ctx, ctxKey{}, tx)
}

// From returns the transaction carried by ctx, if any.
func From(ctx context.Context) (pgx.Tx, bool) {
	tx, ok := ctx.Value(ctxKey{}).(pgx.Tx)
	return tx, ok && tx != nil
}
