package repocontract

import (
	"testing"

	"github.com/IQVO/customer-payments/internal/application/ports"
)

func testAuthorizations(t *testing.T, e Env) {
	_, err := e.Authorizations.Get(ctx(), "nope")
	wantErr(t, err, ports.ErrNotFound)
	p := newAuth(t, "pay-1", "cs-1", 0)
	fatalIf(t, e.Authorizations.Save(ctx(), p), "save")
	if p.LoadedVersion() != p.Version() {
		t.Fatalf("Save must MarkPersisted: loaded %d, version %d", p.LoadedVersion(), p.Version())
	}
	got, err := e.Authorizations.Get(ctx(), "pay-1")
	fatalIf(t, err, "get")
	sameAuth(t, got, p)

	wantErr(t, e.Authorizations.Save(ctx(), newAuth(t, "pay-1", "cs-other", 0)), ports.ErrConcurrentUpdate)

	// capture, bind, save at the loaded version, then read back
	_, err = got.Capture(1, 7000, []string{"TRK1", "TRK2"}, "cap-1", at(5))
	fatalIf(t, err, "capture")
	_, err = got.BindOrder("ord-1")
	fatalIf(t, err, "bind")
	stale, err := e.Authorizations.Get(ctx(), "pay-1")
	fatalIf(t, err, "get stale copy")
	fatalIf(t, e.Authorizations.Save(ctx(), got), "save update")
	again, err := e.Authorizations.Get(ctx(), "pay-1")
	fatalIf(t, err, "get updated")
	sameAuth(t, again, got)
	if a, _ := again.Allocation(1); a.TrackingNumber() != "TRK1" || len(a.TrackingNumbers) != 2 || a.ProviderCaptureID != "cap-1" {
		t.Fatalf("allocation 1 = %+v", a)
	}
	if _, err = stale.BindOrder("ord-1"); err != nil {
		t.Fatal(err)
	}
	wantErr(t, e.Authorizations.Save(ctx(), stale), ports.ErrConcurrentUpdate)

	ghost := newAuth(t, "pay-ghost", "cs-ghost", 0)
	ghost.MarkPersisted()
	_, err = ghost.BindOrder("ord-g")
	fatalIf(t, err, "bind ghost")
	wantErr(t, e.Authorizations.Save(ctx(), ghost), ports.ErrConcurrentUpdate)
}

func testSessionUniqueness(t *testing.T, e Env) {
	savedAuth(t, e, "pay-a", "cs-1", 0)
	wantErr(t, e.Authorizations.Save(ctx(), newAuth(t, "pay-b", "cs-1", 1)), ports.ErrConcurrentUpdate)

	d1, d2 := declinedAuth(t, "pay-d1", "cs-2"), declinedAuth(t, "pay-d2", "cs-2")
	fatalIf(t, e.Authorizations.Save(ctx(), d1), "save first declined")
	fatalIf(t, e.Authorizations.Save(ctx(), d2), "declined attempts may repeat")
	fatalIf(t, e.Authorizations.Save(ctx(), newAuth(t, "pay-c", "cs-2", 2)), "a live one after declined attempts")
	wantErr(t, e.Authorizations.Save(ctx(), newAuth(t, "pay-e", "cs-2", 3)), ports.ErrConcurrentUpdate)

	live, err := e.Authorizations.FindLiveBySession(ctx(), "cs-2")
	fatalIf(t, err, "find live")
	if live.ID() != "pay-c" {
		t.Fatalf("live = %s", live.ID())
	}
	all, err := e.Authorizations.ListBySession(ctx(), "cs-2")
	fatalIf(t, err, "list session")
	if len(all) != 3 {
		t.Fatalf("session list = %d, want 3", len(all))
	}
	_, err = e.Authorizations.FindLiveBySession(ctx(), "cs-unknown")
	wantErr(t, err, ports.ErrNotFound)
	none, err := e.Authorizations.ListBySession(ctx(), "cs-unknown")
	fatalIf(t, err, "list unknown")
	if len(none) != 0 {
		t.Fatalf("unknown session list = %d", len(none))
	}
}

func testLookups(t *testing.T, e Env) {
	p := savedAuth(t, e, "pay-1", "cs-1", 0)
	_, err := p.Capture(2, 3000, []string{"TRK-X"}, "cap", at(1))
	fatalIf(t, err, "capture")
	_, err = p.BindOrder("ord-1")
	fatalIf(t, err, "bind")
	fatalIf(t, e.Authorizations.Save(ctx(), p), "save")
	for _, find := range []func() (string, error){
		func() (string, error) { a, err := e.Authorizations.FindByOrder(ctx(), "ord-1"); return idOf(a, err) },
		func() (string, error) { a, err := e.Authorizations.FindByTracking(ctx(), "TRK-X"); return idOf(a, err) },
	} {
		id, err := find()
		fatalIf(t, err, "lookup")
		if id != "pay-1" {
			t.Fatalf("lookup found %s", id)
		}
	}
	_, err = e.Authorizations.FindByOrder(ctx(), "ord-none")
	wantErr(t, err, ports.ErrNotFound)
	_, err = e.Authorizations.FindByTracking(ctx(), "TRK-none")
	wantErr(t, err, ports.ErrNotFound)
}

func testDueQueries(t *testing.T, e Env) {
	savedAuth(t, e, "pay-old", "cs-old", 0)
	savedAuth(t, e, "pay-new", "cs-new", 60)
	bound := savedAuth(t, e, "pay-bound", "cs-bound", 0)
	_, err := bound.BindOrder("ord-b")
	fatalIf(t, err, "bind")
	fatalIf(t, e.Authorizations.Save(ctx(), bound), "save bound")

	void, err := e.Authorizations.DueForVoid(ctx(), at(30), 10)
	fatalIf(t, err, "due for void")
	if len(void) != 1 || void[0] != "pay-old" {
		t.Fatalf("due for void = %v", void)
	}
	reauth, err := e.Authorizations.DueForReauthorization(ctx(), at(7*24*60+30), 10)
	fatalIf(t, err, "due for reauth")
	if len(reauth) != 2 || reauth[0] != "pay-bound" && reauth[0] != "pay-old" {
		t.Fatalf("due for reauth = %v", reauth)
	}
	limited, err := e.Authorizations.DueForReauthorization(ctx(), at(8*24*60), 1)
	fatalIf(t, err, "limit")
	if len(limited) != 1 {
		t.Fatalf("limit not applied: %v", limited)
	}
	early, err := e.Authorizations.DueForReauthorization(ctx(), at(10), 10)
	fatalIf(t, err, "early")
	if len(early) != 0 {
		t.Fatalf("nothing is due yet: %v", early)
	}
	voided := savedAuth(t, e, "pay-v", "cs-v", 0)
	_, err = voided.Void(at(1))
	fatalIf(t, err, "void")
	fatalIf(t, e.Authorizations.Save(ctx(), voided), "save voided")
	void, _ = e.Authorizations.DueForVoid(ctx(), at(30), 10)
	if len(void) != 1 {
		t.Fatalf("a voided authorization is not due: %v", void)
	}
}
