package bdd

import (
	"context"
	"fmt"
	"net/http"

	"github.com/cucumber/godog"

	"github.com/IQVO/customer-payments/internal/application/usecases"
)

func initializeScenario(sc *godog.ScenarioContext) {
	var w *world
	sc.Before(func(ctx context.Context, _ *godog.Scenario) (context.Context, error) {
		w = newWorld()
		return ctx, nil
	})
	sc.After(func(ctx context.Context, _ *godog.Scenario, _ error) (context.Context, error) {
		w.srv.Close()
		return ctx, nil
	})
	sc.Step(`^(?:the shopper's )?checkout session "([^"]*)" is authorized for (\d+) BRL as (\d+) for group 1(?: and (\d+) for group 2)?$`,
		func(session string, total, a1 int, a2 string) error { return w.authorize(session, total, a1, a2) })
	sc.Step(`^checkout completed for session "([^"]*)" as order "([^"]*)"$`,
		func(session, order string) error { return w.checkoutCompleted(session, order) })
	sc.Step(`^the authorization is voided$`, func() error {
		return w.do(http.MethodPost, "/payment-authorizations/"+w.authID+":void", "")
	})
	sc.Step(`^the response status is (\d+)$`, func(want int) error {
		if w.status != want {
			return fmt.Errorf("status = %d, want %d: %s", w.status, want, w.body)
		}
		return nil
	})
	sc.Step(`^the problem type is "([^"]*)"$`, func(slug string) error {
		if got := w.json()["type"]; got != "/problems/"+slug {
			return fmt.Errorf("problem type = %v, want /problems/%s", got, slug)
		}
		return nil
	})
	sc.Step(`^the authorization state is "([^"]*)"$`, func(want string) error { return w.wantState(want) })
	sc.Step(`^the authorization of order "([^"]*)" is "([^"]*)"$`, func(_, want string) error { return w.wantState(want) })
	sc.Step(`^allocation of group (\d+) is "([^"]*)"$`, func(group int, want string) error { return w.wantAllocation(group, want) })
	sc.Step(`^the authorization id is the same as the first one$`, func() error {
		if w.authID != w.firstAuthID {
			return fmt.Errorf("replay minted %s, first was %s", w.authID, w.firstAuthID)
		}
		return nil
	})
	sc.Step(`^the provider authorized exactly (\d+) holds?$`, func(want int) error {
		if got := w.provider.Effects("authorize"); got != want {
			return fmt.Errorf("provider authorized %d holds, want %d", got, want)
		}
		return nil
	})
	registerFactSteps(sc, func() *world { return w })
}

func (w *world) authorize(session string, total, a1 int, a2 string) error {
	allocs := fmt.Sprintf(`{"groupNo":1,"amountMinor":%d}`, a1)
	if a2 != "" {
		allocs += fmt.Sprintf(`,{"groupNo":2,"amountMinor":%s}`, a2)
	}
	body := fmt.Sprintf(`{"checkoutSessionId":%q,"paymentMethodToken":"tok_visa","currency":"BRL","totalMinor":%d,"allocations":[%s]}`, session, total, allocs)
	if err := w.do(http.MethodPost, "/payment-authorizations", body); err != nil {
		return err
	}
	if id, ok := w.json()["authorizationId"].(string); ok {
		if w.firstAuthID == "" {
			w.firstAuthID = id
		}
		w.authID = id
	}
	return nil
}

func (w *world) checkoutCompleted(session, order string) error {
	ev := usecases.CheckoutCompletedEvent{SessionID: session, OrderID: order, AuthorizationID: w.authID}
	return w.consumer().CheckoutCompleted(context.Background(), w.eventID(), "checkout-completed", ev)
}

// current reads the authorization back, so facts applied by consumers show.
func (w *world) current() (map[string]any, error) {
	if err := w.do(http.MethodGet, "/payment-authorizations/"+w.authID, ""); err != nil {
		return nil, err
	}
	return w.json(), nil
}

func (w *world) wantState(want string) error {
	m, err := w.current()
	if err != nil {
		return err
	}
	if m["state"] != want {
		return fmt.Errorf("state = %v, want %s: %s", m["state"], want, w.body)
	}
	return nil
}

func (w *world) wantAllocation(group int, want string) error {
	m, err := w.current()
	if err != nil {
		return err
	}
	allocs, _ := m["allocations"].([]any)
	for _, a := range allocs {
		if row, _ := a.(map[string]any); row["groupNo"] == float64(group) {
			if row["state"] != want {
				return fmt.Errorf("group %d is %v, want %s", group, row["state"], want)
			}
			return nil
		}
	}
	return fmt.Errorf("group %d not found in %s", group, w.body)
}
