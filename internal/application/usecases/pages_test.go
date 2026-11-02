package usecases_test

import (
	"context"
	"testing"

	"github.com/IQVO/customer-payments/internal/application/usecases"
)

// The read tools of the MCP and A2A adapters get the complete answer: All follows the pages of
// Execute (100 rows each) until the collection ends.
func TestListRefundsAllFollowsEveryPage(t *testing.T) {
	e := newEnv(t)
	e.shippedBoth()
	const n = 130
	for range n {
		_, err := usecases.RequestRefund{Deps: e.deps}.Execute(context.Background(), usecases.RequestRefundInput{OrderID: "ord-1", AmountMinor: 1, Reason: "MANUAL"})
		if err != nil {
			t.Fatal(err)
		}
	}
	all, err := usecases.ListRefunds{Deps: e.deps}.All(context.Background(), "ord-1")
	if err != nil || len(all) != n {
		t.Fatalf("All = %d refunds, err %v, want %d", len(all), err, n)
	}
	seen := map[string]bool{}
	for _, f := range all {
		if seen[f.RefundID] {
			t.Fatalf("%s repeated", f.RefundID)
		}
		seen[f.RefundID] = true
	}
	first, err := usecases.ListRefunds{Deps: e.deps}.Execute(context.Background(), "ord-1", nil, 100)
	if err != nil || len(first.Items) != 100 || first.Next == nil {
		t.Fatalf("first page = %d items, next %v, err %v", len(first.Items), first.Next, err)
	}
	rest, err := usecases.ListRefunds{Deps: e.deps}.Execute(context.Background(), "ord-1", first.Next, 100)
	if err != nil || len(rest.Items) != n-100 || rest.Next != nil {
		t.Fatalf("second page = %d items, next %v, err %v", len(rest.Items), rest.Next, err)
	}
}
