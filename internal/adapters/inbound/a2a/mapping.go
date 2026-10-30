package a2a

import (
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strings"

	"github.com/IQVO/customer-payments/internal/application/ports"
	"github.com/IQVO/customer-payments/internal/application/views"
	"github.com/IQVO/customer-payments/internal/domain/shared"
)

// problemTypeBase is the namespace of the RFC 9457 `type` URIs. It is the SAME
// namespace as the REST adapter's ("/problems/<slug>"), and the slugs not-found,
// validation-failed, malformed-request and internal-error are the REST ones, so
// a delegating agent sees the identifiers a REST caller would for the outcomes
// both surfaces share. unknown-skill is the one slug only A2A has.
const problemTypeBase = "/problems/"

// problem is the RFC 7807-shaped rejection an agent client receives instead of
// a result: domain rejections and request-shape failures are business
// answers, not transport errors, so they travel in the message envelope.
type problem struct {
	Type   string `json:"type"`
	Title  string `json:"title"`
	Detail string `json:"detail"`
	Status int    `json:"status"`
}

// summary renders the one-line form used for the reply's text part.
func (p *problem) summary() string {
	return fmt.Sprintf("%s (%d): %s", p.Title, p.Status, p.Detail)
}

// The statuses and titles below are the REST adapter's problem catalogue
// (internal/adapters/inbound/http/errors.go) for what a read can meet:
// malformed-request 400, not-found 404, validation-failed 422 and
// internal-error 500. The 409 and 503 classes belong to writes and to the
// payment provider, neither of which this read-only agent reaches.
func problemMalformedRequest(detail string) *problem {
	return &problem{Type: problemTypeBase + "malformed-request", Title: "Malformed request", Detail: detail, Status: http.StatusBadRequest}
}

func problemUnknownSkill(skill string) *problem {
	return &problem{Type: problemTypeBase + "unknown-skill", Title: "Unknown skill", Detail: fmt.Sprintf("skill %q is not offered by this agent; the Agent Card lists the supported skills", skill), Status: http.StatusBadRequest}
}

func problemValidation(detail string) *problem {
	return &problem{Type: problemTypeBase + "validation-failed", Title: "Validation failed", Detail: detail, Status: http.StatusUnprocessableEntity}
}

func problemNotFound(detail string) *problem {
	return &problem{Type: problemTypeBase + "not-found", Title: "Not found", Detail: detail, Status: http.StatusNotFound}
}

func problemInternal() *problem {
	return &problem{Type: problemTypeBase + "internal-error", Title: "Internal server error", Detail: "an unexpected error occurred; see server logs", Status: http.StatusInternalServerError}
}

// problemFromUseCase maps a use case error to a problem answer, using the SAME
// classification as the REST adapter's problemFor for the errors a read can
// raise: ports.ErrNotFound is 404, shared.ErrValidation is 422 and anything
// unrecognised is 500. notFoundDetail is the adapter-owned text of the 404, so
// the shared error's own text never reaches an agent client; a 500 is logged
// and answered generically, so infrastructure detail (a DSN, SQL, a host)
// never does either.
func problemFromUseCase(err error, notFoundDetail string) *problem {
	switch {
	case errors.Is(err, ports.ErrNotFound):
		return problemNotFound(notFoundDetail)
	case errors.Is(err, shared.ErrValidation):
		return problemValidation("the skill arguments failed this context's validation rules")
	default:
		slog.Error("a2a skill failed", "error", err)
		return problemInternal()
	}
}

// envelope is the structured payload of every skill answer: exactly one of
// Data (a skill DTO) or Problem (why there is no result), plus the skill id
// the answer belongs to.
type envelope struct {
	Skill   string   `json:"skill"`
	Data    skillDTO `json:"data,omitempty"`
	Problem *problem `json:"problem,omitempty"`
}

// skillDTO is the union of the skill result DTOs. A2A parts are
// machine-readable JSON; each skill fills its own field, keeping the envelope
// flat and versioned as one contract.
type skillDTO struct {
	Authorizations *authorizationListDTO `json:"authorizations,omitempty"`
	Refunds        *refundListDTO        `json:"refunds,omitempty"`
	summary        string
}

// Summary is the human-readable one-liner for the reply's text part.
func (s skillDTO) Summary() string { return s.summary }

