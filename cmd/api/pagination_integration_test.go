//go:build integration

package main

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"testing"

	"github.com/IQVO/customer-payments/internal/testing/providerfake"
)

// The keyset cursor over the real Postgres adapters (the repository contract covers ties; this
// proves the wire cursor and the SQL row comparison agree end to end).

func (r *idemRig) pageOf(t *testing.T, query string) (ids []string, next string, status int) {
	t.Helper()
	resp, err := http.Get(r.url + query)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	raw, _ := io.ReadAll(resp.Body)
	var doc struct {
		Items []struct {
			AuthorizationID string `json:"authorizationId"`
		} `json:"items"`
		NextCursor string `json:"nextCursor"`
	}
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatalf("not JSON: %s", raw)
	}
	for _, it := range doc.Items {
		ids = append(ids, it.AuthorizationID)
	}
	return ids, doc.NextCursor, resp.StatusCode
}

func TestAuthorizationsWalkEveryPageOnPostgres(t *testing.T) {
	r := newIdemRig(t)
	declined := strings.Replace(authorizeBody("cs-walk"), "tok_visa", providerfake.DeclineToken, 1)
	var created []string
	for i := range 6 {
		got := r.post(t, "/payment-authorizations", fmt.Sprintf("walk-%d", i), declined)
		wantIdem(t, got, 201, "")
		var doc struct {
			ID    string `json:"authorizationId"`
			State string `json:"state"`
		}
		if err := json.Unmarshal([]byte(got.body), &doc); err != nil || doc.State != "DECLINED" {
			t.Fatalf("attempt %d: %s", i, got.body)
		}
		created = append(created, doc.ID)
	}
	wantIdem(t, r.post(t, "/payment-authorizations", "walk-live", authorizeBody("cs-walk")), 201, "")
	wantIdem(t, r.post(t, "/payment-authorizations", "walk-other", authorizeBody("cs-elsewhere")), 201, "")

	full, next, status := r.pageOf(t, "/payment-authorizations?checkoutSessionId=cs-walk&limit=500")
	if status != 200 || next != "" || len(full) != 7 {
		t.Fatalf("full read = %d rows, next %q, status %d", len(full), next, status)
	}
	for _, size := range []int{1, 2, 3, 6, 7, 8} {
		var walked []string
		cursor := ""
		for pages := 0; pages < 20; pages++ {
			query := fmt.Sprintf("/payment-authorizations?checkoutSessionId=cs-walk&limit=%d", size)
			if cursor != "" {
				query += "&cursor=" + url.QueryEscape(cursor)
			}
			ids, more, status := r.pageOf(t, query)
			if status != 200 || len(ids) > size {
				t.Fatalf("limit %d: status %d, %d rows", size, status, len(ids))
			}
			walked = append(walked, ids...)
			if cursor = more; cursor == "" {
				break
			}
		}
		if fmt.Sprint(walked) != fmt.Sprint(full) {
			t.Fatalf("limit %d: walked %v, want %v", size, walked, full)
		}
	}
	for _, id := range created {
		if !strings.Contains(strings.Join(full, " "), id) {
			t.Fatalf("declined attempt %s is missing from %v", id, full)
		}
	}
	_, _, status = r.pageOf(t, "/payment-authorizations?checkoutSessionId=cs-walk&after=x")
	if status != 400 {
		t.Fatalf("a retired paging parameter = %d, want 400", status)
	}
}
