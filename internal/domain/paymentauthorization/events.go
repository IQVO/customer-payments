package paymentauthorization

import (
	"time"

	"github.com/IQVO/customer-payments/internal/domain/shared"
)

// The reasons of PaymentReleased (H5, H1).
const (
	ReasonCancelled    = "CANCELLED"
	ReasonUnsourceable = "UNSOURCEABLE"
	ReasonVoided       = "VOIDED"
	ReasonExpired      = "EXPIRED"
)

// ReasonReauthDeclined is the PaymentDeclined reason of a declined
// re-authorization (H2).
const ReasonReauthDeclined = "REAUTH_DECLINED"

// AllocationAmount is one allocation as PaymentAuthorized publishes it.
type AllocationAmount struct {
	GroupNo     int
	AmountMinor int64
}

// PaymentAuthorized is raised when the provider approved the hold.
type PaymentAuthorized struct {
	shared.Header
	AuthorizationID   ID
	CheckoutSessionID string
	Currency          string
	TotalMinor        int64
	Allocations       []AllocationAmount
	ExpiresAt         time.Time
}

// PaymentCaptured is raised when a group's allocation was captured.
type PaymentCaptured struct {
	shared.Header
	AuthorizationID ID
	OrderID         string
	GroupNo         int
	AmountMinor     int64
}

// PaymentReleased is raised when an allocation was let go (reason: one of the
// Reason constants).
type PaymentReleased struct {
	shared.Header
	AuthorizationID ID
	OrderID         string
	GroupNo         int
	AmountMinor     int64
	Reason          string
}

// PaymentAuthorizationExpired is raised when the hold lapsed with money still
// uncaptured.
type PaymentAuthorizationExpired struct {
	shared.Header
	AuthorizationID ID
	OrderID         string
	ExpiredAt       time.Time
}

// PaymentDeclined is raised when the provider declined the authorization or a
// re-authorization (H7: it names the authorization).
type PaymentDeclined struct {
	shared.Header
	AuthorizationID   ID
	CheckoutSessionID string
	Reason            string
}

// PaymentReauthorized is raised when the hold was renewed (H1).
type PaymentReauthorized struct {
	shared.Header
	AuthorizationID         ID
	OrderID                 string
	ExpiresAt               time.Time
	ProviderAuthorizationID string
}

// EventName returns "PaymentAuthorized".
func (PaymentAuthorized) EventName() string { return "PaymentAuthorized" }

// EventName returns "PaymentCaptured".
func (PaymentCaptured) EventName() string { return "PaymentCaptured" }

// EventName returns "PaymentReleased".
func (PaymentReleased) EventName() string { return "PaymentReleased" }

// EventName returns "PaymentAuthorizationExpired".
func (PaymentAuthorizationExpired) EventName() string { return "PaymentAuthorizationExpired" }

// EventName returns "PaymentDeclined".
func (PaymentDeclined) EventName() string { return "PaymentDeclined" }

// EventName returns "PaymentReauthorized".
func (PaymentReauthorized) EventName() string { return "PaymentReauthorized" }

// Entity returns "paymentauthorization".
func (PaymentAuthorized) Entity() string { return entity }

// Entity returns "paymentauthorization".
func (PaymentCaptured) Entity() string { return entity }

// Entity returns "paymentauthorization".
func (PaymentReleased) Entity() string { return entity }

// Entity returns "paymentauthorization".
func (PaymentAuthorizationExpired) Entity() string { return entity }

// Entity returns "paymentauthorization".
func (PaymentDeclined) Entity() string { return entity }

// Entity returns "paymentauthorization".
func (PaymentReauthorized) Entity() string { return entity }

// SubjectID returns the authorizationId.
func (e PaymentAuthorized) SubjectID() string { return string(e.AuthorizationID) }

// SubjectID returns the authorizationId.
func (e PaymentCaptured) SubjectID() string { return string(e.AuthorizationID) }

// SubjectID returns the authorizationId.
func (e PaymentReleased) SubjectID() string { return string(e.AuthorizationID) }

// SubjectID returns the authorizationId.
func (e PaymentAuthorizationExpired) SubjectID() string { return string(e.AuthorizationID) }

// SubjectID returns the authorizationId.
func (e PaymentDeclined) SubjectID() string { return string(e.AuthorizationID) }

// SubjectID returns the authorizationId.
func (e PaymentReauthorized) SubjectID() string { return string(e.AuthorizationID) }

func (p *PaymentAuthorization) header(now time.Time) shared.Header {
	return shared.Header{Version: p.version, At: shared.UTC(now)}
}

func (p *PaymentAuthorization) authorized(now time.Time) shared.Event {
	e := PaymentAuthorized{
		Header: p.header(now), AuthorizationID: p.id, CheckoutSessionID: p.sessionID,
		Currency: p.currency, TotalMinor: p.total, ExpiresAt: p.expiresAt,
	}
	for _, a := range p.allocations {
		e.Allocations = append(e.Allocations, AllocationAmount{GroupNo: a.GroupNo, AmountMinor: a.AmountMinor})
	}
	return e
}

func (p *PaymentAuthorization) declined(reason string, now time.Time) shared.Event {
	return PaymentDeclined{Header: p.header(now), AuthorizationID: p.id, CheckoutSessionID: p.sessionID, Reason: reason}
}