// The DTOs are the message-boundary representation of the read views: the
// SAME fields as the MCP tools and the REST bodies (camelCase), and none more.
// In particular there is no provider authorization id, no PSP token and no
// payment method: the views carry the first and the aggregates the others, and
// none of them may cross this boundary (TestAnswersNeverCarryProviderOrCardData).
type allocationDTO struct {
	GroupNo         int      `json:"groupNo"`
	AmountMinor     int64    `json:"amountMinor"`
	State           string   `json:"state"`
	TrackingNumbers []string `json:"trackingNumbers"`
}

type authorizationDTO struct {
	AuthorizationID   string          `json:"authorizationId"`
	CheckoutSessionID string          `json:"checkoutSessionId"`
	OrderID           string          `json:"orderId,omitempty"`
	State             string          `json:"state"`
	Currency          string          `json:"currency"`
	TotalMinor        int64           `json:"totalMinor"`
	Allocations       []allocationDTO `json:"allocations"`
	ExpiresAt         string          `json:"expiresAt,omitempty"`
	DeclineReason     string          `json:"declineReason,omitempty"`
}

// authorizationListDTO is the get_payment_status result: always a list, one
// element for an authorizationId lookup.
type authorizationListDTO struct {
	Authorizations []authorizationDTO `json:"authorizations"`
}

type refundDTO struct {
	RefundID        string `json:"refundId"`
	AuthorizationID string `json:"authorizationId"`
	OrderID         string `json:"orderId"`
	GroupNo         int    `json:"groupNo,omitempty"`
	AmountMinor     int64  `json:"amountMinor"`
	Reason          string `json:"reason"`
	State           string `json:"state"`
	Attempts        int    `json:"attempts"`
	FailureCode     string `json:"failureCode,omitempty"`
}

// refundListDTO is the get_refund_status result: always a list, one element
// for a refundId lookup.
type refundListDTO struct {
	Refunds []refundDTO `json:"refunds"`
}

// timeLayout is the UTC second-precision layout the REST and MCP bodies use.
const timeLayout = "2006-01-02T15:04:05Z"

func toAuthorizationDTO(v views.Authorization) authorizationDTO {
	out := authorizationDTO{
		AuthorizationID: v.AuthorizationID, CheckoutSessionID: v.CheckoutSessionID, OrderID: v.OrderID, State: v.State,
		Currency: v.Currency, TotalMinor: v.TotalMinor, DeclineReason: v.DeclineReason, Allocations: []allocationDTO{},
	}
	if !v.ExpiresAt.IsZero() {
		out.ExpiresAt = v.ExpiresAt.UTC().Format(timeLayout)
	}
	for _, a := range v.Allocations {
		tracking := a.TrackingNumbers
		if tracking == nil {
			tracking = []string{}
		}
		out.Allocations = append(out.Allocations, allocationDTO{GroupNo: a.GroupNo, AmountMinor: a.AmountMinor, State: a.State, TrackingNumbers: tracking})
	}
	return out
}

func toRefundDTO(v views.Refund) refundDTO {
	return refundDTO{
		RefundID: v.RefundID, AuthorizationID: v.AuthorizationID, OrderID: v.OrderID, GroupNo: v.GroupNo, AmountMinor: v.AmountMinor,
		Reason: v.Reason, State: v.State, Attempts: v.Attempts, FailureCode: v.FailureCode,
	}
}

// summarizeAuthorizations is the one-line text form of a payment status.
func summarizeAuthorizations(list authorizationListDTO) string {
	if len(list.Authorizations) == 0 {
		return "0 payment authorizations"
	}
	parts := make([]string, 0, len(list.Authorizations))
	for _, a := range list.Authorizations {
		parts = append(parts, fmt.Sprintf("%s %s %d %s", a.AuthorizationID, a.State, a.TotalMinor, a.Currency))
	}
	return fmt.Sprintf("%d payment authorizations: %s", len(list.Authorizations), strings.Join(parts, "; "))
}

// summarizeRefunds is the one-line text form of a refund status.
func summarizeRefunds(list refundListDTO) string {
	if len(list.Refunds) == 0 {
		return "0 refunds"
	}
	parts := make([]string, 0, len(list.Refunds))
	for _, r := range list.Refunds {
		parts = append(parts, fmt.Sprintf("%s %s %d (%s)", r.RefundID, r.State, r.AmountMinor, r.Reason))
	}
	return fmt.Sprintf("%d refunds: %s", len(list.Refunds), strings.Join(parts, "; "))
}
