package bdd

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/cucumber/godog"

	"github.com/IQVO/customer-payments/internal/application/parked"
	"github.com/IQVO/customer-payments/internal/application/usecases"
)

// steps_rules_test.go — vocabulary of payment_rules.feature: facts delivered
// under a chosen CloudEvents id (inbox redelivery), facts the consumer
// refuses, a void under an explicit Idempotency-Key and a read-back of the
// scenario's authorization.

func registerRuleSteps(sc *godog.ScenarioContext, get func() *world) {
	sc.Step(`^group (\d+) of order "([^"]*)" ships with tracking number "([^"]*)" in event "([^"]*)"$`,
		func(g int, order, trk, ev string) error {
			return get().factAs(ev, usecases.Fact{Kind: parked.Shipped, OrderID: order, GroupNo: g, TrackingNumbers: []string{trk}})
		})
	sc.Step(`^package "([^"]*)" of order "([^"]*)" is lost in event "([^"]*)"$`, func(trk, order, ev string) error {
		return get().factAs(ev, usecases.Fact{Kind: parked.Lost, OrderID: order, TrackingNumbers: []string{trk}})
	})
	sc.Step(`^group (\d+) of order "([^"]*)" ships with tracking number "([^"]*)" but the fact is refused$`,
		func(g int, order, trk string) error {
			return get().factRefused(usecases.Fact{Kind: parked.Shipped, OrderID: order, GroupNo: g, TrackingNumbers: []string{trk}})
		})
	sc.Step(`^group (\d+) of order "([^"]*)" is cancelled but the fact is refused$`, func(g int, order string) error {
		return get().factRefused(usecases.Fact{Kind: parked.Cancelled, OrderID: order, GroupNo: g})
	})
	sc.Step(`^the authorization is voided with key "([^"]+)"$`, func(key string) error {
		w := get()
		return w.doKey(http.MethodPost, "/payment-authorizations/"+w.authID+"/voids", key, "")
	})
	sc.Step(`^a POST request is sent to "([^"]+)" without a key and the JSON body (.*)$`, func(path, body string) error {
		w := get()
		resp, err := http.Post(w.srv.URL+path, "application/json", strings.NewReader(body))
		if err != nil {
			return err
		}
		defer func() { _ = resp.Body.Close() }()
		b, _ := io.ReadAll(resp.Body)
		w.status, w.body, w.header = resp.StatusCode, string(b), resp.Header
		return nil
	})
	sc.Step(`^the authorization is read back$`, func() error {
		w := get()
		return w.do(http.MethodGet, "/payment-authorizations/"+w.authID, "")
	})
}

// factAs delivers a fact under a chosen CloudEvents id, so a second call with
// the same id is a redelivery the inbox must absorb.
func (w *world) factAs(eventID string, f usecases.Fact) error {
	w.lastOrder = f.OrderID
	return w.consumer().ApplyFact(context.Background(), eventID, string(f.Kind), f)
}

// factRefused delivers a fact the aggregate or the provider cannot honour: the
// consumer has no caller to answer, so the scenario asserts on the resulting
// state; the step only requires that the delivery itself did not panic.
func (w *world) factRefused(f usecases.Fact) error {
	w.lastOrder = f.OrderID
	w.policyErr = w.consumer().ApplyFact(context.Background(), w.eventID(), string(f.Kind), f)
	if w.policyErr != nil {
		w.policyErr = fmt.Errorf("refused: %w", w.policyErr)
	}
	return nil
}
