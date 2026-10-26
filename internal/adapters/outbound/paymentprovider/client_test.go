package paymentprovider_test

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/IQVO/customer-payments/internal/adapters/outbound/paymentprovider"
	"github.com/IQVO/customer-payments/internal/application/provider"
	"github.com/IQVO/customer-payments/internal/testing/providerfake"
)

func newClient(t *testing.T) (*paymentprovider.Client, *providerfake.Fake) {
	t.Helper()
	f := providerfake.New(t)
	c, err := paymentprovider.New(f.URL(), nil)
	if err != nil {
		t.Fatal(err)
	}
	return c, f
}

func authReq(key, token string) provider.AuthorizeRequest {
	return provider.AuthorizeRequest{Key: key, PaymentMethodToken: token, Currency: "BRL", AmountMinor: 10000, Reference: "cs-1"}
}

func TestAuthorizeIsIdempotentOnTheKey(t *testing.T) {
	c, f := newClient(t)
	a, err := c.Authorize(context.Background(), authReq("k1", "tok_visa"))
	if err != nil || a.ID == "" || !a.ExpiresAt.After(time.Now()) {
		t.Fatalf("authorize = %+v %v", a, err)
	}
	b, err := c.Authorize(context.Background(), authReq("k1", "tok_visa"))
	if err != nil || b.ID != a.ID || f.Effects("authorize") != 1 {
		t.Fatalf("replay = %+v %v effects %d", b, err, f.Effects("authorize"))
	}
}

func TestAuthorizeDecline(t *testing.T) {
	c, _ := newClient(t)
	_, err := c.Authorize(context.Background(), authReq("k1", providerfake.DeclineToken))
	if !errors.Is(err, provider.ErrDeclined) || provider.Code(err) != "CARD_DECLINED" {
		t.Fatalf("err = %v", err)
	}
}

func TestCaptureRefundVoidReduceReauthorize(t *testing.T) {
	c, f := newClient(t)
	ctx := context.Background()
	hold, _ := c.Authorize(ctx, authReq("k1", "tok_visa"))
	if id, err := c.Capture(ctx, "c1", hold.ID, 7000); err != nil || id == "" {
		t.Fatalf("capture = %q %v", id, err)
	}
	if id, err := c.Refund(ctx, "r1", hold.ID, 700); err != nil || id == "" {
		t.Fatalf("refund = %q %v", id, err)
	}
	if err := c.ReduceAuthorization(ctx, "d1", hold.ID, 3000); err != nil {
		t.Fatalf("reduce: %v", err)
	}
	if next, err := c.Reauthorize(ctx, "a1", hold.ID, 3000); err != nil || next.ID == "" {
		t.Fatalf("reauthorize = %+v %v", next, err)
	}
	if err := c.Void(ctx, "v1", hold.ID); err != nil {
		t.Fatalf("void: %v", err)
	}
	for _, op := range []string{"authorize", "captures", "refunds", "reduce", "reauthorize", "void"} {
		if f.Effects(op) != 1 {
			t.Errorf("effects(%s) = %d", op, f.Effects(op))
		}
	}
}

func TestDeclinedOperations(t *testing.T) {
	c, f := newClient(t)
	f.Decline("refunds", "ACCOUNT_CLOSED")
	_, err := c.Refund(context.Background(), "r1", "hold-1", 5)
	if provider.Code(err) != "ACCOUNT_CLOSED" {
		t.Fatalf("err = %v", err)
	}
}

func TestOutageAndServerErrorsAreUnavailable(t *testing.T) {
	c, f := newClient(t)
	f.SetDown(true)
	if _, err := c.Authorize(context.Background(), authReq("k1", "tok")); !errors.Is(err, provider.ErrUnavailable) {
		t.Fatalf("503 err = %v", err)
	}
	gone := httptest.NewServer(http.NotFoundHandler())
	gone.Close()
	dead, _ := paymentprovider.New(gone.URL, nil)
	if err := dead.Void(context.Background(), "v", "h"); !errors.Is(err, provider.ErrUnavailable) {
		t.Fatalf("connection refused err = %v", err)
	}
	slow := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { time.Sleep(300 * time.Millisecond) }))
	defer slow.Close()
	timed, _ := paymentprovider.New(slow.URL, &http.Client{Timeout: 50 * time.Millisecond})
	if err := timed.Void(context.Background(), "v", "h"); !errors.Is(err, provider.ErrUnavailable) {
		t.Fatalf("timeout err = %v", err)
	}
}

func TestOtherStatusesAreContractErrors(t *testing.T) {
	for status, wantUnavailable := range map[int]bool{http.StatusTooManyRequests: true, http.StatusBadRequest: false, http.StatusNotFound: false} {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(status) }))
		c, _ := paymentprovider.New(srv.URL, nil)
		err := c.Void(context.Background(), "v", "h")
		srv.Close()
		if err == nil || errors.Is(err, provider.ErrUnavailable) != wantUnavailable || errors.Is(err, provider.ErrDeclined) {
			t.Errorf("status %d err = %v", status, err)
		}
	}
}

func TestMalformedAnswers(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte(`{}`)) }))
	defer srv.Close()
	c, _ := paymentprovider.New(srv.URL, nil)
	if _, err := c.Authorize(context.Background(), authReq("k", "t")); err == nil {
		t.Error("a hold without id must fail")
	}
	if _, err := c.Capture(context.Background(), "k", "h", 1); err == nil {
		t.Error("a capture without id must fail")
	}
	bad := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte(`nope`)) }))
	defer bad.Close()
	c2, _ := paymentprovider.New(bad.URL, nil)
	if _, err := c2.Reauthorize(context.Background(), "k", "h", 1); err == nil {
		t.Error("undecodable answer must fail")
	}
}

func TestNewRejectsBadURLs(t *testing.T) {
	for _, u := range []string{"", "ftp://x", "localhost:8080", "http://"} {
		if _, err := paymentprovider.New(u, nil); err == nil {
			t.Errorf("New(%q) succeeded", u)
		}
	}
	if !strings.Contains(providerfake.DeclineToken, "declined") {
		t.Fatal("fixture drifted")
	}
}
