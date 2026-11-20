//go:build integration

package main

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/IQVO/customer-payments/internal/domain/paymentauthorization"
)

// The replay classes (ADR 0008) over the REAL router and REAL Postgres: a
// RETRYABLE refusal rolls the request back, key row included, so the same key
// retries the work and is then stored like any success.

func (r *idemRig) keyRows(t *testing.T, key string) int {
	t.Helper()
	return r.rows(t, `SELECT count(*) FROM idempotency_keys WHERE key = $1`, key)
}

// concurrent-update is RETRYABLE. Every read inside a keyed request locks its
// row (FOR UPDATE), so a lost optimistic-lock race cannot be provoked by timing
// here: the trigger makes the version-guarded UPDATE match no row, exactly what
// the guard sees when another writer got in between the load and the save, on
// both attempts of the use case (commandAttempts). The refused request must
// leave no key row; the same key then runs again, is stored and is replayed.
func TestAConcurrentUpdateRefusalIsNotStoredSoItsKeyRetries(t *testing.T) {
	r := newIdemRig(t)
	created := r.post(t, "/payment-authorizations", "auth-cu", authorizeBody("cs-cu"))
	wantIdem(t, created, 201, "")
	var auth struct {
		ID string `json:"authorizationId"`
	}
	if err := json.Unmarshal([]byte(created.body), &auth); err != nil || auth.ID == "" {
		t.Fatalf("authorize answer %s: %v", created.body, err)
	}
	path := "/payment-authorizations/" + auth.ID + "/voids"

	r.exec(t, `CREATE FUNCTION cp_drop_update() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RETURN NULL; END $$`)
	r.exec(t, `CREATE TRIGGER cp_drop_update BEFORE UPDATE ON payment_authorizations FOR EACH ROW EXECUTE FUNCTION cp_drop_update()`)
	refused := r.post(t, path, "void-cu", "")
	wantIdem(t, refused, 409, "concurrent-update")
	if n := r.keyRows(t, "void-cu"); n != 0 {
		t.Fatalf("the refused request left %d row(s) under its key, want 0: a concurrent-update refusal is RETRYABLE and must release the key", n)
	}

	r.exec(t, `DROP TRIGGER cp_drop_update ON payment_authorizations`)
	retry := r.post(t, path, "void-cu", "")
	wantIdem(t, retry, 200, "")
	if retry.replayed != "" {
		t.Fatalf("the retry under the released key = %+v, want a fresh 200", retry)
	}
	if n := r.keyRows(t, "void-cu"); n != 1 {
		t.Fatalf("%d row(s) under the key after the retry succeeded, want 1 (a 2xx is stored)", n)
	}
	again := r.post(t, path, "void-cu", "")
	if again.status != 200 || again.body != retry.body || again.replayed != "true" {
		t.Fatalf("third call = %+v, want the stored 200 replayed", again)
	}
	if n := r.fake.Effects("voids"); n != 1 {
		t.Fatalf("provider voids = %d, want 1: the provider key is deterministic, the retry must not void twice", n)
	}
}

// capturedAuthorization stores an authorization of session bound to order with
// group 1 captured, the state the events bind and capture after the fact.
func capturedAuthorization(t *testing.T, r *idemRig, session, order string) {
	t.Helper()
	now := time.Now().UTC()
	p, _, err := paymentauthorization.New(paymentauthorization.Draft{
		ID: paymentauthorization.ID("auth-" + session), CheckoutSessionID: session, PaymentMethodToken: "tok_visa", Currency: "BRL", TotalMinor: 10000,
		Allocations: []paymentauthorization.AllocationInput{{GroupNo: 1, AmountMinor: 7000}, {GroupNo: 2, AmountMinor: 3000}},
	}, paymentauthorization.Outcome{ProviderAuthorizationID: "hold-" + session, ExpiresAt: now.Add(48 * time.Hour)}, now)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := p.BindOrder(order); err != nil {
		t.Fatal(err)
	}
	if _, err := p.Capture(1, 7000, []string{"TRK-" + session}, "cap-"+session, now); err != nil {
		t.Fatal(err)
	}
	if err := r.auths.Save(context.Background(), p); err != nil {
		t.Fatal(err)
	}
}

// not-found is RETRYABLE: the order a manual refund names is bound to its
// authorization by an event (CheckoutCompleted), so it can exist a moment
// after the request that found nothing.
func TestANotFoundRefusalIsNotStoredSoTheRefundRetriesOnceTheOrderIsBound(t *testing.T) {
	r := newIdemRig(t)
	const key, body = "refund-late", `{"orderId":"ord-late","amountMinor":1000,"reason":"MANUAL"}`

	refused := r.post(t, "/refunds", key, body)
	wantIdem(t, refused, 404, "not-found")
	if n := r.keyRows(t, key); n != 0 {
		t.Fatalf("the 404 left %d row(s) under its key, want 0: not-found is RETRYABLE and must release the key", n)
	}

	capturedAuthorization(t, r, "cs-late", "ord-late")
	retry := r.post(t, "/refunds", key, body)
	wantIdem(t, retry, 201, "")
	if retry.replayed != "" || !strings.Contains(retry.body, `"ord-late"`) {
		t.Fatalf("the retry under the released key = %+v, want a fresh 201 for ord-late", retry)
	}
	if n := r.keyRows(t, key); n != 1 {
		t.Fatalf("%d row(s) under the key after the retry succeeded, want 1", n)
	}
	again := r.post(t, "/refunds", key, body)
	if again.status != 201 || again.body != retry.body || again.replayed != "true" {
		t.Fatalf("third call = %+v, want the stored 201 replayed", again)
	}
	if n := r.rows(t, `SELECT count(*) FROM refunds WHERE order_id = 'ord-late'`); n != 1 {
		t.Fatalf("%d refund(s) for ord-late, want 1: the replay must not create another", n)
	}
}
