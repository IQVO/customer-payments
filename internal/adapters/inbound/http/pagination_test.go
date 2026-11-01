package http_test

import (
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"testing"
	"time"

	"github.com/IQVO/customer-payments/internal/adapters/inbound/http/page"
)

// walk follows nextCursor from the first page of path (a query without limit/cursor) until it
// is absent and returns the value of idField of every item in the order served, and the page count.
func walk(e *env, path, idField string, limit int) (ids []string, pages int) {
	e.t.Helper()
	cursor := ""
	for {
		target := path + "&limit=" + strconv.Itoa(limit)
		if cursor != "" {
			target += "&cursor=" + url.QueryEscape(cursor)
		}
		reply := e.get(target)
		e.wantStatus(reply, http.StatusOK)
		doc := reply.json(e.t)
		for _, retired := range []string{"authorizations", "refunds"} {
			if _, old := doc[retired]; old {
				e.t.Fatalf("the per-resource key `%s` is retired: %s", retired, reply.Body)
			}
		}
		items, ok := doc["items"].([]any)
		if !ok || len(items) > limit {
			e.t.Fatalf("items = %v (limit %d)", doc["items"], limit)
		}
		for _, it := range items {
			ids = append(ids, it.(map[string]any)[idField].(string))
		}
		pages++
		next, more := doc["nextCursor"].(string)
		if !more {
			return ids, pages
		}
		if pages > 100 {
			e.t.Fatal("the walk never ends")
		}
		cursor = next
	}
}

// checkWalks proves, for every page size, that the walk serves exactly the rows of one full
// read (limit 500) in the same order: no duplicate, no skip, and the right number of pages.
func checkWalks(t *testing.T, e *env, path, idField string, rows int) {
	t.Helper()
	full, pages := walk(e, path, idField, 500)
	if len(full) != rows || pages != 1 {
		t.Fatalf("full read: %d rows on %d pages, want %d on 1: %v", len(full), pages, rows, full)
	}
	seen := map[string]bool{}
	for _, id := range full {
		if seen[id] {
			t.Fatalf("%s repeated in the full read %v", id, full)
		}
		seen[id] = true
	}
	for _, limit := range []int{1, 2, 3, rows - 1, rows, rows + 1} {
		ids, got := walk(e, path, idField, limit)
		if fmt.Sprint(ids) != fmt.Sprint(full) {
			t.Fatalf("limit %d: walked %v, want %v", limit, ids, full)
		}
		if want := (rows + limit - 1) / limit; got != want {
			t.Fatalf("limit %d: %d pages, want %d", limit, got, want)
		}
	}
}

// More authorizations than the page size, most created at the same instant (the test clock is
// frozen between the Advance calls): every page is walked and no row is repeated or skipped.
func TestListAuthorizationsWalksEveryPageWithTiedCreatedAt(t *testing.T) {
	e := newEnv(t)
	e.provider.Fail("authorize", declined("CARD_DECLINED"))
	for i := 0; i < 7; i++ {
		if i == 4 {
			e.clock.Advance(time.Second)
		}
		e.wantStatus(e.post("/payment-authorizations", authorizeBody), http.StatusCreated)
	}
	e.provider.Fail("authorize", nil)
	e.clock.Advance(time.Second)
	e.authorized()
	e.wantStatus(e.post("/payment-authorizations", authorizeBodyFor("cs-other")), http.StatusCreated)

	checkWalks(t, e, "/payment-authorizations?checkoutSessionId=cs-1", "authorizationId", 8)
}

func TestListRefundsWalksEveryPageWithTiedCreatedAt(t *testing.T) {
	e := newEnv(t)
	e.shipped()
	for i := 0; i < 7; i++ {
		if i == 3 {
			e.clock.Advance(time.Second)
		}
		e.wantStatus(e.post("/refunds", `{"orderId":"ord-1","amountMinor":100,"reason":"MANUAL"}`), http.StatusCreated)
	}
	checkWalks(t, e, "/refunds?orderId=ord-1", "refundId", 7)
}

func TestListByOrderIsOnePageAndHonoursTheCursor(t *testing.T) {
	e := newEnv(t)
	id := e.shipped()
	doc := e.get("/payment-authorizations?orderId=ord-1&limit=1").json(t)
	items := doc["items"].([]any)
	if _, has := doc["nextCursor"]; has || len(items) != 1 || items[0].(map[string]any)["authorizationId"] != id {
		t.Fatalf("one authorization is bound to an order: %v", doc)
	}
	past := page.Encode(page.Position{Sort: time.Now().Add(time.Hour).UTC().Format(time.RFC3339Nano), ID: "pay-zzz"})
	after := e.get("/payment-authorizations?orderId=ord-1&cursor=" + url.QueryEscape(past)).json(t)
	if got := after["items"].([]any); len(got) != 0 {
		t.Fatalf("a cursor past the only row leaves nothing: %v", after)
	}
}

func TestListLastPageHasNoCursorAndEmptyIsAnEmptyPage(t *testing.T) {
	e := newEnv(t)
	e.authorized()
	one := e.get("/payment-authorizations?checkoutSessionId=cs-1&limit=1").json(t)
	if _, has := one["nextCursor"]; has || len(one["items"].([]any)) != 1 {
		t.Fatalf("exactly limit rows is the last page: %v", one)
	}
	for _, path := range []string{"/payment-authorizations?checkoutSessionId=nobody", "/refunds?orderId=nobody"} {
		none := e.get(path).json(t)
		if items, ok := none["items"].([]any); !ok || len(items) != 0 || none["nextCursor"] != nil {
			t.Fatalf("%s: no rows is {items: []}: %v", path, none)
		}
	}
}

// The retired paging parameters and any undeclared parameter fail loudly (STANDARD 4.2); bad limits
// and cursors are 400 problems. Pinned here because the Schemathesis-style contract run relies on it.
func TestListRejectsBadLimitCursorAndUnknownParameters(t *testing.T) {
	e := newEnv(t)
	e.authorized()
	lists := map[string]string{
		"/payment-authorizations?checkoutSessionId=cs-1": "checkoutSessionId",
		"/refunds?orderId=ord-1":                         "orderId",
	}
	notATime := page.Encode(page.Position{Sort: "yesterday", ID: "pay-1"})
	for path := range lists {
		for _, limit := range []string{"0", "501", "-1", "abc", "1.5"} {
			e.wantProblem(e.get(path+"&limit="+limit), http.StatusBadRequest, "invalid-limit")
		}
		for _, cursor := range []string{"%25%25", "bm90LWpzb24", "e30", url.QueryEscape(notATime)} {
			e.wantProblem(e.get(path+"&cursor="+cursor), http.StatusBadRequest, "invalid-cursor")
		}
		for _, retired := range []string{"after=x", "offset=10", "nextToken=abc", "from=x", "page=2", "whatever=1"} {
			e.wantProblem(e.get(path+"&"+retired), http.StatusBadRequest, "unknown-query-parameter")
		}
	}
	// A filter of the other list is not declared here either.
	e.wantProblem(e.get("/refunds?orderId=ord-1&checkoutSessionId=cs-1"), http.StatusBadRequest, "unknown-query-parameter")
	e.wantProblem(e.get("/payment-authorizations?checkoutSessionId=cs-1&orderId=ord-1"), http.StatusUnprocessableEntity, "validation-failed")
}
