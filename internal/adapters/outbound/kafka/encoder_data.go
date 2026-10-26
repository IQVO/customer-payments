package kafka

import (
	"fmt"

	"github.com/IQVO/customer-payments/internal/domain/paymentauthorization"
	"github.com/IQVO/customer-payments/internal/domain/refund"
	"github.com/IQVO/customer-payments/internal/domain/shared"
)

// payloadFor maps a domain event to its wire payload.
func payloadFor(ev shared.Event) (any, error) {
	switch e := ev.(type) {
	case paymentauthorization.PaymentAuthorized:
		return authorizedPayload(e), nil
	case paymentauthorization.PaymentCaptured:
		return capturedData{AuthorizationID: string(e.AuthorizationID), OrderID: e.OrderID, GroupNo: e.GroupNo, AmountMinor: e.AmountMinor}, nil
	case paymentauthorization.PaymentReleased:
		return releasedData{
			AuthorizationID: string(e.AuthorizationID), OrderID: e.OrderID, GroupNo: e.GroupNo, AmountMinor: e.AmountMinor, Reason: e.Reason,
		}, nil
	case paymentauthorization.PaymentAuthorizationExpired:
		return expiredData{AuthorizationID: string(e.AuthorizationID), OrderID: e.OrderID, ExpiredAt: e.ExpiredAt.UTC()}, nil
	case paymentauthorization.PaymentDeclined:
		return declinedData{
			AuthorizationID: string(e.AuthorizationID), CheckoutSessionID: e.CheckoutSessionID, Reason: e.Reason, At: e.OccurredAt(),
		}, nil
	case paymentauthorization.PaymentReauthorized:
		return reauthorizedData{
			AuthorizationID: string(e.AuthorizationID), OrderID: e.OrderID, ExpiresAt: e.ExpiresAt.UTC(),
			ProviderAuthorizationID: e.ProviderAuthorizationID,
		}, nil
	case refund.RefundIssued:
		return refundIssuedData{
			RefundID: string(e.RefundID), OrderID: e.OrderID, AuthorizationID: e.AuthorizationID, GroupNo: e.GroupNo,
			AmountMinor: e.AmountMinor, Reason: string(e.Reason),
		}, nil
	case refund.RefundFailed:
		return refundFailedData{
			RefundID: string(e.RefundID), OrderID: e.OrderID, AuthorizationID: e.AuthorizationID, GroupNo: e.GroupNo,
			Code: e.Code, Attempts: e.Attempts, Final: e.Final,
		}, nil
	}
	return nil, fmt.Errorf("kafka encoder: %T is not a published event", ev)
}

func authorizedPayload(e paymentauthorization.PaymentAuthorized) authorizedData {
	d := authorizedData{
		AuthorizationID: string(e.AuthorizationID), CheckoutSessionID: e.CheckoutSessionID, Currency: e.Currency,
		TotalMinor: e.TotalMinor, ExpiresAt: e.ExpiresAt.UTC(), Allocations: make([]allocationData, len(e.Allocations)),
	}
	for i, a := range e.Allocations {
		d.Allocations[i] = allocationData(a)
	}
	return d
}
