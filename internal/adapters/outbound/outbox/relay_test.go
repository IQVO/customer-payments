package outbox_test

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"

	relay "github.com/IQVO/customer-payments/internal/adapters/outbound/outbox"
	"github.com/IQVO/customer-payments/internal/application/outbox"
)

// fakeStore drains a scripted list of rows.
type fakeStore struct {
	mu       sync.Mutex
	rows     []outbox.Message
	failAt   map[int]int // row index -> attempts reported when send fails
	capSeen  int
	limits   []int
	passes   int
	sendErrs []error
}

func (s *fakeStore) Drain(ctx context.Context, limit, maxAttempts int, send func(context.Context, outbox.Message) error) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.passes++
	s.capSeen = maxAttempts
	s.limits = append(s.limits, limit)
	n := 0
	for i := 0; i < len(s.rows) && n < limit; i++ {
		if err := send(ctx, s.rows[i]); err != nil {
			s.sendErrs = append(s.sendErrs, err)
			s.rows = s.rows[n:]
			return n, &outbox.SendFailure{RowID: int64(i + 1), Message: s.rows[0], Attempts: s.failAt[i], Err: err}
		}
		n++
	}
	s.rows = s.rows[n:]
	return n, nil
}

type fakeSink struct {
	mu   sync.Mutex
	got  []outbox.Message
	fail error
}

func (s *fakeSink) Send(_ context.Context, msgs ...outbox.Message) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.fail != nil {
		return s.fail
	}
	s.got = append(s.got, msgs...)
	return nil
}

func rows(n int) []outbox.Message {
	out := make([]outbox.Message, n)
	for i := range out {
		out[i] = outbox.Message{EventID: string(rune('a' + i)), Topic: "t", EventType: "x.Y", Key: []byte("k")}
	}
	return out
}

func TestRelayOncePublishesTheClaimedBatch(t *testing.T) {
	store, sink := &fakeStore{rows: rows(3)}, &fakeSink{}
	r := relay.NewRelay(store, sink, nil, relay.WithBatchSize(10), relay.WithMaxAttempts(7))
	n, err := r.RelayOnce(context.Background())
	if err != nil || n != 3 || len(sink.got) != 3 || sink.got[0].EventID != "a" || sink.got[2].EventID != "c" {
		t.Fatalf("n=%d err=%v got=%+v", n, err, sink.got)
	}
	if store.capSeen != 7 || store.limits[0] != 10 {
		t.Fatalf("the cap and batch size must reach the store: cap %d limits %v", store.capSeen, store.limits)
	}
}

func TestOptionsIgnoreNonsense(t *testing.T) {
	store := &fakeStore{}
	r := relay.NewRelay(store, &fakeSink{}, nil, relay.WithBatchSize(-1), relay.WithBatchSize(0), relay.WithInterval(0), relay.WithMaxAttempts(-3))
	if _, err := r.RelayOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	if store.limits[0] != relay.DefaultBatchSize || store.capSeen != 0 {
		t.Fatalf("defaults must survive: limit %d cap %d", store.limits[0], store.capSeen)
	}
	if relay.DefaultMaxAttempts != 20 || relay.DefaultInterval != time.Second {
		t.Fatal("fleet defaults changed")
	}
}

func TestAFailingRowIsLoggedOnceWhenItReachesTheCap(t *testing.T) {
	var buf bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&buf, nil))
	for _, c := range []struct {
		attempts, cap int
		logged        bool
	}{{2, 3, false}, {3, 3, true}, {4, 3, true}, {5, 0, false}} {
		buf.Reset()
		store := &fakeStore{rows: rows(2), failAt: map[int]int{0: c.attempts}}
		sink := &fakeSink{fail: errors.New("broker down")}
		r := relay.NewRelay(store, sink, logger, relay.WithMaxAttempts(c.cap))
		n, err := r.RelayOnce(context.Background())
		var sf *outbox.SendFailure
		if n != 0 || !errors.As(err, &sf) || !strings.Contains(err.Error(), "broker down") {
			t.Fatalf("n=%d err=%v", n, err)
		}
		if got := strings.Contains(buf.String(), relay.AttemptCapReachedMsg); got != c.logged {
			t.Fatalf("attempts=%d cap=%d: logged=%v, want %v\n%s", c.attempts, c.cap, got, c.logged, buf.String())
		}
	}
}

func TestRunDrainsUntilCancelledAndKeepsGoingAfterAFailure(t *testing.T) {
	store := &fakeStore{rows: rows(3)}
	sink := &fakeSink{fail: errors.New("broker down")}
	var buf bytes.Buffer
	r := relay.NewRelay(store, sink, slog.New(slog.NewTextHandler(&buf, nil)), relay.WithInterval(5*time.Millisecond), relay.WithBatchSize(2))
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- r.Run(ctx) }()

	waitFor(t, func() bool { store.mu.Lock(); defer store.mu.Unlock(); return store.passes >= 2 }) // failing passes are retried
	sink.mu.Lock()
	sink.fail = nil
	sink.mu.Unlock()
	waitFor(t, func() bool { sink.mu.Lock(); defer sink.mu.Unlock(); return len(sink.got) == 3 })
	cancel()
	if err := <-done; !errors.Is(err, context.Canceled) {
		t.Fatalf("Run must return the context error, got %v", err)
	}
	if !strings.Contains(buf.String(), "outbox relay pass failed") {
		t.Fatalf("a failed pass must be logged: %s", buf.String())
	}
	store.mu.Lock()
	defer store.mu.Unlock()
	if store.limits[len(store.limits)-1] != 2 {
		t.Fatalf("limits = %v", store.limits)
	}
}

func TestRunReturnsAtOnceWhenTheContextIsAlreadyCancelled(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	store := &fakeStore{}
	if err := relay.NewRelay(store, &fakeSink{}, nil).Run(ctx); !errors.Is(err, context.Canceled) || store.passes != 0 {
		t.Fatalf("err=%v passes=%d", err, store.passes)
	}
}

func waitFor(t *testing.T, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("condition not reached in time")
}
