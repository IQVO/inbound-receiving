package a2a

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/a2aproject/a2a-go/v2/a2a"
	"github.com/a2aproject/a2a-go/v2/a2asrv"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"

	"github.com/claudioed/inbound-receiving/internal/application/repository"
	"github.com/claudioed/inbound-receiving/internal/application/usecases"
	"github.com/claudioed/inbound-receiving/internal/domain/asn"
)

// tracerName is the OTel instrumentation scope for A2A skill spans, mirroring
// the MCP adapter's mcp.tool spans.
const tracerName = "github.com/claudioed/inbound-receiving/internal/adapters/inbound/a2a"

// Skill ids advertised on the Agent Card (see Card).
const (
	skillAsnReceivingStatus = "asn_receiving_status"
	skillDockSchedule       = "dock_schedule"
)

// Result bounds: an ASN carries a handful of receipts at most, and the
// schedule is a bounded view, never a feed (A2A delegates a goal, it does not
// sync data).
const (
	asnReceiptsLimit         = 100
	dockScheduleDefaultLimit = 50
	dockScheduleMaxLimit     = 100
)

// skillRequest is the wire contract of every skill call: the first meaningful
// message part carries (as a Data part, or a Text part holding the same JSON)
// the skill id and its arguments. Strict on purpose: an agent client is
// machine-to-machine, so unknown fields are rejected, never ignored, and a
// misspelled name like "doorr" must not silently widen the schedule to every
// door. (Go's encoding/json matches field names case-insensitively, so "DOOR"
// still reaches door; only genuinely unknown names are refused.)
type skillRequest struct {
	Skill     string          `json:"skill"`
	Arguments json.RawMessage `json:"arguments"`
}

// asnReceivingStatusArgs and dockScheduleArgs are the per-skill argument
// shapes. The filters are strings the use cases validate; Limit is a pointer
// so an explicit 0 is refused instead of read as "default".
type asnReceivingStatusArgs struct {
	AsnNumber string `json:"asnNumber"`
}

type dockScheduleArgs struct {
	Door  string `json:"door"`
	State string `json:"state"`
	From  string `json:"from"`
	To    string `json:"to"`
	Limit *int   `json:"limit"`
}

// answer maps one incoming message to a skill and returns the agent's reply
// Message. It is the whole executor body: this is a message-only agent, so a
// skill run is exactly one request mapped to one answer (docs/adr/0016).
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
	case skillAsnReceivingStatus:
		data, summary, problem := deps.asnReceivingStatus(ctx, req.Arguments)
		if problem != nil {
			return skillDTO{}, problem
		}
		return skillDTO{AsnReceivingStatus: &data, summary: summary}, nil
	case skillDockSchedule:
		data, summary, problem := deps.dockSchedule(ctx, req.Arguments)
		if problem != nil {
			return skillDTO{}, problem
		}
		return skillDTO{DockSchedule: &data, summary: summary}, nil
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

// parseSkillRequest extracts the skillRequest from the message's first
// meaningful part. A Data part (or a Text part holding the same JSON object)
// is accepted; anything else is a request-shape problem, answered, not
// raised, because a malformed delegation is a business answer for the calling
// agent, not a transport failure.
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
// skill's own required-field checks produce the specific problem.
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

// asnReceivingStatus is the composed read skill: one ASN through GetAsn and
// the receipts counted against it through ListReceipts, the same use cases
// the REST and MCP adapters use. The receipts are the first page of at most
// asnReceiptsLimit; MoreReceipts flags the rest.
func (d Deps) asnReceivingStatus(ctx context.Context, rawArgs json.RawMessage) (asnReceivingStatusDTO, string, *problem) {
	var args asnReceivingStatusArgs
	if err := decodeStrict(rawArgs, &args); err != nil {
		return asnReceivingStatusDTO{}, "", problemInvalidArgument(fmt.Sprintf("arguments: %v", err))
	}
	if strings.TrimSpace(args.AsnNumber) == "" {
		return asnReceivingStatusDTO{}, "", problemInvalidArgument("asnNumber is required")
	}
	a, err := d.GetAsn.Handle(ctx, args.AsnNumber)
	if err != nil {
		return asnReceivingStatusDTO{}, "", problemFromUseCase(err, args.AsnNumber)
	}
	page, err := d.ListReceipts.Handle(ctx, usecases.ListReceiptsQuery{AsnNumber: string(a.Number()), Limit: asnReceiptsLimit})
	if err != nil {
		return asnReceivingStatusDTO{}, "", problemFromUseCase(err, args.AsnNumber)
	}
	out := asnReceivingStatusDTO{Asn: toAsnDTO(a), Receipts: make([]receiptDTO, 0, len(page.Items)), MoreReceipts: page.NextCursor != ""}
	for _, r := range page.Items {
		out.Receipts = append(out.Receipts, toReceiptDTO(r))
	}
	return out, summarizeAsnStatus(out), nil
}

