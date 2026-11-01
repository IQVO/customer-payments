package bdd

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"

	"github.com/cucumber/godog"

	"github.com/IQVO/customer-payments/internal/application/parked"
	"github.com/IQVO/customer-payments/internal/application/usecases"
)

func registerFactSteps(sc *godog.ScenarioContext, get func() *world) {
	sc.Step(`^group (\d+) of order "([^"]*)" ships with tracking number "([^"]*)"$`, func(g int, order, trk string) error {
		return get().fact(usecases.Fact{Kind: parked.Shipped, OrderID: order, GroupNo: g, TrackingNumbers: []string{trk}})
	})
	sc.Step(`^group (\d+) of order "([^"]*)" is cancelled$`, func(g int, order string) error {
		return get().fact(usecases.Fact{Kind: parked.Cancelled, OrderID: order, GroupNo: g})
	})
	sc.Step(`^group (\d+) of order "([^"]*)" is unsourceable$`, func(g int, order string) error {
		return get().fact(usecases.Fact{Kind: parked.Unsourceable, OrderID: order, GroupNo: g})
	})
	sc.Step(`^package "([^"]*)" of order "([^"]*)" is lost$`, func(trk, order string) error {
		return get().fact(usecases.Fact{Kind: parked.Lost, OrderID: order, TrackingNumbers: []string{trk}})
	})
	sc.Step(`^package "([^"]*)" of order "([^"]*)" is returned to sender$`, func(trk, order string) error {
		return get().fact(usecases.Fact{Kind: parked.Returned, OrderID: order, TrackingNumbers: []string{trk}})
	})
	sc.Step(`^a manual refund of (\d+) is requested for order "([^"]*)"$`, func(amount int, order string) error {
		w := get()
		body := fmt.Sprintf(`{"orderId":%q,"amountMinor":%d,"reason":"MANUAL"}`, order, amount)
		return w.do(http.MethodPost, "/refunds", body)
	})
	sc.Step(`^a refund of (\d+) for reason "([^"]*)" is "([^"]*)"$`, func(amount int, reason, state string) error {
		return get().wantRefund(amount, reason, state)
	})
}

func (w *world) fact(f usecases.Fact) error {
	w.lastOrder = f.OrderID
	return w.consumer().ApplyFact(context.Background(), w.eventID(), string(f.Kind), f)
}

func (w *world) wantRefund(amount int, reason, state string) error {
	if err := w.do(http.MethodGet, "/refunds?orderId="+w.lastOrder, ""); err != nil {
		return err
	}
	var list struct {
		Items []struct {
			AmountMinor int    `json:"amountMinor"`
			Reason      string `json:"reason"`
			State       string `json:"state"`
		} `json:"items"`
	}
	if err := json.Unmarshal([]byte(w.body), &list); err != nil {
		return err
	}
	for _, r := range list.Items {
		if r.AmountMinor == amount && r.Reason == reason && r.State == state {
			return nil
		}
	}
	return fmt.Errorf("no %s refund of %d (%s) in %s", reason, amount, state, w.body)
}
