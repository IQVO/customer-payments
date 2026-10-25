// Package paymentauthorization is the PaymentAuthorization aggregate: the hold
// on a shopper's payment method for one checkout session, split into
// allocations per shipment group. Money moves per allocation: captured when
// its group ships, released when its group is cancelled or unsourceable.
//
// States are DERIVED from the allocations (H3): AUTHORIZED while nothing was
// captured and something is pending, PARTIALLY_CAPTURED while some are
// captured and some pending, CAPTURED when every allocation is captured or
// released with at least one captured, RELEASED when every allocation is
// released. VOIDED, EXPIRED and DECLINED are explicit terminal outcomes.
//
// Invariants, by the model's names: allocations sum to the authorized total
// and name distinct groups (ErrAllocationMismatch); a capture never exceeds
// its allocation (ErrOverCapture); no capture after expiresAt
// (ErrCaptureAfterExpiry); a capture or release names an existing group
// (ErrUnknownGroup); one authorization per checkout session
// (ErrDuplicateAuthorization, enforced by the repository on the session id).
package paymentauthorization

import (
	"errors"
	"slices"
	"time"
)

const entity = "paymentauthorization"

// The invariants of the model, by the model's names.
var (
	// ErrDuplicateAuthorization: one authorization per checkout session (409
	// duplicate-authorization). The use case answers a replay with the
	// existing authorization instead.
	ErrDuplicateAuthorization = errors.New("duplicate authorization")
	// ErrAllocationMismatch: allocations sum to the total and name distinct
	// groups (422 allocation-mismatch).
	ErrAllocationMismatch = errors.New("allocation mismatch")
	// ErrOverCapture: the captured amount of a group never exceeds its
	// allocation (409 over-capture).
	ErrOverCapture = errors.New("over capture")
	// ErrCaptureAfterExpiry: no capture after expiresAt (409
	// capture-after-expiry).
	ErrCaptureAfterExpiry = errors.New("capture after expiry")
	// ErrUnknownGroup: a capture or release names a group the authorization
	// has no allocation for (422 unknown-group).
	ErrUnknownGroup = errors.New("unknown group")
	// ErrOrderAlreadyBound: the authorization is bound to another order.
	ErrOrderAlreadyBound = errors.New("order already bound")
)

// MaxGroups is the most allocations one authorization holds (ADR 0002, A7).
const MaxGroups = 50

// MaxTokenLen is the longest accepted PSP token.
const MaxTokenLen = 256

// ID identifies an authorization.
type ID string

// State is the state of an authorization, derived except for the terminal
// outcomes.
type State string

// The seven states.
const (
	StateAuthorized        State = "AUTHORIZED"
	StatePartiallyCaptured State = "PARTIALLY_CAPTURED"
	StateCaptured          State = "CAPTURED"
	StateReleased          State = "RELEASED"
	StateVoided            State = "VOIDED"
	StateExpired           State = "EXPIRED"
	StateDeclined          State = "DECLINED"
)

// Valid reports whether s is one of the seven states.
func (s State) Valid() bool {
	switch s {
	case StateAuthorized, StatePartiallyCaptured, StateCaptured, StateReleased, StateVoided, StateExpired, StateDeclined:
		return true
	}
	return false
}

// AllocState is the state of one allocation.
type AllocState string

// The three allocation states.
const (
	AllocPending  AllocState = "PENDING"
	AllocCaptured AllocState = "CAPTURED"
	AllocReleased AllocState = "RELEASED"
)

// Allocation is the part of the authorization that belongs to one shipment
// group.
type Allocation struct {
	GroupNo       int
	AmountMinor   int64
	State         AllocState
	CapturedMinor int64
	// TrackingNumbers are set at capture (H6), one per package of the group.
	TrackingNumbers   []string
	ProviderCaptureID string
}

// TrackingNumber returns the first tracking number, or "" before capture.
func (a Allocation) TrackingNumber() string {
	if len(a.TrackingNumbers) == 0 {
		return ""
	}
	return a.TrackingNumbers[0]
}

// AllocationInput names one group and its share of the total.
type AllocationInput struct {
	GroupNo     int
	AmountMinor int64
}

// Draft is the validated request of AuthorizePayment.
type Draft struct {
	ID                 ID
	CheckoutSessionID  string
	PaymentMethodToken string
	Currency           string
	TotalMinor         int64
	Allocations        []AllocationInput
}

// PaymentAuthorization is the aggregate root.
type PaymentAuthorization struct {
	id                ID
	sessionID         string
	orderID           string
	token             string
	currency          string
	total             int64
	allocations       []Allocation
	terminal          State
	declineReason     string
	providerAuthID    string
	expiresAt         time.Time
	createdAt         time.Time
	reauthorizedCount int
	version           int64
	loaded            int64
}

// ID returns the authorizationId.
func (p *PaymentAuthorization) ID() ID { return p.id }

// CheckoutSessionID returns the checkout session the hold belongs to.
func (p *PaymentAuthorization) CheckoutSessionID() string { return p.sessionID }

// OrderID returns the bound order, "" before CheckoutCompleted.
func (p *PaymentAuthorization) OrderID() string { return p.orderID }

// PaymentMethodToken returns the PSP token the hold was made with.
func (p *PaymentAuthorization) PaymentMethodToken() string { return p.token }

// Currency returns the ISO 4217 currency.
func (p *PaymentAuthorization) Currency() string { return p.currency }

// TotalMinor returns the authorized total in minor units.
func (p *PaymentAuthorization) TotalMinor() int64 { return p.total }

// ProviderAuthorizationID returns the provider's reference of the hold.
func (p *PaymentAuthorization) ProviderAuthorizationID() string { return p.providerAuthID }

// ExpiresAt returns when the provider's hold lapses.
func (p *PaymentAuthorization) ExpiresAt() time.Time { return p.expiresAt }

// CreatedAt returns when the authorization was made.
func (p *PaymentAuthorization) CreatedAt() time.Time { return p.createdAt }

// DeclineReason returns why a DECLINED authorization was declined.
func (p *PaymentAuthorization) DeclineReason() string { return p.declineReason }

// Reauthorizations returns how often the hold was renewed.
func (p *PaymentAuthorization) Reauthorizations() int { return p.reauthorizedCount }

// Version returns the aggregate version; every change adds one.
func (p *PaymentAuthorization) Version() int64 { return p.version }

// LoadedVersion returns the version the aggregate had when it was loaded (0
// for a new one): the version a repository expects to find stored.
func (p *PaymentAuthorization) LoadedVersion() int64 { return p.loaded }

// MarkPersisted records that the current version is stored.
func (p *PaymentAuthorization) MarkPersisted() { p.loaded = p.version }

// Allocations returns a copy of the allocations, by group number.
func (p *PaymentAuthorization) Allocations() []Allocation {
	out := slices.Clone(p.allocations)
	for i := range out {
		out[i].TrackingNumbers = slices.Clone(out[i].TrackingNumbers)
	}
	return out
}
