package usecases

import (
	"context"
	"errors"

	"github.com/IQVO/customer-payments/internal/application/ports"
	"github.com/IQVO/customer-payments/internal/application/provider"
	"github.com/IQVO/customer-payments/internal/application/views"
	"github.com/IQVO/customer-payments/internal/domain/paymentauthorization"
	"github.com/IQVO/customer-payments/internal/domain/shared"
)

type pa = paymentauthorization.PaymentAuthorization

// check runs fn on a copy of p, so the domain's verdict on a command is known
// before the provider is called, and the real aggregate changes only after the
// provider answered.
func check(p *pa, fn func(*pa) error) error {
	c, err := paymentauthorization.Restore(p.Snapshot())
	if err != nil {
		return err
	}
	return fn(c)
}

// AuthorizePayment is the command AuthorizePayment (POST /payment-authorizations).
// One authorization per checkout session: a session that already holds a live
// (not DECLINED) authorization gets it back and the provider is not called.
type AuthorizePayment struct{ Deps }

// AuthorizePaymentInput is the body of POST /payment-authorizations.
type AuthorizePaymentInput struct {
	CheckoutSessionID  string
	PaymentMethodToken string
	Currency           string
	TotalMinor         int64
	Allocations        []paymentauthorization.AllocationInput
}

// AuthorizeResult is the authorization and whether this call created it.
type AuthorizeResult struct {
	Authorization views.Authorization
	Created       bool
}

// Execute authorizes the total with the provider and records the outcome:
// AUTHORIZED with PaymentAuthorized, or DECLINED with PaymentDeclined. A
// provider that cannot be reached is provider.ErrUnavailable and nothing
// is stored.
func (u AuthorizePayment) Execute(ctx context.Context, in AuthorizePaymentInput) (AuthorizeResult, error) {
	return transact(ctx, u.UoW, commandAttempts, func(ctx context.Context) (AuthorizeResult, error) {
		if shared.ValidID(in.CheckoutSessionID) {
			existing, err := u.Authorizations.FindLiveBySession(ctx, in.CheckoutSessionID)
			if err == nil {
				return AuthorizeResult{Authorization: views.AuthorizationFrom(existing)}, nil
			}
			if !errors.Is(err, ports.ErrNotFound) {
				return AuthorizeResult{}, err
			}
		}
		return u.create(ctx, in)
	})
}

func (u AuthorizePayment) create(ctx context.Context, in AuthorizePaymentInput) (AuthorizeResult, error) {
	draft := paymentauthorization.Draft{
		ID: u.IDs.NewAuthorizationID(), CheckoutSessionID: in.CheckoutSessionID, PaymentMethodToken: in.PaymentMethodToken,
		Currency: in.Currency, TotalMinor: in.TotalMinor, Allocations: in.Allocations,
	}
	if err := paymentauthorization.Check(draft); err != nil {
		return AuthorizeResult{}, err
	}
	earlier, err := u.Authorizations.ListBySession(ctx, in.CheckoutSessionID)
	if err != nil {
		return AuthorizeResult{}, err
	}
	hold, err := u.Provider.Authorize(ctx, provider.AuthorizeRequest{
		Key:                Key("authorize", in.CheckoutSessionID, 0, itoa(len(earlier)+1)),
		PaymentMethodToken: in.PaymentMethodToken, Currency: in.Currency, AmountMinor: in.TotalMinor, Reference: in.CheckoutSessionID,
	})
	outcome := paymentauthorization.Outcome{ProviderAuthorizationID: hold.ID, ExpiresAt: hold.ExpiresAt}
	switch {
	case errors.Is(err, provider.ErrDeclined):
		outcome = paymentauthorization.Outcome{DeclineReason: provider.Code(err)}
	case err != nil:
		return AuthorizeResult{}, err
	}
	p, events, err := paymentauthorization.New(draft, outcome, u.now())
	if err != nil {
		return AuthorizeResult{}, err
	}
	if err := u.Authorizations.Save(ctx, p); err != nil {
		return AuthorizeResult{}, err
	}
	if err := u.publish(ctx, events); err != nil {
		return AuthorizeResult{}, err
	}
	return AuthorizeResult{Authorization: views.AuthorizationFrom(p), Created: true}, nil
}

// VoidAuthorization is the command VoidAuthorization
// (POST /payment-authorizations/{authorizationId}:void).
type VoidAuthorization struct{ Deps }

// Execute voids an uncaptured authorization at the provider and releases its
// allocations (PaymentReleased, reason VOIDED).
func (u VoidAuthorization) Execute(ctx context.Context, id string) (views.Authorization, error) {
	return transact(ctx, u.UoW, commandAttempts, func(ctx context.Context) (views.Authorization, error) {
		p, err := u.Authorizations.Get(ctx, paymentauthorization.ID(id))
		if err != nil {
			return views.Authorization{}, err
		}
		return u.voidLoaded(ctx, p)
	})
}

func (u Deps) voidLoaded(ctx context.Context, p *pa) (views.Authorization, error) {
	if err := check(p, func(c *pa) error { _, err := c.Void(u.now()); return err }); err != nil {
		return views.Authorization{}, err
	}
	if err := u.Provider.Void(ctx, Key("void", string(p.ID()), 0, ""), p.ProviderAuthorizationID()); err != nil {
		return views.Authorization{}, err
	}
	events, err := p.Void(u.now())
	if err != nil {
		return views.Authorization{}, err
	}
	if err := u.Authorizations.Save(ctx, p); err != nil {
		return views.Authorization{}, err
	}
	if err := u.publish(ctx, events); err != nil {
		return views.Authorization{}, err
	}
	return views.AuthorizationFrom(p), nil
}

// GetAuthorization is the query GetAuthorization
// (GET /payment-authorizations/{authorizationId}).
type GetAuthorization struct{ Deps }

// Execute returns the authorization, or ports.ErrNotFound.
func (u GetAuthorization) Execute(ctx context.Context, id string) (views.Authorization, error) {
	p, err := u.Authorizations.Get(ctx, paymentauthorization.ID(id))
	if err != nil {
		return views.Authorization{}, err
	}
	return views.AuthorizationFrom(p), nil
}

// ListAuthorizations is the query ListAuthorizations
// (GET /payment-authorizations?orderId=|checkoutSessionId=).
type ListAuthorizations struct{ Deps }

// Execute lists by exactly one of orderId and checkoutSessionId; anything else
// is shared.ErrValidation.
func (u ListAuthorizations) Execute(ctx context.Context, orderID, sessionID string) ([]views.Authorization, error) {
	if (orderID == "") == (sessionID == "") {
		return nil, shared.Invalid("exactly one of orderId and checkoutSessionId is required")
	}
	var found []*pa
	if sessionID != "" {
		list, err := u.Authorizations.ListBySession(ctx, sessionID)
		if err != nil {
			return nil, err
		}
		found = list
	} else {
		p, err := u.Authorizations.FindByOrder(ctx, orderID)
		if err != nil && !errors.Is(err, ports.ErrNotFound) {
			return nil, err
		}
		if p != nil {
			found = []*pa{p}
		}
	}
	out := make([]views.Authorization, 0, len(found))
	for _, p := range found {
		out = append(out, views.AuthorizationFrom(p))
	}
	return out, nil
}
