package mcp_test

import (
	"context"
	"strings"
	"testing"
	"time"

	sdk "github.com/modelcontextprotocol/go-sdk/mcp"

	inboundmcp "github.com/IQVO/customer-payments/internal/adapters/inbound/mcp"
	"github.com/IQVO/customer-payments/internal/adapters/outbound/memory"
	"github.com/IQVO/customer-payments/internal/domain/paymentauthorization"
	"github.com/IQVO/customer-payments/internal/domain/refund"
)

// t0 is the fixture's fixed "now".
var t0 = time.Date(2026, 10, 24, 10, 0, 0, 0, time.UTC)

// harness is a connected in-memory MCP client over the real server wired to
// in-memory repositories, exactly as cmd/mcp wires them (minus Postgres). The
// write side exists ONLY here, to seed data the way the REST adapter and the
// consumers would: the MCP surface itself has none.
type harness struct {
	session *sdk.ClientSession
	store   *memory.Store
}

func newHarness(t *testing.T) *harness {
	t.Helper()
	store := memory.NewStore(memory.NewClock(t0))
	deps := inboundmcp.NewDeps(store.Authorizations(), store.Refunds())
	return &harness{session: connectSession(t, deps), store: store}
}

func connectSession(t *testing.T, deps inboundmcp.Deps) *sdk.ClientSession {
	t.Helper()
	ctx := context.Background()
	clientTransport, serverTransport := sdk.NewInMemoryTransports()
	serverSession, err := inboundmcp.NewServer(deps).Connect(ctx, serverTransport, nil)
	if err != nil {
		t.Fatalf("server connect: %v", err)
	}
	t.Cleanup(func() { _ = serverSession.Close() })
	client := sdk.NewClient(&sdk.Implementation{Name: "test-client", Version: "0"}, nil)
	session, err := client.Connect(ctx, clientTransport, nil)
	if err != nil {
		t.Fatalf("client connect: %v", err)
	}
	t.Cleanup(func() { _ = session.Close() })
	return session
}

func must(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}

// seedAuthorization stores an AUTHORIZED authorization bound to ord-1 whose
// group 1 was captured with tracking number TRK1.
func (h *harness) seedAuthorization(t *testing.T) {
	t.Helper()
	p, _, err := paymentauthorization.New(paymentauthorization.Draft{
		ID: "pay-1", CheckoutSessionID: "cs-1", PaymentMethodToken: "tok_visa", Currency: "BRL", TotalMinor: 10000,
		Allocations: []paymentauthorization.AllocationInput{{GroupNo: 1, AmountMinor: 7000}, {GroupNo: 2, AmountMinor: 3000}},
	}, paymentauthorization.Outcome{ProviderAuthorizationID: "hold-1", ExpiresAt: t0.Add(7 * 24 * time.Hour)}, t0)
	must(t, err)
	_, err = p.BindOrder("ord-1")
	must(t, err)
	_, err = p.Capture(1, 7000, []string{"TRK1"}, "cap-1", t0.Add(time.Hour))
	must(t, err)
	must(t, h.store.Authorizations().Save(t.Context(), p))
}

// seedRefund stores a FAILED refund of group 1.
func (h *harness) seedRefund(t *testing.T) {
	t.Helper()
	f, err := refund.New(refund.Request{
		ID: "ref-1", AuthorizationID: "pay-1", OrderID: "ord-1", GroupNo: 1, Currency: "BRL", AmountMinor: 7000, Reason: refund.ReasonLost,
	}, refund.Limits{CapturedMinor: 7000, OrderCapturedMinor: 7000}, t0)
	must(t, err)
	_, err = f.RecordFailure("ACCOUNT_CLOSED", t0)
	must(t, err)
	must(t, h.store.Refunds().Save(t.Context(), f))
}

// call invokes a tool and fails the test on a TRANSPORT/protocol error;
// tool-level failures come back as res.IsError.
func call(t *testing.T, session *sdk.ClientSession, name string, args map[string]any) *sdk.CallToolResult {
	t.Helper()
	res, err := session.CallTool(context.Background(), &sdk.CallToolParams{Name: name, Arguments: args})
	if err != nil {
		t.Fatalf("%s: transport/protocol error (want a tool result): %v", name, err)
	}
	return res
}

// ok calls a tool, requires success, and returns its structured content.
func (h *harness) ok(t *testing.T, name string, args map[string]any) map[string]any {
	t.Helper()
	res := call(t, h.session, name, args)
	if res.IsError {
		t.Fatalf("%s returned a tool error: %s", name, text(res))
	}
	sc, isMap := res.StructuredContent.(map[string]any)
	if !isMap {
		t.Fatalf("%s: structured content = %#v, want an object", name, res.StructuredContent)
	}
	return sc
}

// failWith calls a tool and requires an isError result whose text starts
// with the slug want.
func failWith(t *testing.T, session *sdk.ClientSession, name string, args map[string]any, want string) {
	t.Helper()
	res := call(t, session, name, args)
	if !res.IsError {
		t.Fatalf("%s: expected an isError tool result, got %#v", name, res.StructuredContent)
	}
	if got := text(res); !strings.HasPrefix(got, want+": ") {
		t.Fatalf("%s: error text %q does not start with %q", name, got, want+": ")
	}
}

func text(res *sdk.CallToolResult) string {
	var b strings.Builder
	for _, c := range res.Content {
		if tc, isText := c.(*sdk.TextContent); isText {
			b.WriteString(tc.Text)
		}
	}
	return b.String()
}
