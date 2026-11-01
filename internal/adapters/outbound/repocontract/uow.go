package repocontract

import (
	"context"
	"errors"
	"testing"

	"github.com/IQVO/customer-payments/internal/application/ports"
)

var errBoom = errors.New("boom")

func wantErr(t *testing.T, got, want error) {
	t.Helper()
	if !errors.Is(got, want) {
		t.Fatalf("error = %v, want %v", got, want)
	}
}

func testInbox(t *testing.T, e Env) {
	first, err := e.Inbox.Claim(ctx(), "ev-1", "type.A")
	fatalIf(t, err, "claim")
	again, err := e.Inbox.Claim(ctx(), "ev-1", "type.A")
	fatalIf(t, err, "claim again")
	other, err := e.Inbox.Claim(ctx(), "ev-2", "type.A")
	fatalIf(t, err, "claim other")
	if !first || again || !other {
		t.Fatalf("claims = %v %v %v, want true false true", first, again, other)
	}
}

func testUnitOfWork(t *testing.T, e Env) {
	t.Run("a unit that fails rolls back every table", func(t *testing.T) { uowRollsBack(t, e) })
	t.Run("a unit that succeeds commits everything", func(t *testing.T) { uowCommits(t, e) })
	t.Run("a nested unit joins the outer one", func(t *testing.T) { uowNests(t, e) })
	t.Run("a nested unit that fails undoes only itself", func(t *testing.T) { uowNestedFailure(t, e) })
}

func uowRollsBack(t *testing.T, e Env) {
	err := e.UoW.Do(ctx(), func(c context.Context) error {
		fatalIf(t, e.Authorizations.Save(c, newAuth(t, "pay-rb", "cs-rb", 0)), "save in unit")
		fatalIf(t, e.Parked.Park(c, parkedFact("ev-p", "ord-rb", 0)), "park in unit")
		first, err := e.Inbox.Claim(c, "ev-rb", "type.A")
		fatalIf(t, err, "claim in unit")
		if !first {
			t.Fatal("the claim inside the unit must be the first")
		}
		fatalIf(t, e.Outbox.Append(c, authEvents(t, "pay-rbe")...), "append in unit")
		return errBoom
	})
	wantErr(t, err, errBoom)
	_, err = e.Authorizations.Get(ctx(), "pay-rb")
	wantErr(t, err, ports.ErrNotFound)
	if n, _ := e.Parked.Count(ctx()); n != 0 {
		t.Fatalf("a rolled back parked fact is visible: %d", n)
	}
	if first, _ := e.Inbox.Claim(ctx(), "ev-rb", "type.A"); !first {
		t.Fatal("the rolled back claim must be free again")
	}
	if n := e.OutboxCount(t); n != 0 {
		t.Fatalf("outbox holds %d events after a rollback", n)
	}
}

func uowCommits(t *testing.T, e Env) {
	fatalIf(t, e.UoW.Do(ctx(), func(c context.Context) error {
		fatalIf(t, e.Authorizations.Save(c, newAuth(t, "pay-ok", "cs-ok", 0)), "save in unit")
		return e.Outbox.Append(c, authEvents(t, "pay-oke")...)
	}), "commit")
	if _, err := e.Authorizations.Get(ctx(), "pay-ok"); err != nil {
		t.Fatalf("committed authorization: %v", err)
	}
	if n := e.OutboxCount(t); n != 1 {
		t.Fatalf("outbox holds %d events, want 1", n)
	}
}

func uowNests(t *testing.T, e Env) {
	err := e.UoW.Do(ctx(), func(c context.Context) error {
		fatalIf(t, e.UoW.Do(c, func(c context.Context) error { return e.Authorizations.Save(c, newAuth(t, "pay-nest", "cs-nest", 0)) }), "inner")
		return errBoom
	})
	wantErr(t, err, errBoom)
	_, err = e.Authorizations.Get(ctx(), "pay-nest")
	wantErr(t, err, ports.ErrNotFound)
}

// uowNestedFailure: the use cases retry a whole attempt on ErrConcurrentUpdate, so a failed
// attempt must leave nothing behind inside the unit it joined (a savepoint in Postgres).
func uowNestedFailure(t *testing.T, e Env) {
	err := e.UoW.Do(ctx(), func(c context.Context) error {
		fatalIf(t, e.Authorizations.Save(c, newAuth(t, "pay-keep", "cs-keep", 0)), "save outer")
		inner := e.UoW.Do(c, func(c context.Context) error {
			fatalIf(t, e.Authorizations.Save(c, newAuth(t, "pay-undone", "cs-undone", 0)), "save inner")
			return errBoom
		})
		wantErr(t, inner, errBoom)
		return nil
	})
	fatalIf(t, err, "outer unit")
	if _, err := e.Authorizations.Get(ctx(), "pay-keep"); err != nil {
		t.Fatalf("the outer write must commit: %v", err)
	}
	_, err = e.Authorizations.Get(ctx(), "pay-undone")
	wantErr(t, err, ports.ErrNotFound)
}
