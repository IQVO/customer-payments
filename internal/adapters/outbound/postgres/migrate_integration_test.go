//go:build integration

package postgres_test

import (
	"context"
	"testing"

	"github.com/IQVO/customer-payments/internal/adapters/outbound/postgres"
	"github.com/IQVO/customer-payments/internal/testing/pgtest"
)

func tableCount(t *testing.T, url string) int {
	t.Helper()
	pool, err := postgres.NewPool(context.Background(), url)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	var n int
	if err := pool.QueryRow(context.Background(), `SELECT count(*) FROM information_schema.tables WHERE table_schema = 'public' AND table_name <> 'schema_migrations'`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

func TestMigrationsGoUpDownUp(t *testing.T) {
	url := pgtest.EmptyURL(t)
	if n := tableCount(t, url); n != 0 {
		t.Fatalf("a fresh database holds %d tables", n)
	}
	if err := postgres.RunMigrations(url); err != nil {
		t.Fatalf("up: %v", err)
	}
	up := tableCount(t, url)
	// 0001 creates the 7 tables of the customer-payments schema.
	if up != 7 {
		t.Fatalf("after up: %d tables, want 7", up)
	}
	if err := postgres.RunMigrations(url); err != nil {
		t.Fatalf("a second up must be a no-op: %v", err)
	}
	if err := postgres.RollbackMigrations(url); err != nil {
		t.Fatalf("down: %v", err)
	}
	if n := tableCount(t, url); n != 0 {
		t.Fatalf("after down: %d tables remain", n)
	}
	if err := postgres.RollbackMigrations(url); err != nil {
		t.Fatalf("a second down must be a no-op: %v", err)
	}
	if err := postgres.RunMigrations(url); err != nil {
		t.Fatalf("up again: %v", err)
	}
	if n := tableCount(t, url); n != up {
		t.Fatalf("after up again: %d tables, want %d", n, up)
	}
}
