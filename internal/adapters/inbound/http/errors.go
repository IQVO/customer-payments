package http

import (
	"errors"
	"net/http"

	"github.com/IQVO/customer-payments/internal/adapters/inbound/http/page"
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

// errRefundsDisabled marks POST /refunds while the ops kill switch
// REFUNDS_ENABLED=false is set: the route answers 503 refunds-disabled
// instead of creating a manual refund (portfolio rec 2026-11-02).
var errRefundsDisabled = errors.New("manual refunds are disabled by configuration")

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
	idempotencyKeyInvalid  = problem{http.StatusBadRequest, "idempotency-key-invalid", "Idempotency-Key header is invalid"}
	idempotencyKeyReused   = problem{http.StatusConflict, "idempotency-key-reused", "Idempotency-Key was already used with a different request"}
)

func unprocessable(slug, title string) problem {
	return problem{http.StatusUnprocessableEntity, slug, title}
}

func conflict(slug, title string) problem {
	return problem{http.StatusConflict, slug, title}
}

var notFound = problem{http.StatusNotFound, "not-found", "Not found"}

// replayClass is what the Idempotency-Key middleware does with the answer of a
// problem type (ADR 0008): FINAL and OUTCOME answers are stored and replayed, a
// RETRYABLE one is not stored and its key is released. A 2xx is stored and a 5xx
// never is, whatever the class. The empty class is "unclassified".
type replayClass string

const (
	// replayFinal: the answer is a function of the request (or of state that
	// only ever moves one way) alone; stored and replayed.
	replayFinal replayClass = "FINAL"
	// replayOutcome: the command committed state and then refused; the writes
	// commit with the stored answer. No problem type of this service is one.
	replayOutcome replayClass = "OUTCOME"
	// replayRetryable: nothing committed and the answer depends on state that
	// can change; not stored, the key is released, a retry executes again.
	replayRetryable replayClass = "RETRYABLE"
)

// stored reports whether an error answer of this class is remembered under its key.
func (c replayClass) stored() bool { return c == replayFinal || c == replayOutcome }

// catalogueEntry is one typed error, its problem and the replay class of that problem.
type catalogueEntry struct {
	err    error
	p      problem
	replay replayClass
}

// problemCatalogue maps every typed error to its problem and its replay class,
// in match order. The slugs are the ones of apis/openapi.yaml and
// .claude/rules/rest-api.md. ADR 0008 holds the reason of every class and a test
// keeps its table equal to this one; a new entry without a class fails
// TestEveryProblemTypeDeclaresAReplayClass. When unsure the class is RETRYABLE: a
// refusal that rolled everything back costs one more execution, a wrongly stored
// one pins a stale answer for 24 hours.
var problemCatalogue = []catalogueEntry{
	// The body is not valid JSON for the operation: a function of the request (decodeJSON, handlers.go).
	{errMalformedRequest, problem{http.StatusBadRequest, "malformed-request", "Malformed request"}, replayFinal},
	// A 5xx is never stored; the ops switch can be turned back on.
	{errRefundsDisabled, problem{http.StatusServiceUnavailable, "refunds-disabled", "Manual refunds are disabled by configuration"}, replayRetryable},
	// GET only (a list parameter); classified for completeness.
	{page.ErrInvalidLimit, problem{http.StatusBadRequest, "invalid-limit", "limit must be an integer between 1 and 500"}, replayFinal},
	// GET only.
	{page.ErrInvalidCursor, problem{http.StatusBadRequest, "invalid-cursor", "cursor is not a valid page token"}, replayFinal},
	// GET only.
	{errUnknownQueryParameter, problem{http.StatusBadRequest, "unknown-query-parameter", "The collection does not declare this query parameter"}, replayFinal},
	// Shared slug: an authorization or a refund of the path (ids this service mints, never deleted) but also the order of POST /refunds, bound to its
	// authorization by an event later (refund.go 114), and an empty or overlong path id (handlers.go 21): mixed, so RETRYABLE.
	{ports.ErrNotFound, notFound, replayRetryable},
	// A 5xx is never stored; the provider comes back.
	{provider.ErrUnavailable, problem{http.StatusServiceUnavailable, "payment-provider-unavailable", "The payment provider is unavailable; retry with the same Idempotency-Key"}, replayRetryable},
	// Only the void answers it as a 422 (authorization.go 131: a declined authorize is a stored DECLINED record answered 201, a declined refund a FAILED
	// refund answered 2xx). Nothing committed, and the answer also depends on the authorization state that check() reads (128): not a function of the request.
	{provider.ErrDeclined, unprocessable("payment-declined", "The payment provider declined the operation"), replayRetryable},
	// No code raises it (the repositories answer a second live authorization of a session as concurrent-update); were it raised, the retry finds the
	// live authorization and answers 200 (authorization.go 62-66).
	{paymentauthorization.ErrDuplicateAuthorization, conflict("duplicate-authorization", "The checkout session already has an authorization"), replayRetryable},
	// No keyed route captures (captures come from Kafka facts); unsure, so RETRYABLE.
	{paymentauthorization.ErrOverCapture, conflict("over-capture", "A capture never exceeds its allocation"), replayRetryable},
	// No keyed route captures; once the hold lapsed (or is EXPIRED) it stays lapsed: time and the terminal state only move forward (commands.go 60).
	{paymentauthorization.ErrCaptureAfterExpiry, conflict("capture-after-expiry", "No capture after the authorization expired"), replayFinal},
	// No keyed route binds (BindOrder is a Kafka fact); the binding is write-once (commands.go 31-40).
	{paymentauthorization.ErrOrderAlreadyBound, conflict("order-already-bound", "The authorization is bound to another order"), replayFinal},
	// POST /refunds: judged against the captured totals, which events raise, and the refunds that fail and stop counting (refund.go 21-41).
	{refund.ErrOverRefund, conflict("over-refund", "Refunds never exceed the captured total"), replayRetryable},
	// The optimistic-lock guard (or a unique index of a second live authorization / bound order) after the use case's own commandAttempts.
	{ports.ErrConcurrentUpdate, conflict("concurrent-update", "The resource was modified by another request; retry"), replayRetryable},
	// A void of an authorization that is not AUTHORIZED, a retry of a refund that is not FAILED: states that events and the policies move.
	{shared.ErrIllegalTransition, conflict("illegal-transition", "The resource does not allow this change in its current state"), replayRetryable},
	// The allocations of the request against its total (create.go 55 and 61): the request alone.
	{paymentauthorization.ErrAllocationMismatch, unprocessable("allocation-mismatch", "Allocations sum to the total and name distinct groups"), replayFinal},
	// No keyed route names a group (captures and releases are Kafka facts); unsure, so RETRYABLE.
	{paymentauthorization.ErrUnknownGroup, unprocessable("unknown-group", "The authorization has no allocation for this group"), replayRetryable},
	// A negative total or allocation of the request (money.go 17-19, create.go 35 and 51): the request alone.
	{shared.ErrNegativeAmount, unprocessable("negative-amount", "An amount is never negative"), replayFinal},
	// Mixed: fields of the request, but also what the provider answered (create.go 87: a hold without a future expiry) - one class per slug, so RETRYABLE.
	{shared.ErrValidation, unprocessable("validation-failed", "Validation failed"), replayRetryable},
}

// entryFor returns the first catalogue entry err matches; an unmatched error is
// the internal-error problem with no class.
func entryFor(err error) catalogueEntry {
	for _, entry := range problemCatalogue {
		if errors.Is(err, entry.err) {
			return entry
		}
	}
	return catalogueEntry{p: internalProblem}
}
