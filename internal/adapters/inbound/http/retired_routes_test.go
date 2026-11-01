package http_test

import (
	"net/http"
	"testing"
)

// TestRetiredRoutesAreGone pins the REST guideline cutover: the retired
// spellings answer 404 or 405, never the old behaviour (no alias, no redirect).
func TestRetiredRoutesAreGone(t *testing.T) {
	e := newEnv(t)
	id := e.authorized()
	for _, c := range []struct{ method, path string }{
		{http.MethodPost, "/payment-authorizations/" + id + ":void"},
		{http.MethodPost, "/payment-authorizations/{authorizationId}:void"},
	} {
		r := e.do(c.method, c.path, "retired-key", "")
		if r.Status != http.StatusNotFound && r.Status != http.StatusMethodNotAllowed {
			t.Errorf("%s %s = %d, want 404 or 405: %s", c.method, c.path, r.Status, r.Body)
		}
	}
	// The authorization is untouched by the retired call.
	if got := e.get("/payment-authorizations/" + id).json(t)["state"]; got != "AUTHORIZED" {
		t.Fatalf("state = %v, want AUTHORIZED", got)
	}
}
