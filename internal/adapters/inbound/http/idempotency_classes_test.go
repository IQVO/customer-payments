package http_test

import (
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	httpadapter "github.com/IQVO/customer-payments/internal/adapters/inbound/http"
	"github.com/IQVO/customer-payments/internal/application/ports"
)

// An error answer whose problem type declares no class (a handler that wrote a
// bare 4xx) is not remembered: the fail-safe direction is to run the work again.
func TestAnUnclassifiedErrorAnswerIsNotStored(t *testing.T) {
	a := newReplayAdapter(t)
	bare := func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/problem+json")
		w.WriteHeader(http.StatusConflict)
	}
	h := &fakeHandler{script: []answerFn{bare, succeeding(a, "ok")}}
	mw := a.Middleware(t, h)

	if first := post(t, mw, "k-bare", `{"n":1}`); first.Code != http.StatusConflict {
		t.Fatalf("first = %d, want the 409", first.Code)
	}
	if got := a.KeyRows(t, "k-bare"); got != 0 {
		t.Fatalf("key rows after an unclassified 409 = %d, want 0", got)
	}
	if retry := post(t, mw, "k-bare", `{"n":1}`); retry.Code != http.StatusCreated || h.calls != 2 || replayed(retry) {
		t.Fatalf("retry = %d replayed=%v calls=%d, want a fresh 201 after 2 calls", retry.Code, replayed(retry), h.calls)
	}
}

// A copy of a request that waits on the key while the first ends RETRYABLE
// never replays the released refusal and never runs alongside the first: it
// takes the key over and runs the handler itself, and the copies behind it
// replay that run's answer.
func TestACopyWaitingOnARetryableFirstRunsItself(t *testing.T) {
	a := newReplayAdapter(t)
	var calls, active, peak atomic.Int32
	h := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := calls.Add(1)
		cur := active.Add(1)
		for p := peak.Load(); cur > p && !peak.CompareAndSwap(p, cur); p = peak.Load() {
		}
		time.Sleep(20 * time.Millisecond) // let the copies queue behind the key
		active.Add(-1)
		if n == 1 {
			httpadapter.WriteError(w, r, ports.ErrConcurrentUpdate)
			return
		}
		succeeding(a, "ok")(w, r)
	})
	mw := a.Middleware(t, h)

	const copies = 12
	var (
		wg      sync.WaitGroup
		mu      sync.Mutex
		fresh   = map[int]int{}
		replays int
	)
	for range copies {
		wg.Add(1)
		go func() {
			defer wg.Done()
			rec := post(t, mw, "k-wait", `{"n":1}`)
			mu.Lock()
			defer mu.Unlock()
			if replayed(rec) {
				replays++
				if rec.Code != http.StatusCreated {
					t.Errorf("a replay answered %d: only the stored success may be replayed", rec.Code)
				}
				return
			}
			fresh[rec.Code]++
		}()
	}
	wg.Wait()
	if calls.Load() != 2 || peak.Load() != 1 {
		t.Fatalf("handler ran %d times with %d at once, want 2 and 1", calls.Load(), peak.Load())
	}
	if fresh[http.StatusConflict] != 1 || fresh[http.StatusCreated] != 1 || replays != copies-2 {
		t.Fatalf("fresh answers %v and %d replays, want one 409, one 201 and %d replays", fresh, replays, copies-2)
	}
	if got := a.KeyRows(t, "k-wait"); got != 1 {
		t.Fatalf("key rows = %d, want 1: the success is stored", got)
	}
}

// Every refusal a keyed handler writes goes through writeError, the writer that
// marks the capture with the class of its problem; a handler that wrote a
// problem by hand would bypass the class (and be released as unclassified).
func TestHandlersAnswerErrorsOnlyThroughWriteError(t *testing.T) {
	src, err := os.ReadFile("handlers.go")
	if err != nil {
		t.Fatal(err)
	}
	for _, banned := range []string{"writeProblem(", "writeBody(", "WriteHeader(", "problem+json"} {
		if strings.Contains(string(src), banned) {
			t.Errorf("handlers.go contains %q: answer an error with writeError(w, r, err) so its replay class marks the capture", banned)
		}
	}
}

var adrClassRow = regexp.MustCompile("^\\| `([a-z-]+)` \\| (FINAL|OUTCOME|RETRYABLE) \\|")

// ADR 0008 carries the class of every problem type with its reason; the table
// and the catalogue must say the same, in both directions.
func TestTheADRDeclaresTheClassOfEveryProblem(t *testing.T) {
	paths, err := filepath.Glob("../../../../docs/adr/0008-*.md")
	if err != nil || len(paths) != 1 {
		t.Fatalf("ADR 0008 not found (%v, %v)", paths, err)
	}
	raw, err := os.ReadFile(paths[0])
	if err != nil {
		t.Fatal(err)
	}
	inADR := map[string]string{}
	for _, line := range regexp.MustCompile(`\r?\n`).Split(string(raw), -1) {
		if m := adrClassRow.FindStringSubmatch(line); m != nil {
			inADR[m[1]] = m[2]
		}
	}
	for _, e := range newReplayAdapter(t).Catalogue() {
		if got := inADR[e.Slug]; got != e.Class {
			t.Errorf("problem type %q: the ADR says %q, the catalogue %q", e.Slug, got, e.Class)
		}
		delete(inADR, e.Slug)
	}
	for slug, class := range inADR {
		t.Errorf("the ADR classifies %q as %s but the catalogue has no such problem type", slug, class)
	}
}
