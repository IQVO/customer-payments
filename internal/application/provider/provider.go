// Package provider is the vocabulary of the payment-provider port: the request
// and answer data, and the two failure kinds. ErrUnavailable marks a provider
// that could not be reached or timed out (problem type
// payment-provider-unavailable, 503); the call is safe to repeat because the
// provider keys are deterministic. ErrDeclined marks a definitive refusal.
package provider

import (
	"errors"
	"time"
)

var (
	// ErrUnavailable: the provider could not be reached or timed out.
	ErrUnavailable = errors.New("payment provider unavailable")
	// ErrDeclined: the provider refused definitively.
	ErrDeclined = errors.New("payment provider declined")
)

// AuthorizeRequest is the input of PaymentProvider.Authorize.
type AuthorizeRequest struct {
	Key                string
	PaymentMethodToken string
	Currency           string
	AmountMinor        int64
	// Reference is the checkout session id, for the provider's own records.
	Reference string
}

// Hold is an approved authorization at the provider.
type Hold struct {
	ID        string
	ExpiresAt time.Time
}

// DeclinedError is the refusal of the payment provider with its code, for
// example CARD_DECLINED. errors.Is(err, ErrDeclined) holds.
type DeclinedError struct{ Code string }

// Error returns the provider code.
func (e *DeclinedError) Error() string { return "payment provider declined: " + e.Code }

// Is makes a DeclinedError match ErrDeclined.
func (e *DeclinedError) Is(target error) bool { return target == ErrDeclined }

// Declined builds the error for a provider refusal.
func Declined(code string) error { return &DeclinedError{Code: code} }

// Code returns the provider code of a refusal, or "" when err is none.
func Code(err error) string {
	var d *DeclinedError
	if errors.As(err, &d) {
		return d.Code
	}
	if errors.Is(err, ErrDeclined) {
		return "DECLINED"
	}
	return ""
}
