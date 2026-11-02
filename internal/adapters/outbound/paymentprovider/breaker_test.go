package paymentprovider

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/IQVO/customer-payments/internal/application/provider"
)

// fakeTransport is the breaker test's stand-in for the network: it counts the
// RoundTrips that actually reached it and answers with a transport failure,
// a 200 JSON id answer or a bare 500, on demand.
type fakeTransport struct {
	calls int
	fail  bool // transport failure (the provider is unreachable)
	five  bool // the provider answers 500 (it IS reachable)
}

func (f *fakeTransport) RoundTrip(*http.Request) (*http.Response, error) {
	f.calls++
	switch {
	case f.fail:
		return nil, errors.New("connection refused")
	case f.five:
		return textResponse(http.StatusInternalServerError, ""), nil
	default:
		return textResponse(http.StatusOK, `{"id":"prv-1"}`), nil
	}
}

func textResponse(status int, body string) *http.Response {
	return &http.Response{StatusCode: status, Body: io.NopCloser(strings.NewReader(body)), Header: http.Header{}}
}

// newBrokeClient builds a Client whose breaker has a small threshold and a
// movable clock, so the cooldown can elapse without sleeping.
func newBrokeClient(ft *fakeTransport, threshold int, now *time.Time) *Client {
	return &Client{
		base: &url.URL{Scheme: "http", Host: "provider.invalid"},
		http: &http.Client{Transport: ft},
		br:   newBreaker(threshold, time.Minute, func() time.Time { return *now }),
	}
}

func refund(c *Client) error {
	_, err := c.Refund(context.Background(), "k", "hold-1", 100)
	return err
}

func TestBreakerOpensAfterConsecutiveTransportFailures(t *testing.T) {
	now := time.Date(2026, 11, 2, 12, 0, 0, 0, time.UTC)
	ft := &fakeTransport{fail: true}
	c := newBrokeClient(ft, 3, &now)
	for i := 0; i < 3; i++ {
		if err := refund(c); !errors.Is(err, provider.ErrUnavailable) {
			t.Fatalf("call %d err = %v", i, err)
		}
	}
	if got := c.br.state(); got != "open" {
		t.Fatalf("state = %q, want open", got)
	}
	// While open, calls fail fast without reaching the transport.
	calls := ft.calls
	err := refund(c)
	if !errors.Is(err, provider.ErrUnavailable) || !strings.Contains(err.Error(), "circuit open") {
		t.Fatalf("open-circuit err = %v", err)
	}
	if ft.calls != calls {
		t.Fatalf("an open breaker still reached the transport (%d -> %d calls)", calls, ft.calls)
	}
}

func TestBreakerHalfOpenProbeClosesAfterCooldown(t *testing.T) {
	now := time.Date(2026, 11, 2, 12, 0, 0, 0, time.UTC)
	ft := &fakeTransport{fail: true}
	c := newBrokeClient(ft, 2, &now)
	refund(c)
	refund(c)
	if c.br.state() != "open" {
		t.Fatal("breaker did not open")
	}
	// After the cooldown the state is half-open and one probe passes; the
	// provider has recovered, so the probe closes the breaker.
	now = now.Add(time.Minute + time.Second)
	if got := c.br.state(); got != "half-open" {
		t.Fatalf("state = %q, want half-open", got)
	}
	ft.fail = false
	if err := refund(c); err != nil {
		t.Fatalf("probe err = %v", err)
	}
	if got := c.br.state(); got != "closed" {
		t.Fatalf("state = %q, want closed", got)
	}
	if err := refund(c); err != nil {
		t.Fatalf("post-recovery err = %v", err)
	}
}

func TestBreakerHalfOpenProbeFailureReopens(t *testing.T) {
	now := time.Date(2026, 11, 2, 12, 0, 0, 0, time.UTC)
	ft := &fakeTransport{fail: true}
	c := newBrokeClient(ft, 1, &now)
	refund(c) // threshold 1: open immediately
	now = now.Add(time.Minute + time.Second)
	refund(c) // the half-open probe fails; the cooldown restarts
	if got := c.br.state(); got != "open" {
		t.Fatalf("state = %q, want open after a failed probe", got)
	}
	calls := ft.calls // both failures reached the transport, nothing more does
	if err := refund(c); !errors.Is(err, provider.ErrUnavailable) {
		t.Fatalf("reopened err = %v", err)
	}
	if ft.calls != calls {
		t.Fatalf("a reopened breaker reached the transport (%d -> %d calls)", calls, ft.calls)
	}
}

func TestBreakerDoesNotTripOnProviderStatusAnswers(t *testing.T) {
	now := time.Date(2026, 11, 2, 12, 0, 0, 0, time.UTC)
	ft := &fakeTransport{five: true}
	c := newBrokeClient(ft, 3, &now)
	for i := 0; i < 5; i++ {
		if err := refund(c); !errors.Is(err, provider.ErrUnavailable) {
			t.Fatalf("500 err = %v", err)
		}
	}
	if got := c.br.state(); got != "closed" {
		t.Fatalf("state = %q, want closed: a provider that answers is alive", got)
	}
	if ft.calls != 5 {
		t.Fatalf("calls = %d, want 5", ft.calls)
	}
}
