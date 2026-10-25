package kafka

import (
	"time"
)

// Topic of this service: every event goes to TopicEvents (the analytics-only
// consumers of H9 read it too).
const TopicEvents = "retailnetwork.customer-payments.events"

// schemaVersion is the dataschema version of every payload below.
const schemaVersion = 1

// Wire payloads (CloudEvents `data`, camelCase JSON) pinned by
// apis/asyncapi.yaml. Money is flat `...Minor` integers, arrays are always
// present and optional fields are omitted when absent, never null.

type allocationData struct {
	GroupNo     int   `json:"groupNo"`
	AmountMinor int64 `json:"amountMinor"`
}

type authorizedData struct {
	AuthorizationID   string           `json:"authorizationId"`
	CheckoutSessionID string           `json:"checkoutSessionId"`
	Currency          string           `json:"currency"`
	TotalMinor        int64            `json:"totalMinor"`
	Allocations       []allocationData `json:"allocations"`
	ExpiresAt         time.Time        `json:"expiresAt"`
}

type capturedData struct {
	AuthorizationID string `json:"authorizationId"`
	OrderID         string `json:"orderId,omitempty"`
	GroupNo         int    `json:"groupNo"`
	AmountMinor     int64  `json:"amountMinor"`
}

type releasedData struct {
	AuthorizationID string `json:"authorizationId"`
	OrderID         string `json:"orderId,omitempty"`
	GroupNo         int    `json:"groupNo"`
	AmountMinor     int64  `json:"amountMinor"`
	Reason          string `json:"reason"`
}

type expiredData struct {
	AuthorizationID string    `json:"authorizationId"`
	OrderID         string    `json:"orderId,omitempty"`
	ExpiredAt       time.Time `json:"expiredAt"`
}

type declinedData struct {
	AuthorizationID   string    `json:"authorizationId"`
	CheckoutSessionID string    `json:"checkoutSessionId"`
	Reason            string    `json:"reason"`
	At                time.Time `json:"at"`
}

type reauthorizedData struct {
	AuthorizationID         string    `json:"authorizationId"`
	OrderID                 string    `json:"orderId,omitempty"`
	ExpiresAt               time.Time `json:"expiresAt"`
	ProviderAuthorizationID string    `json:"providerAuthorizationId"`
}

type refundIssuedData struct {
	RefundID        string `json:"refundId"`
	OrderID         string `json:"orderId"`
	AuthorizationID string `json:"authorizationId"`
	GroupNo         int    `json:"groupNo,omitempty"`
	AmountMinor     int64  `json:"amountMinor"`
	Reason          string `json:"reason"`
}

type refundFailedData struct {
	RefundID        string `json:"refundId"`
	OrderID         string `json:"orderId"`
	AuthorizationID string `json:"authorizationId"`
	GroupNo         int    `json:"groupNo,omitempty"`
	Code            string `json:"code"`
	Attempts        int    `json:"attempts"`
	Final           bool   `json:"final"`
}
