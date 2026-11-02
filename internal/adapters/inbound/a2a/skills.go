package a2a

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/a2aproject/a2a-go/v2/a2a"
	"github.com/a2aproject/a2a-go/v2/a2asrv"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
)

// tracerName is the OTel instrumentation scope for A2A skill spans, mirroring
// the MCP adapter's mcp.tool spans.
const tracerName = "github.com/IQVO/customer-payments/internal/adapters/inbound/a2a"

// Skill ids advertised on the Agent Card (see Card). Each folds the get and the
// list MCP tools of one read into one goal-shaped question, so the four MCP
// tools become two skills.
const (
	skillGetPaymentStatus = "get_payment_status"
	skillGetRefundStatus  = "get_refund_status"
)

// maxIDLength mirrors the REST and MCP adapters' id guard.
const maxIDLength = 128

// skillRequest is the wire contract of every skill call: the first message
// part carries (as a Data part, or a Text part holding the same JSON) the
// skill id and its arguments. Strict on purpose: an agent client is
// machine-to-machine, so unknown fields are rejected, never ignored, and a
// misspelled name like "orderId_" must not silently turn the call into a
// different question. (Go's encoding/json matches field names
// case-insensitively, so "orderid" still reaches orderId; only genuinely
// unknown names are refused.)
type skillRequest struct {
	Skill     string          `json:"skill"`
	Arguments json.RawMessage `json:"arguments"`
}

// getPaymentStatusArgs and getRefundStatusArgs are the per-skill argument
// shapes; each skill takes exactly one of its selectors.
type getPaymentStatusArgs struct {
	AuthorizationID   string `json:"authorizationId,omitempty"`
	OrderID           string `json:"orderId,omitempty"`
	CheckoutSessionID string `json:"checkoutSessionId,omitempty"`
}

type getRefundStatusArgs struct {
	RefundID string `json:"refundId,omitempty"`
	OrderID  string `json:"orderId,omitempty"`
}

// answer maps one incoming message to a skill and returns the agent's reply
// Message. It is the whole executor body: this is a message-only agent, so a
// skill run is exactly one request mapped to one answer (ADR 0005).
func answer(ctx context.Context, deps Deps, ec *a2asrv.ExecutorContext) a2a.Event {
	req, problem := parseSkillRequest(ec)
	if problem != nil {
		return reply(ec, envelope{Skill: "", Problem: problem}, problem.summary())
	}

	ctx, span := otel.Tracer(tracerName).Start(ctx, "a2a.skill "+req.Skill)
	defer span.End()
	span.SetAttributes(attribute.String("a2a.skill", req.Skill))

	dto, problem := run(ctx, deps, req)
	if problem != nil {
		span.SetStatus(codes.Error, problem.Detail)
		return reply(ec, envelope{Skill: req.Skill, Problem: problem}, problem.summary())
	}
	return reply(ec, envelope{Skill: req.Skill, Data: dto}, dto.Summary())
}

// run dispatches one validated request to its skill.
func run(ctx context.Context, deps Deps, req skillRequest) (skillDTO, *problem) {
	switch req.Skill {
	case skillGetPaymentStatus:
		data, problem := deps.getPaymentStatus(ctx, req.Arguments)
		if problem != nil {
			return skillDTO{}, problem
		}
		return skillDTO{Authorizations: &data, summary: summarizeAuthorizations(data)}, nil
	case skillGetRefundStatus:
		data, problem := deps.getRefundStatus(ctx, req.Arguments)
		if problem != nil {
			return skillDTO{}, problem
		}
		return skillDTO{Refunds: &data, summary: summarizeRefunds(data)}, nil
	default:
		return skillDTO{}, problemUnknownSkill(req.Skill)
	}
}

// reply builds the agent Message for one skill run: a Data part carrying the
// structured envelope and a Text part carrying the one-line summary, bound to
// the caller's context (and task, when the client referenced one).
func reply(ec *a2asrv.ExecutorContext, env envelope, summary string) a2a.Event {
	return a2a.NewMessageForTask(a2a.MessageRoleAgent, ec, a2a.NewDataPart(env), a2a.NewTextPart(summary))
}

// parseSkillRequest extracts the skillRequest from the message's first part. A
// Data part (or a Text part holding the same JSON object) is accepted; anything
// else is a request-shape problem, answered, not raised, because a malformed
// delegation is a business answer for the calling agent, not a transport
// failure.
func parseSkillRequest(ec *a2asrv.ExecutorContext) (skillRequest, *problem) {
	msg := ec.Message
	if msg == nil || len(msg.Parts) == 0 {
		return skillRequest{}, problemMalformedRequest("the message carries no parts")
	}
	raw, err := partJSON(msg.Parts[0])
	if err != nil {
		return skillRequest{}, problemMalformedRequest(fmt.Sprintf("the first message part is not a skill request: %v", err))
	}
	var req skillRequest
	if err := decodeStrict(raw, &req); err != nil {
		return skillRequest{}, problemMalformedRequest(fmt.Sprintf("the skill request is not valid: %v", err))
	}
	if req.Skill == "" {
		return skillRequest{}, problemMalformedRequest(`the skill request carries no "skill" field`)
	}
	return req, nil
}

