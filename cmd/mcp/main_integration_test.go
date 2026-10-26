//go:build integration

package main

import (
	"context"
	"os"
	"testing"
	"time"

	sdk "github.com/modelcontextprotocol/go-sdk/mcp"

	inboundmcp "github.com/IQVO/customer-payments/internal/adapters/inbound/mcp"
	"github.com/IQVO/customer-payments/internal/adapters/outbound/postgres"
	"github.com/IQVO/customer-payments/internal/domain/paymentauthorization"
	"github.com/IQVO/customer-payments/internal/testing/pgtest"
)

func TestMain(m *testing.M) {
	code := m.Run()
	pgtest.Shutdown()
	os.Exit(code)
}

// The binary migrates a database itself (idempotently: pgtest's databases are
// already migrated, a second run must be a no-op), then reads through MCP
// tools exactly what the REST side persisted, and its reads add no outbox row.
func TestMCPAgainstPostgres_ReadsWhatTheAPIWroteAndNeverWrites(t *testing.T) {
	url := pgtest.URL(t)
	ctx := context.Background()
	pool, err := postgres.NewPool(ctx, url)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	t0 := time.Date(2026, 10, 24, 10, 0, 0, 0, time.UTC)
	p, _, err := paymentauthorization.New(paymentauthorization.Draft{
		ID: "pay-it-1", CheckoutSessionID: "cs-it-1", PaymentMethodToken: "tok_visa", Currency: "BRL", TotalMinor: 5000,
		Allocations: []paymentauthorization.AllocationInput{{GroupNo: 1, AmountMinor: 5000}},
	}, paymentauthorization.Outcome{ProviderAuthorizationID: "hold-1", ExpiresAt: t0.Add(7 * 24 * time.Hour)}, t0)
	if err != nil {
		t.Fatal(err)
	}
	if err := postgres.NewAuthorizationRepository(pool).Save(ctx, p); err != nil {
		t.Fatal(err)
	}
	var before int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM outbox_events`).Scan(&before); err != nil {
		t.Fatal(err)
	}

	deps, closeFn, err := buildDeps(ctx, quietLogger(), url, url)
	if err != nil {
		t.Fatalf("buildDeps: %v", err)
	}
	t.Cleanup(closeFn)
	ct, st := sdk.NewInMemoryTransports()
	ss, err := inboundmcp.NewServer(deps).Connect(ctx, st, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer ss.Close()
	session, err := sdk.NewClient(&sdk.Implementation{Name: "t", Version: "0"}, nil).Connect(ctx, ct, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer session.Close()

	res, err := session.CallTool(ctx, &sdk.CallToolParams{Name: "get_payment_authorization", Arguments: map[string]any{"authorizationId": "pay-it-1"}})
	if err != nil || res.IsError {
		t.Fatalf("get_payment_authorization: err=%v res=%+v", err, res)
	}
	got := res.StructuredContent.(map[string]any)
	if got["state"] != "AUTHORIZED" || got["checkoutSessionId"] != "cs-it-1" || got["totalMinor"] != 5000.0 {
		t.Fatalf("authorization = %v", got)
	}
	var after int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM outbox_events`).Scan(&after); err != nil || after != before {
		t.Fatalf("outbox rows %d -> %d (%v): the MCP binary wrote an event", before, after, err)
	}
}
