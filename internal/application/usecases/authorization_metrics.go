package usecases

import (
	"context"
	"errors"

	"github.com/IQVO/customer-payments/internal/application/ports"
	"github.com/IQVO/customer-payments/internal/application/provider"
	"github.com/IQVO/customer-payments/internal/domain/paymentauthorization"
	"github.com/IQVO/customer-payments/internal/domain/shared"
)

// failureReasons maps an authorization failure to the problem slug the REST
// adapter answers with, in match order.
var failureReasons = []struct {
	err    error
	reason string
}{
	{provider.ErrUnavailable, "payment-provider-unavailable"},
	{paymentauthorization.ErrAllocationMismatch, "allocation-mismatch"},
	{ports.ErrConcurrentUpdate, "concurrent-update"},
	{shared.ErrValidation, "validation-failed"},
}

// internalErrorReason is the reason of every failure that is not a refusal the
// caller can act on (a database error, a bug).
const internalErrorReason = "internal-error"

// recordOutcome counts one authorization attempt that was not a replay: failed
// when err is set, declined when the stored authorization is DECLINED, else
// authorized.
func (u AuthorizePayment) recordOutcome(ctx context.Context, res AuthorizeResult, err error) {
	m := u.metrics()
	switch {
	case err != nil:
		m.AuthorizationFailed(ctx, failureReason(err))
	case res.Authorization.DeclineReason != "":
		m.AuthorizationDeclined(ctx, res.Authorization.DeclineReason)
	default:
		m.AuthorizationAuthorized(ctx)
	}
}

func failureReason(err error) string {
	for _, r := range failureReasons {
		if errors.Is(err, r.err) {
			return r.reason
		}
	}
	return internalErrorReason
}