// partJSON renders one part's content as the JSON bytes of a skill request.
func partJSON(part *a2a.Part) ([]byte, error) {
	switch content := part.Content.(type) {
	case a2a.Data:
		return json.Marshal(content.Value)
	case a2a.Text:
		return []byte(string(content)), nil
	default:
		return nil, errors.New("expected a data part (or a text part holding the skill JSON)")
	}
}

// decodeStrict decodes one JSON document into dst, rejecting unknown fields
// and trailing data. Absent arguments decode as the empty object so each
// skill's own selector check produces the specific problem.
func decodeStrict(raw []byte, dst any) error {
	if len(bytes.TrimSpace(raw)) == 0 {
		raw = []byte("{}")
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(dst); err != nil {
		return err
	}
	if dec.More() {
		return errors.New("unexpected data after the JSON document")
	}
	return nil
}

// selector is one named selector argument and the value the caller gave.
type selector struct {
	name, value string
}

// chooseSelector requires exactly one non-blank selector and returns it. Zero
// or several is the validation-failed (422) answer the REST list operations
// give for the same mistake; the detail is adapter-owned so it names this
// surface's argument names.
func chooseSelector(selectors ...selector) (selector, *problem) {
	var given []selector
	names := make([]string, 0, len(selectors))
	for _, s := range selectors {
		names = append(names, s.name)
		if strings.TrimSpace(s.value) != "" {
			given = append(given, s)
		}
	}
	if len(given) != 1 {
		return selector{}, problemValidation("give exactly one of " + strings.Join(names, ", "))
	}
	return given[0], nil
}

// getPaymentStatus is the payment read skill: one authorization by id, or the
// authorizations of an order or a checkout session, through the same
// GetAuthorization and ListAuthorizations use cases the REST and MCP adapters
// use.
func (d Deps) getPaymentStatus(ctx context.Context, rawArgs json.RawMessage) (authorizationListDTO, *problem) {
	var args getPaymentStatusArgs
	if err := decodeStrict(rawArgs, &args); err != nil {
		return authorizationListDTO{}, problemMalformedRequest(fmt.Sprintf("arguments: %v", err))
	}
	sel, problem := chooseSelector(
		selector{"authorizationId", args.AuthorizationID},
		selector{"orderId", args.OrderID},
		selector{"checkoutSessionId", args.CheckoutSessionID},
	)
	if problem != nil {
		return authorizationListDTO{}, problem
	}
	if len(sel.value) > maxIDLength {
		return authorizationListDTO{}, tooLong(sel)
	}

	out := authorizationListDTO{Authorizations: []authorizationDTO{}}
	if sel.name == "authorizationId" {
		v, err := d.GetAuthorization.Execute(ctx, sel.value)
		if err != nil {
			return authorizationListDTO{}, problemFromUseCase(err, fmt.Sprintf("no payment authorization exists with authorizationId %q", sel.value))
		}
		out.Authorizations = append(out.Authorizations, toAuthorizationDTO(v))
		return out, nil
	}
	orderID, sessionID := sel.value, ""
	if sel.name == "checkoutSessionId" {
		orderID, sessionID = "", sel.value
	}
	list, err := d.ListAuthorizations.All(ctx, orderID, sessionID)
	if err != nil {
		return authorizationListDTO{}, problemFromUseCase(err, "")
	}
	for _, v := range list {
		out.Authorizations = append(out.Authorizations, toAuthorizationDTO(v))
	}
	return out, nil
}

// getRefundStatus is the refund read skill: one refund by id, or the refunds of
// an order, through the same GetRefund and ListRefunds use cases the REST and
// MCP adapters use.
func (d Deps) getRefundStatus(ctx context.Context, rawArgs json.RawMessage) (refundListDTO, *problem) {
	var args getRefundStatusArgs
	if err := decodeStrict(rawArgs, &args); err != nil {
		return refundListDTO{}, problemMalformedRequest(fmt.Sprintf("arguments: %v", err))
	}
	sel, problem := chooseSelector(selector{"refundId", args.RefundID}, selector{"orderId", args.OrderID})
	if problem != nil {
		return refundListDTO{}, problem
	}
	if len(sel.value) > maxIDLength {
		return refundListDTO{}, tooLong(sel)
	}

	out := refundListDTO{Refunds: []refundDTO{}}
	if sel.name == "refundId" {
		v, err := d.GetRefund.Execute(ctx, sel.value)
		if err != nil {
			return refundListDTO{}, problemFromUseCase(err, fmt.Sprintf("no refund exists with refundId %q", sel.value))
		}
		out.Refunds = append(out.Refunds, toRefundDTO(v))
		return out, nil
	}
	list, err := d.ListRefunds.All(ctx, sel.value)
	if err != nil {
		return refundListDTO{}, problemFromUseCase(err, "")
	}
	for _, v := range list {
		out.Refunds = append(out.Refunds, toRefundDTO(v))
	}
	return out, nil
}

// tooLong answers an id that cannot name a resource. Like the REST and MCP
// adapters, a resource id over the guard length is a miss (404), not a server
// error; a listing key over it is a malformed request.
func tooLong(sel selector) *problem {
	if sel.name == "authorizationId" || sel.name == "refundId" {
		return problemNotFound(fmt.Sprintf("an id is 1 to %d characters; this %s cannot name a resource", maxIDLength, sel.name))
	}
	return problemMalformedRequest(fmt.Sprintf("%s is at most %d characters", sel.name, maxIDLength))
}
