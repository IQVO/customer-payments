package kafka

import (
	"context"
	"errors"
	"log/slog"
	"time"

	ce "github.com/cloudevents/sdk-go/v2/event"

	"github.com/IQVO/customer-payments/internal/application/parked"
	"github.com/IQVO/customer-payments/internal/application/usecases"
	"github.com/IQVO/customer-payments/internal/domain/paymentauthorization"
	"github.com/IQVO/customer-payments/internal/domain/shared"
)

// Topics and types of the events customer-payments consumes (ADR 0003).
// Dispatch is on the full type string; the namespace of each type is the
// producer's own.
const (
	CheckoutTopic             = "retailnetwork.checkout.events"
	OrderSourcingTopic        = "retailnetwork.order-sourcing.events"
	ShippingAndTrackingTopic  = "retailnetwork.shipping-and-tracking.events"
	TypeCheckoutCompleted     = "com.retailnetwork.storefront.checkout.checkoutsession.CheckoutCompleted"
	TypeFulfillmentShipped    = "com.retailnetwork.commerce.order-sourcing.assignment.FulfillmentShipped"
	TypeFulfillmentCancelled  = "com.retailnetwork.commerce.order-sourcing.assignment.FulfillmentCancelled"
	TypeFulfillmentUnsourcing = "com.retailnetwork.commerce.order-sourcing.assignment.FulfillmentUnsourceable"
	TypePackageLost           = "com.retailnetwork.logistics.shipping-and-tracking.journey.PackageLost"
	TypePackageReturned       = "com.retailnetwork.logistics.shipping-and-tracking.journey.PackageReturned"
)

// The parts of the consumed payloads that are used; unknown fields are
// ignored.
type (
	checkoutCompletedData struct {
		SessionID       string `json:"sessionId"`
		OrderID         string `json:"orderId"`
		AuthorizationID string `json:"authorizationId"`
	}
	fulfillmentData struct {
		OrderID         string    `json:"orderId"`
		GroupNo         int       `json:"groupNo"`
		TrackingNumbers []string  `json:"trackingNumbers"`
		ShippedAt       time.Time `json:"shippedAt"`
	}
	packageData struct {
		OrderID        string `json:"orderId"`
		TrackingNumber string `json:"trackingNumber"`
	}
)

// NewCheckoutConsumer builds the consumer of checkout's event topic.
func NewCheckoutConsumer(brokers []string, groupID string, c usecases.Consumer, logger *slog.Logger) *Consumer {
	return newConsumer("checkout", brokers, CheckoutTopic, groupID, checkoutHandlers(c), logger)
}

// NewOrderSourcingConsumer builds the consumer of order-sourcing's event topic.
func NewOrderSourcingConsumer(brokers []string, groupID string, c usecases.Consumer, logger *slog.Logger) *Consumer {
	return newConsumer("order-sourcing", brokers, OrderSourcingTopic, groupID, orderSourcingHandlers(c), logger)
}

// NewShippingConsumer builds the consumer of shipping-and-tracking's event
// topic.
func NewShippingConsumer(brokers []string, groupID string, c usecases.Consumer, logger *slog.Logger) *Consumer {
	return newConsumer("shipping-and-tracking", brokers, ShippingAndTrackingTopic, groupID, shippingHandlers(c), logger)
}

func checkoutHandlers(c usecases.Consumer) map[string]HandlerFunc {
	return map[string]HandlerFunc{
		TypeCheckoutCompleted: func(ctx context.Context, e ce.Event) error {
			d, err := decodeData[checkoutCompletedData](e)
			if err != nil {
				return err
			}
			return classify(c.CheckoutCompleted(ctx, e.ID(), e.Type(), usecases.CheckoutCompletedEvent{
				SessionID: d.SessionID, OrderID: d.OrderID, AuthorizationID: d.AuthorizationID,
			}))
		},
	}
}

func fulfillmentHandler(c usecases.Consumer, kind parked.Kind) HandlerFunc {
	return func(ctx context.Context, e ce.Event) error {
		d, err := decodeData[fulfillmentData](e)
		if err != nil {
			return err
		}
		return classify(c.ApplyFact(ctx, e.ID(), e.Type(), usecases.Fact{
			Kind: kind, OrderID: d.OrderID, GroupNo: d.GroupNo, TrackingNumbers: d.TrackingNumbers, OccurredAt: e.Time().UTC(),
		}))
	}
}

func orderSourcingHandlers(c usecases.Consumer) map[string]HandlerFunc {
	return map[string]HandlerFunc{
		TypeFulfillmentShipped:    fulfillmentHandler(c, parked.Shipped),
		TypeFulfillmentCancelled:  fulfillmentHandler(c, parked.Cancelled),
		TypeFulfillmentUnsourcing: fulfillmentHandler(c, parked.Unsourceable),
	}
}

func packageHandler(c usecases.Consumer, kind parked.Kind) HandlerFunc {
	return func(ctx context.Context, e ce.Event) error {
		d, err := decodeData[packageData](e)
		if err != nil {
			return err
		}
		return classify(c.ApplyFact(ctx, e.ID(), e.Type(), usecases.Fact{
			Kind: kind, OrderID: d.OrderID, TrackingNumbers: []string{d.TrackingNumber}, OccurredAt: e.Time().UTC(),
		}))
	}
}

func shippingHandlers(c usecases.Consumer) map[string]HandlerFunc {
	return map[string]HandlerFunc{
		TypePackageLost:     packageHandler(c, parked.Lost),
		TypePackageReturned: packageHandler(c, parked.Returned),
	}
}

// classify turns an error that can never go away on a retry into a poison
// payload (dead-lettered at once): an invalid event, or a domain refusal of
// the fact (unknown group, capture after expiry, illegal transition, another
// order bound). Everything else, a provider outage included, is transient.
func classify(err error) error {
	switch {
	case err == nil:
		return nil
	case errors.Is(err, usecases.ErrInvalidEvent),
		errors.Is(err, paymentauthorization.ErrUnknownGroup),
		errors.Is(err, paymentauthorization.ErrCaptureAfterExpiry),
		errors.Is(err, paymentauthorization.ErrOrderAlreadyBound),
		errors.Is(err, shared.ErrIllegalTransition),
		errors.Is(err, shared.ErrValidation):
		return errors.Join(errBadPayload, err)
	}
	return err
}
