package kafka

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	kafkago "github.com/segmentio/kafka-go"

	"github.com/IQVO/customer-payments/internal/application/parked"
	"github.com/IQVO/customer-payments/internal/application/ports"
)

// UnboundFactsDLQ is the dead-letter topic of facts whose order stayed unbound
// past 24 h (gap S4): they are never dropped silently.
const UnboundFactsDLQ = "retailnetwork.customer-payments.unbound-facts.dlq"

// Escalator implements ports.Escalator: it publishes the parked fact to
// UnboundFactsDLQ, keyed by the order id. The writer behind it is topic-less.
type Escalator struct{ W Writer }

var _ ports.Escalator = Escalator{}

type escalatedFact struct {
	EventID         string    `json:"eventId"`
	Kind            string    `json:"kind"`
	OrderID         string    `json:"orderId"`
	GroupNo         int       `json:"groupNo,omitempty"`
	TrackingNumbers []string  `json:"trackingNumbers,omitempty"`
	OccurredAt      time.Time `json:"occurredAt"`
	ParkedAt        time.Time `json:"parkedAt"`
	Reason          string    `json:"reason"`
}

// Escalate implements ports.Escalator.
func (e Escalator) Escalate(ctx context.Context, f parked.Fact) error {
	value, err := json.Marshal(escalatedFact{
		EventID: f.EventID, Kind: string(f.Kind), OrderID: f.OrderID, GroupNo: f.GroupNo, TrackingNumbers: f.TrackingNumbers,
		OccurredAt: f.OccurredAt.UTC(), ParkedAt: f.ParkedAt.UTC(), Reason: "order never bound to an authorization within 24h",
	})
	if err != nil {
		return fmt.Errorf("encode escalated fact: %w", err)
	}
	if err := e.W.WriteMessages(ctx, kafkago.Message{Topic: UnboundFactsDLQ, Key: []byte(f.OrderID), Value: value}); err != nil {
		return fmt.Errorf("dead-letter fact %s: %w", f.EventID, err)
	}
	return nil
}
