// Package parked holds the ParkedFulfillmentFacts read model's row: a
// fulfilment or tracking fact whose order is not yet bound to an
// authorization (cross-topic order, gap S4). It is data only; the port that
// stores it is ports.ParkedFacts.
package parked

import "time"

// Kind names the fact.
type Kind string

// The five facts customer-payments reacts to.
const (
	Shipped      Kind = "SHIPPED"
	Cancelled    Kind = "CANCELLED"
	Unsourceable Kind = "UNSOURCEABLE"
	Lost         Kind = "LOST"
	Returned     Kind = "RETURNED"
)

// Retention is how long a parked fact is kept before it is purged (H10).
const Retention = 30 * 24 * time.Hour

// EscalateAfter is how long a fact may stay parked before it is
// dead-lettered with an alert (S4: the 24 h binding capacity).
const EscalateAfter = 24 * time.Hour

// Fact is one parked fact.
type Fact struct {
	// EventID is the CloudEvents id of the consumed event.
	EventID string
	Kind    Kind
	OrderID string
	// GroupNo is set for SHIPPED, CANCELLED and UNSOURCEABLE.
	GroupNo int
	// TrackingNumbers: every package for SHIPPED, the one package for LOST and
	// RETURNED.
	TrackingNumbers []string
	OccurredAt      time.Time
	ParkedAt        time.Time
	// EscalatedAt is set once the fact was dead-lettered with an alert.
	EscalatedAt time.Time
}
