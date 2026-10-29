package repocontract

import (
	"errors"
	"testing"
	"time"

	"github.com/IQVO/customer-payments/internal/application/idempotency"
	"github.com/IQVO/customer-payments/internal/application/ports"
)

const route = "POST /payment-authorizations"

func testIdempotency(t *testing.T, e Env) {
	if _, err := e.Idempotency.Get(ctx(), route, "k1"); !errors.Is(err, ports.ErrNotFound) {
		t.Fatalf("Get of an unknown key = %v, want ErrNotFound", err)
	}
	now := e.Clock.Now().UTC()
	rec := idempotency.Record{RequestHash: "h1", Status: 201, ContentType: "application/json", Body: []byte(`{"a":1}`), CreatedAt: now}
	stored, err := e.Idempotency.Put(ctx(), route, "k1", rec)
	fatalIf(t, err, "first put")
	if !stored {
		t.Fatal("the first Put must store")
	}
	stored, err = e.Idempotency.Put(ctx(), route, "k1", idempotency.Record{RequestHash: "h2", Status: 500, CreatedAt: now})
	fatalIf(t, err, "second put")
	if stored {
		t.Fatal("a second Put must not overwrite")
	}
	got, err := e.Idempotency.Get(ctx(), route, "k1")
	fatalIf(t, err, "get")
	if got.RequestHash != "h1" || got.Status != 201 || got.ContentType != "application/json" || string(got.Body) != `{"a":1}` {
		t.Fatalf("stored record = %+v", got)
	}
	if _, err := e.Idempotency.Get(ctx(), "POST /other", "k1"); !errors.Is(err, ports.ErrNotFound) {
		t.Fatalf("the route is part of the key: %v", err)
	}
	expiredIdempotency(t, e)
}

func expiredIdempotency(t *testing.T, e Env) {
	old := idempotency.Record{RequestHash: "old", Status: 200, CreatedAt: e.Clock.Now().UTC().Add(-idempotency.TTL - time.Minute)}
	_, err := e.Idempotency.Put(ctx(), route, "k-old", old)
	fatalIf(t, err, "put an old record")
	if _, err := e.Idempotency.Get(ctx(), route, "k-old"); !errors.Is(err, ports.ErrNotFound) {
		t.Fatalf("an expired record must read as absent: %v", err)
	}
	replaced, err := e.Idempotency.Put(ctx(), route, "k-old", idempotency.Record{RequestHash: "new", Status: 201, CreatedAt: e.Clock.Now().UTC()})
	fatalIf(t, err, "replace an expired record")
	if !replaced {
		t.Fatal("an expired record is replaceable")
	}
}
