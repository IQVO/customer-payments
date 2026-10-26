package http

import (
	"errors"
	"net/http"

	"github.com/IQVO/customer-payments/internal/application/ports"
	"github.com/IQVO/customer-payments/internal/application/provider"
	"github.com/IQVO/customer-payments/internal/domain/paymentauthorization"
	"github.com/IQVO/customer-payments/internal/domain/refund"
	"github.com/IQVO/customer-payments/internal/domain/shared"
)

// problemBase is the namespace of this service's RFC 9457 `type` URIs.
const problemBase = "/problems/"

// errMalformedRequest marks a body or parameter the adapter itself rejects
// (bad JSON, unknown field, wrong type).
var errMalformedRequest = errors.New("request is not valid for this operation")

// problem is the fixed (status, slug, title) of one error category; the detail
// comes from the error at write time.
type problem struct {
	status int
	slug   string
	title  string
}

var internalProblem = problem{http.StatusInternalServerError, "internal-error", "Internal server error"}

// Problems the adapter writes itself, before any use case runs.
var (
	idempotencyKeyRequired = problem{http.StatusBadRequest, "idempotency-key-required", "Idempotency-Key header is required"}
	idempotencyKeyReused   = problem{http.StatusUnprocessableEntity, "idempotency-key-reused", "Idempotency-Key was already used with a different request"}
)

func unprocessable(slug, title string) problem {
	return problem{http.StatusUnprocessableEntity, slug, title}
}

func conflict(slug, title string) problem {
	return problem{http.StatusConflict, slug, title}
}

var notFound = problem{http.StatusNotFound, "not-found", "Not found"}

// problemCatalogue maps every typed error to its problem, in match order. The
// slugs are the ones of apis/openapi.yaml and .claude/rules/rest-api.md.
var problemCatalogue = []struct {
	err error
	p   problem
}{
	{errMalformedRequest, problem{http.StatusBadRequest, "malformed-request", "Malformed request"}},
	{ports.ErrNotFound, notFound},
	{provider.ErrUnavailable, problem{http.StatusServiceUnavailable, "payment-provider-unavailable", "The payment provider is unavailable; retry with the same Idempotency-Key"}},
	{provider.ErrDeclined, unprocessable("payment-declined", "The payment provider declined the operation")},
	{paymentauthorization.ErrDuplicateAuthorization, conflict("duplicate-authorization", "The checkout session already has an authorization")},
	{paymentauthorization.ErrOverCapture, conflict("over-capture", "A capture never exceeds its allocation")},
	{paymentauthorization.ErrCaptureAfterExpiry, conflict("capture-after-expiry", "No capture after the authorization expired")},
	{paymentauthorization.ErrOrderAlreadyBound, conflict("order-already-bound", "The authorization is bound to another order")},
	{refund.ErrOverRefund, conflict("over-refund", "Refunds never exceed the captured total")},
	{ports.ErrConcurrentUpdate, conflict("concurrent-update", "The resource was modified by another request; retry")},
	{shared.ErrIllegalTransition, conflict("illegal-transition", "The resource does not allow this change in its current state")},
	{paymentauthorization.ErrAllocationMismatch, unprocessable("allocation-mismatch", "Allocations sum to the total and name distinct groups")},
	{paymentauthorization.ErrUnknownGroup, unprocessable("unknown-group", "The authorization has no allocation for this group")},
	{shared.ErrNegativeAmount, unprocessable("negative-amount", "An amount is never negative")},
	{shared.ErrValidation, unprocessable("validation-failed", "Validation failed")},
}

// problemFor returns the problem of the first catalogue entry err matches, or
// the internal-error problem.
func problemFor(err error) problem {
	for _, entry := range problemCatalogue {
		if errors.Is(err, entry.err) {
			return entry.p
		}
	}
	return internalProblem
}
