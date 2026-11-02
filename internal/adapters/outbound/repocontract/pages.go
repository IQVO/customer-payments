package repocontract

import (
	"fmt"
	"testing"

	"github.com/IQVO/customer-payments/internal/application/pagination"
	"github.com/IQVO/customer-payments/internal/domain/paymentauthorization"
	"github.com/IQVO/customer-payments/internal/domain/refund"
)

// The paged reads (REST guideline 3.4): keyset order (created-at, id) ascending, strictly
// after the keyset, at most limit rows. The fixtures share created-at instants on purpose,
// so a page that cut on created-at alone would skip or repeat rows.

// maxWalkPages bounds a pageWalker run: past it the walk is a bug, not a page.
const maxWalkPages = 100

// pageWalker pages through fetch with the given page size and returns the row
// ids in the order served; idOf renders a row's id and keyOf its resume keyset.
// It is the shared engine behind walkSessions and walkRefunds.
func pageWalker[T any](t *testing.T, what string, size int,
	fetch func(after *pagination.Keyset, limit int) ([]T, error),
	idOf func(T) string, keyOf func(T) pagination.Keyset,
) []string {
	t.Helper()
	var ids []string
	var after *pagination.Keyset
	for pages := 0; ; pages++ {
		if pages > maxWalkPages {
			t.Fatal("the walk never ends")
		}
		rows, err := fetch(after, size)
		fatalIf(t, err, what)
		if len(rows) > size {
			t.Fatalf("page of %d rows, limit %d", len(rows), size)
		}
		for _, row := range rows {
			ids = append(ids, idOf(row))
		}
		if len(rows) < size {
			return ids
		}
		after = new(pagination.Keyset)
		*after = keyOf(rows[len(rows)-1])
	}
}

// walkSessions pages through PageBySession with the given page size and returns the ids in
// the order served.
func walkSessions(t *testing.T, e Env, session string, size int) []string {
	return pageWalker(t, "page by session", size,
		func(after *pagination.Keyset, limit int) ([]*paymentauthorization.PaymentAuthorization, error) {
			return e.Authorizations.PageBySession(ctx(), session, after, limit)
		},
		func(p *paymentauthorization.PaymentAuthorization) string { return string(p.ID()) },
		func(p *paymentauthorization.PaymentAuthorization) pagination.Keyset {
			return pagination.Keyset{CreatedAt: p.CreatedAt(), ID: string(p.ID())}
		})
}

func walkRefunds(t *testing.T, e Env, order string, size int) []string {
	return pageWalker(t, "page refunds", size,
		func(after *pagination.Keyset, limit int) ([]*refund.Refund, error) {
			return e.Refunds.ListByOrder(ctx(), order, after, limit)
		},
		func(f *refund.Refund) string { return string(f.ID()) },
		func(f *refund.Refund) pagination.Keyset {
			return pagination.Keyset{CreatedAt: f.CreatedAt(), ID: string(f.ID())}
		})
}

func sameIDs(t *testing.T, what string, got, want []string) {
	t.Helper()
	if fmt.Sprint(got) != fmt.Sprint(want) {
		t.Fatalf("%s: got %v, want %v", what, got, want)
	}
}

func testPages(t *testing.T, e Env) {
	t.Run("authorizations of a session walk every page with tied created-at", func(t *testing.T) { sessionPages(t, e) })
	t.Run("refunds of an order walk every page with tied created-at", func(t *testing.T) { refundPages(t, e) })
}

func sessionPages(t *testing.T, e Env) {
	// Five declined attempts at t0 (a tie), a second tie at +3 min and the live one at +7 min.
	// Declined attempts may repeat for one session; only one live authorization may exist.
	for _, id := range []string{"pay-e", "pay-b", "pay-d", "pay-a", liveAuthID} {
		fatalIf(t, e.Authorizations.Save(ctx(), declinedAuthAt(t, id, "cs-page", 0)), "save declined "+id)
	}
	for _, id := range []string{"pay-h", "pay-g", "pay-f"} {
		fatalIf(t, e.Authorizations.Save(ctx(), declinedAuthAt(t, id, "cs-page", 3)), "save declined "+id)
	}
	savedAuth(t, e, "pay-z", "cs-page", 7)
	savedAuth(t, e, "pay-other", "cs-other", 0)

	want := []string{"pay-a", "pay-b", liveAuthID, "pay-d", "pay-e", "pay-f", "pay-g", "pay-h", "pay-z"}
	for _, size := range []int{1, 2, 3, 4, 9, 10, 500} {
		sameIDs(t, fmt.Sprintf("page size %d", size), walkSessions(t, e, "cs-page", size), want)
	}
	none, err := e.Authorizations.PageBySession(ctx(), "cs-nobody", nil, 5)
	fatalIf(t, err, "page of an unknown session")
	if len(none) != 0 {
		t.Fatalf("an unknown session has %d rows", len(none))
	}
}

func refundPages(t *testing.T, e Env) {
	fatalIf(t, e.Authorizations.Save(ctx(), newAuth(t, "pay-r", "cs-r", 0)), "authorization of the refunds")
	// Seven refunds: three at +1 min (tie), two at +2 min (tie), two at +5 min (tie).
	for i, m := range []int{5, 1, 2, 1, 5, 2, 1} {
		id := fmt.Sprintf("ref-%d", i)
		fatalIf(t, e.Refunds.Save(ctx(), newRefund(t, id, "pay-r", "ord-pages", m)), "save "+id)
	}
	fatalIf(t, e.Refunds.Save(ctx(), newRefund(t, "ref-other", "pay-r", "ord-other", 1)), "refund of another order")

	want := []string{"ref-1", "ref-3", "ref-6", "ref-2", "ref-5", "ref-0", "ref-4"}
	for _, size := range []int{1, 2, 3, 4, 7, 8, 500} {
		sameIDs(t, fmt.Sprintf("page size %d", size), walkRefunds(t, e, "ord-pages", size), want)
	}
}
