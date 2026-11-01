//go:build integration

package main

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/IQVO/customer-payments/internal/testing/kafkatest"
	"github.com/IQVO/customer-payments/internal/testing/pgtest"
	"github.com/IQVO/customer-payments/internal/testing/providerfake"
)

const (
	typeCheckoutCompleted = "com.retailnetwork.storefront.checkout.checkoutsession.CheckoutCompleted"
	typeShipped           = "com.retailnetwork.commerce.order-sourcing.assignment.FulfillmentShipped"
	typeCancelled         = "com.retailnetwork.commerce.order-sourcing.assignment.FulfillmentCancelled"
	typePackageLost       = "com.retailnetwork.logistics.shipping-and-tracking.journey.PackageLost"
)

// bootReal boots the real entry point under consumer groups of its own, so a
// test never inherits the committed offsets of another, against the local
// fake of the payment provider API.
func bootReal(t *testing.T, group string) (*service, *providerfake.Fake) {
	for _, topic := range []string{topicEvents, topicCheckout, topicSourcing, topicShipping} {
		kafkatest.CreateTopic(t, topic)
	}
	fake := providerfake.New(t)
	return boot(t, map[string]string{
		"DATABASE_URL": pgtest.URL(t), "KAFKA_BROKERS": strings.Join(kafkatest.Brokers(t), ","),
		"PAYMENT_PROVIDER_URL":                 fake.URL(),
		"CHECKOUT_CONSUMER_GROUP":              "customer-payments.e2e.checkout." + group,
		"ORDER_SOURCING_CONSUMER_GROUP":        "customer-payments.e2e.sourcing." + group,
		"SHIPPING_AND_TRACKING_CONSUMER_GROUP": "customer-payments.e2e.shipping." + group,
		"OUTBOX_RELAY_INTERVAL":                "100ms", "POLICY_INTERVAL": "200ms", "SHUTDOWN_DRAIN_DELAY": "0",
	}), fake
}

func authorizeBody(session string) string {
	return fmt.Sprintf(`{"checkoutSessionId":%q,"paymentMethodToken":"tok_visa","currency":"BRL","totalMinor":10000,`+
		`"allocations":[{"groupNo":1,"amountMinor":7000},{"groupNo":2,"amountMinor":3000}]}`, session)
}

func authorize(t *testing.T, s *service, session string) string {
	t.Helper()
	r := s.call(t, "POST", "/payment-authorizations", "auth-"+session, authorizeBody(session))
	if r.status != 201 {
		t.Fatalf("authorize: %d %s", r.status, r.body)
	}
	return r.field(t, "authorizationId").(string)
}

func completed(id, session, authID, order string) kafkatest.Message {
	return cloudEvent(id, typeCheckoutCompleted, session, map[string]any{"sessionId": session, "orderId": order, "authorizationId": authID})
}

func shipped(id, order string, group int, tracking string) kafkatest.Message {
	return cloudEvent(id, typeShipped, "asg-"+order, map[string]any{"orderId": order, "groupNo": group, "trackingNumbers": []string{tracking}})
}

func stateOf(t *testing.T, s *service, id string) any {
	t.Helper()
	return s.call(t, "GET", "/payment-authorizations/"+id, "", "").field(t, "state")
}

func TestRealWiringAuthorizePublishesAndReplays(t *testing.T) {
	s, fake := bootReal(t, "authorize")
	id := authorize(t, s, "cs-a")
	msgs := kafkatest.ReadKeyed(t, topicEvents, id, 1, 60*time.Second)
	if !strings.Contains(string(msgs[0].Value), "com.retailnetwork.storefront.customer-payments.paymentauthorization.PaymentAuthorized") {
		t.Fatalf("published event = %s", msgs[0].Value)
	}
	again := s.call(t, "POST", "/payment-authorizations", "auth-other-key", authorizeBody("cs-a"))
	if again.status != 200 || again.field(t, "authorizationId") != id || fake.Effects("authorize") != 1 {
		t.Fatalf("replay = %d %s (effects %d)", again.status, again.body, fake.Effects("authorize"))
	}
}

func TestRealWiringShipmentCapturesAndLostPackageRefunds(t *testing.T) {
	s, _ := bootReal(t, "capture")
	id := authorize(t, s, "cs-c")
	kafkatest.Produce(t, topicCheckout, completed("evt-cc-c", "cs-c", id, "ord-c"))
	kafkatest.Produce(t, topicSourcing, shipped("evt-s-c", "ord-c", 1, "TRK-C1"))
	eventually(t, 60*time.Second, "group 1 to be captured", func() bool { return stateOf(t, s, id) == "PARTIALLY_CAPTURED" })
	captured := kafkatest.ReadKeyed(t, topicEvents, id, 2, 60*time.Second)
	if !strings.Contains(string(captured[1].Value), "PaymentCaptured") {
		t.Fatalf("second event = %s", captured[1].Value)
	}
	kafkatest.Produce(t, topicShipping, cloudEvent("evt-l-c", typePackageLost, "TRK-C1", map[string]any{"orderId": "ord-c", "trackingNumber": "TRK-C1"}))
	eventually(t, 60*time.Second, "the lost package to be refunded", func() bool {
		return strings.Contains(s.call(t, "GET", "/refunds?orderId=ord-c", "", "").body, `"state":"SUCCEEDED"`)
	})
}

func TestRealWiringFactBeforeBindingIsParkedThenApplied(t *testing.T) {
	s, _ := bootReal(t, "park")
	id := authorize(t, s, "cs-p")
	kafkatest.Produce(t, topicSourcing, cloudEvent("evt-x-p", typeCancelled, "asg-p", map[string]any{"orderId": "ord-p", "groupNo": 2, "reason": "CUSTOMER_REQUEST"}))
	kafkatest.Produce(t, topicCheckout, completed("evt-cc-p", "cs-p", id, "ord-p"))
	eventually(t, 60*time.Second, "the parked cancellation to be applied", func() bool {
		return strings.Contains(s.call(t, "GET", "/payment-authorizations/"+id, "", "").body, `"state":"RELEASED"`) ||
			strings.Contains(s.call(t, "GET", "/payment-authorizations/"+id, "", "").body, `"state":"AUTHORIZED","currency":"BRL","totalMinor":10000,"allocations":[{"groupNo":1,"amountMinor":7000,"state":"PENDING"`) &&
				strings.Contains(s.call(t, "GET", "/payment-authorizations/"+id, "", "").body, `"groupNo":2,"amountMinor":3000,"state":"RELEASED"`)
	})
	// The topics are shared by the tests of this binary, so facts of other tests
	// may be parked too: the gauge is only proven to be exposed.
	if body := s.call(t, "GET", "/metrics", "", "").body; !strings.Contains(body, "customer_payments_parked_facts ") {
		t.Fatalf("metrics = %q", body)
	}
}

func TestRealWiringVoidReleasesTheHold(t *testing.T) {
	s, fake := bootReal(t, "void")
	id := authorize(t, s, "cs-v")
	r := s.call(t, "POST", "/payment-authorizations/"+id+"/voids", "void-1", "")
	if r.status != 200 || r.field(t, "state") != "VOIDED" || fake.Effects("void") != 1 {
		t.Fatalf("void = %d %s", r.status, r.body)
	}
	msgs := kafkatest.ReadKeyed(t, topicEvents, id, 3, 60*time.Second)
	if !strings.Contains(string(msgs[2].Value), `"reason":"VOIDED"`) {
		t.Fatalf("third event = %s", msgs[2].Value)
	}
}