// dockSchedule is the schedule read skill, through the same ListAppointments
// use case the REST and MCP adapters use. It is one bounded page: when more
// match than limit, Truncated says so and the caller narrows the range.
func (d Deps) dockSchedule(ctx context.Context, rawArgs json.RawMessage) (dockScheduleDTO, string, *problem) {
	var args dockScheduleArgs
	if err := decodeStrict(rawArgs, &args); err != nil {
		return dockScheduleDTO{}, "", problemInvalidArgument(fmt.Sprintf("arguments: %v", err))
	}
	limit := dockScheduleDefaultLimit
	if args.Limit != nil {
		limit = *args.Limit
	}
	if limit < 1 || limit > dockScheduleMaxLimit {
		return dockScheduleDTO{}, "", problemInvalidArgument(fmt.Sprintf("limit must be 1..%d", dockScheduleMaxLimit))
	}
	from, err := optionalInstant("from", args.From)
	if err != nil {
		return dockScheduleDTO{}, "", problemFromUseCase(err, "")
	}
	to, err := optionalInstant("to", args.To)
	if err != nil {
		return dockScheduleDTO{}, "", problemFromUseCase(err, "")
	}
	page, err := d.ListAppointments.Handle(ctx, usecases.ListAppointmentsQuery{
		Limit: limit, Door: args.Door, State: args.State, From: from, To: to,
	})
	if err != nil {
		return dockScheduleDTO{}, "", problemFromUseCase(err, "")
	}
	out := dockScheduleDTO{Appointments: make([]appointmentDTO, 0, len(page.Items)), Truncated: page.NextCursor != ""}
	for _, a := range page.Items {
		out.Appointments = append(out.Appointments, toAppointmentDTO(a))
	}
	return out, summarizeDockSchedule(out, args), nil
}

// optionalInstant parses an optional RFC 3339 filter; a malformed one is an
// invalid-query rejection exactly as the REST adapter reports it.
func optionalInstant(name, raw string) (time.Time, error) {
	if raw == "" {
		return time.Time{}, nil
	}
	t, err := time.Parse(time.RFC3339Nano, raw)
	if err != nil {
		return time.Time{}, fmt.Errorf("%w: %s must be an RFC 3339 instant", usecases.ErrInvalidListQuery, name)
	}
	return t.UTC(), nil
}

// problemFromUseCase maps a use case error to a problem answer, using the
// SAME classification and slugs as the REST adapter (internal/adapters/
// inbound/http problemCatalogue) for the errors a read can raise: a malformed
// query or number is 400 (invalid-query, invalid-asn-number), an unknown ASN
// is 404 (asn-not-found) and anything unrecognised is 500 (internal-error).
// The REST adapter's 409, 412, 422 and 503 classes belong to writes and
// dependencies this read-only agent does not have. Typed 400 details come
// from the domain's own message; the not-found detail is adapter-owned so the
// repository's internal error text never reaches an agent client. Domain
// rejections are EXPECTED business outcomes and are answered as RFC 7807-shaped
// problems; anything unrecognised is still answered as a problem, so the
// calling agent always receives a structured, machine-readable result.
func problemFromUseCase(err error, asnNumber string) *problem {
	switch {
	case errors.Is(err, usecases.ErrInvalidListQuery):
		return &problem{Type: problemTypeBase + "/invalid-query", Title: "Invalid query parameter", Detail: err.Error(), Status: 400}
	case errors.Is(err, asn.ErrInvalidNumber):
		return &problem{Type: problemTypeBase + "/invalid-asn-number", Title: "ASN number is invalid", Detail: err.Error(), Status: 400}
	case errors.Is(err, repository.ErrAsnNotFound):
		return &problem{Type: problemTypeBase + "/asn-not-found", Title: "ASN not found", Detail: fmt.Sprintf("no ASN exists with asnNumber %q", asnNumber), Status: 404}
	default:
		slog.Error("a2a skill failed", "error", err)
		return &problem{Type: problemTypeBase + "/internal-error", Title: "Internal server error", Detail: "the skill failed; see server logs (trace id in the response metadata)", Status: 500}
	}
}
