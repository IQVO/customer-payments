package kafka_test

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"testing"
	"time"

	ce "github.com/cloudevents/sdk-go/v2/event"
	kafkago "github.com/segmentio/kafka-go"

	kafkaconsumer "github.com/IQVO/customer-payments/internal/adapters/inbound/kafka"
	"github.com/IQVO/customer-payments/internal/adapters/outbound/memory"
	"github.com/IQVO/customer-payments/internal/application/parked"
	"github.com/IQVO/customer-payments/internal/application/usecases"
	"github.com/IQVO/customer-payments/internal/domain/paymentauthorization"
)

var t0 = time.Date(2026, 10, 25, 10, 0, 0, 0, time.UTC)

type rig struct {
	t        *testing.T
	store    *memory.Store
	provider *memory.Provider
	deps     usecases.Deps
	checkout *kafkaconsumer.Consumer
	sourcing *kafkaconsumer.Consumer
	shipping *kafkaconsumer.Consumer
}

type noDLQ struct{}

func (noDLQ) WriteMessages(context.Context, ...kafkago.Message) error { return nil }
func (noDLQ) Close() error                                            { return nil }

type noEscalator struct{}

func (noEscalator) Escalate(context.Context, parked.Fact) error { return nil }

func newRig(t *testing.T) *rig {
	t.Helper()
	clock := memory.NewClock(t0)
	s := memory.NewStore(clock)
	prov := memory.NewProvider(clock)
	deps := usecases.Deps{
		Authorizations: s.Authorizations(), Refunds: s.Refunds(), Parked: s.Parked(), Inbox: s.Inbox(), Outbox: s.Outbox(),
		UoW: s.UnitOfWork(), Clock: clock, IDs: &memory.SequentialIDs{}, Provider: prov, Escalator: noEscalator{},
	}
	c := usecases.Consumer{Deps: deps}
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	mk := func(name, topic string, h map[string]kafkaconsumer.HandlerFunc) *kafkaconsumer.Consumer {
		return &kafkaconsumer.Consumer{Name: name, Topic: topic, Handlers: h, Logger: logger, DeadLetterInvalid: true, DLQ: noDLQ{}}
	}
	return &rig{
		t: t, store: s, provider: prov, deps: deps,
		checkout: mk("checkout", kafkaconsumer.CheckoutTopic, kafkaconsumer.CheckoutHandlers(c)),
		sourcing: mk("order-sourcing", kafkaconsumer.OrderSourcingTopic, kafkaconsumer.OrderSourcingHandlers(c)),
		shipping: mk("shipping", kafkaconsumer.ShippingAndTrackingTopic, kafkaconsumer.ShippingHandlers(c)),
	}
}

// message builds a structured-mode CloudEvent of the given type.
func message(t *testing.T, id, typ string, data any) []byte {
	t.Helper()
	e := ce.New()
	e.SetID(id)
	e.SetSource("/retailnetwork/producer")
	e.SetType(typ)
	e.SetSubject("subject-1")
	e.SetTime(t0)
	if err := e.SetData(ce.ApplicationJSON, data); err != nil {
		t.Fatal(err)
	}
	b, err := json.Marshal(e)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func (r *rig) authorize() string {
	r.t.Helper()
	res, err := usecases.AuthorizePayment{Deps: r.deps}.Execute(context.Background(), usecases.AuthorizePaymentInput{
		CheckoutSessionID: "cs-1", PaymentMethodToken: "tok", Currency: "BRL", TotalMinor: 10000,
		Allocations: []paymentauthorization.AllocationInput{{GroupNo: 1, AmountMinor: 7000}, {GroupNo: 2, AmountMinor: 3000}},
	})
	if err != nil {
		r.t.Fatal(err)
	}
	return res.Authorization.AuthorizationID
}

func (r *rig) state(id string) string {
	r.t.Helper()
	v, err := usecases.GetAuthorization{Deps: r.deps}.Execute(context.Background(), id)
	if err != nil {
		r.t.Fatal(err)
	}
	return v.State
}

func (r *rig) handle(c *kafkaconsumer.Consumer, msg []byte) error {
	return c.HandleMessage(context.Background(), msg)
}

func completed(t *testing.T, id, authID string) []byte {
	return message(t, id, kafkaconsumer.TypeCheckoutCompleted, map[string]any{
		"sessionId": "cs-1", "orderId": "ord-1", "authorizationId": authID, "shopperId": "ignored",
	})
}

func shipped(t *testing.T, id string, group int, tracking ...string) []byte {
	return message(t, id, kafkaconsumer.TypeFulfillmentShipped, map[string]any{
		"assignmentId": "asg-1", "orderId": "ord-1", "groupNo": group, "trackingNumbers": tracking,
	})
}
