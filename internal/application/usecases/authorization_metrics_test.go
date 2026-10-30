package usecases_test

import (
	"context"
	"fmt"
	"testing"

	"github.com/IQVO/customer-payments/internal/application/provider"
	"github.com/IQVO/customer-payments/internal/application/usecases"
)

// recorder is a ports.PaymentMetrics that remembers each call as "outcome/reason".
type recorder struct{ got []string }

func (r *recorder) AuthorizationAuthorized(context.Context) { r.got = append(r.got, "authorized/") }
func (r *recorder) AuthorizationDeclined(_ context.Context, reason string) {
	r.got = append(r.got, "declined/"+reason)
}
func (r *recorder) AuthorizationFailed(_ context.Context, reason string) {
	r.got = append(r.got, "failed/"+reason)
}

func (e *env) authorizeRecorded(in usecases.AuthorizePaymentInput) []string {
	rec := &recorder{}
	e.deps.Metrics = rec
	_, _ = usecases.AuthorizePayment{Deps: e.deps}.Execute(context.Background(), in)
	return rec.got
}

func TestAuthorizeRecordsExactlyOneOutcomePerAttempt(t *testing.T) {
	bad := authorizeInput()
	bad.TotalMinor = 9999
	cases := []struct {
		name  string
		setup func(*env)
		in    usecases.AuthorizePaymentInput
		want  string
	}{
		{"authorized", func(*env) {}, authorizeInput(), "authorized/"},
		{"declined", func(e *env) { e.provider.Fail("authorize", provider.Declined("CARD_DECLINED")) }, authorizeInput(), "declined/CARD_DECLINED"},
		{"provider down", func(e *env) { e.provider.Fail("authorize", unavailable()) }, authorizeInput(), "failed/payment-provider-unavailable"},
		{"allocation mismatch", func(*env) {}, bad, "failed/allocation-mismatch"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			e := newEnv(t)
			c.setup(e)
			if got := fmt.Sprint(e.authorizeRecorded(c.in)); got != fmt.Sprint([]string{c.want}) {
				t.Errorf("recorded %s, want [%s]", got, c.want)
			}
		})
	}
}

func TestAuthorizeReplayIsNotCounted(t *testing.T) {
	e := newEnv(t)
	e.authorized()
	if got := e.authorizeRecorded(authorizeInput()); len(got) != 0 {
		t.Errorf("a replay recorded %v, want nothing", got)
	}
}

func TestAuthorizeWithoutMetricsWiredDoesNotPanic(t *testing.T) {
	e := newEnv(t)
	e.authorized()
}
