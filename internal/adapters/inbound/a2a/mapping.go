package a2a

import (
	"fmt"
	"strings"
	"time"

	"github.com/claudioed/inbound-receiving/internal/domain/appointment"
	"github.com/claudioed/inbound-receiving/internal/domain/asn"
	"github.com/claudioed/inbound-receiving/internal/domain/receipt"
)

// problemTypeBase namespaces this context's problem types. It is the SAME
// namespace as the REST adapter's RFC 7807 types (internal/adapters/inbound/
// http problemBaseURI), so a delegating agent sees the identifiers a REST
// caller would for the domain outcomes both surfaces share.
const problemTypeBase = "https://errors.inbound-receiving.warehouse-systems.dev"

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

func problemMalformedRequest(detail string) *problem {
	return &problem{Type: problemTypeBase + "/malformed-skill-request", Title: "Malformed skill request", Detail: detail, Status: 400}
}

func problemUnknownSkill(skill string) *problem {
	return &problem{Type: problemTypeBase + "/unknown-skill", Title: "Unknown skill", Detail: fmt.Sprintf("skill %q is not offered by this agent; the Agent Card lists the supported skills", skill), Status: 400}
}

func problemInvalidArgument(detail string) *problem {
	return &problem{Type: problemTypeBase + "/invalid-argument", Title: "Invalid skill argument", Detail: detail, Status: 400}
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
	AsnReceivingStatus *asnReceivingStatusDTO `json:"asnReceivingStatus,omitempty"`
	DockSchedule       *dockScheduleDTO       `json:"dockSchedule,omitempty"`
	summary            string
}

// Summary is the human-readable one-liner for the reply's text part.
func (s skillDTO) Summary() string { return s.summary }

// asnReceivingStatusDTO is the asn_receiving_status result: the ASN and the
// receipts counted against it (the first page of them: an ASN has a handful at
// most, MoreReceipts flags the rest).
type asnReceivingStatusDTO struct {
	Asn          asnDTO       `json:"asn"`
	Receipts     []receiptDTO `json:"receipts"`
	MoreReceipts bool         `json:"moreReceipts"`
}

type asnDTO struct {
	AsnNumber       string       `json:"asnNumber"`
	SupplierRef     string       `json:"supplierRef"`
	ExpectedArrival string       `json:"expectedArrival,omitempty"`
	State           string       `json:"state"`
	Lines           []asnLineDTO `json:"lines"`
}

type asnLineDTO struct {
	LineNo      int    `json:"lineNo"`
	SKU         string `json:"sku"`
	ExpectedQty int64  `json:"expectedQty"`
}

type receiptDTO struct {
	ReceiptID     string           `json:"receiptId"`
	AppointmentID string           `json:"appointmentId,omitempty"`
	DoorCode      string           `json:"doorCode,omitempty"`
	State         string           `json:"state"`
	OpenedAt      string           `json:"openedAt"`
	ClosedAt      string           `json:"closedAt,omitempty"`
	Lines         []receiptLineDTO `json:"lines"`
	Discrepancies []discrepancyDTO `json:"discrepancies"`
}

type receiptLineDTO struct {
	LineNo          int    `json:"lineNo"`
	SKU             string `json:"sku"`
	ExpectedQty     int64  `json:"expectedQty"`
	ReceivedGood    int64  `json:"receivedGood"`
	ReceivedDamaged int64  `json:"receivedDamaged"`
}

type discrepancyDTO struct {
	LineNo      int    `json:"lineNo"`
	SKU         string `json:"sku"`
	Kind        string `json:"kind"`
	ExpectedQty int64  `json:"expectedQty"`
	ReceivedQty int64  `json:"receivedQty"`
	DamagedQty  int64  `json:"damagedQty"`
}

// dockScheduleDTO is the dock_schedule result: the appointments that matched
// and whether more exist beyond the requested limit.
type dockScheduleDTO struct {
	Appointments []appointmentDTO `json:"appointments"`
	Truncated    bool             `json:"truncated"`
}

type appointmentDTO struct {
	AppointmentID string   `json:"appointmentId"`
	DoorCode      string   `json:"doorCode"`
	Carrier       string   `json:"carrier"`
	WindowStart   string   `json:"windowStart"`
	WindowEnd     string   `json:"windowEnd"`
	AsnNumbers    []string `json:"asnNumbers"`
	State         string   `json:"state"`
}

// instant renders a time as the UTC RFC 3339 form every JSON boundary of this
// context uses.
func instant(t time.Time) string { return t.UTC().Format(time.RFC3339Nano) }

func toAsnDTO(a *asn.Asn) asnDTO {
	lines := make([]asnLineDTO, 0, len(a.Lines()))
	for _, l := range a.Lines() {
		lines = append(lines, asnLineDTO{LineNo: l.LineNo(), SKU: string(l.SKU()), ExpectedQty: l.ExpectedQty()})
	}
	out := asnDTO{AsnNumber: string(a.Number()), SupplierRef: a.SupplierRef(), State: string(a.State()), Lines: lines}
	if t := a.ExpectedArrival(); !t.IsZero() {
		out.ExpectedArrival = instant(t)
	}
	return out
}

func toReceiptDTO(r *receipt.Receipt) receiptDTO {
	lines := make([]receiptLineDTO, 0, len(r.Lines()))
	for _, l := range r.Lines() {
		lines = append(lines, receiptLineDTO{
			LineNo: l.LineNo(), SKU: string(l.SKU()), ExpectedQty: l.ExpectedQty(),
			ReceivedGood: l.ReceivedGood(), ReceivedDamaged: l.ReceivedDamaged(),
		})
	}
	found := r.Discrepancies()
	discrepancies := make([]discrepancyDTO, 0, len(found))
	for _, d := range found {
		discrepancies = append(discrepancies, discrepancyDTO{
			LineNo: d.LineNo, SKU: string(d.SKU), Kind: string(d.Kind),
			ExpectedQty: d.ExpectedQty, ReceivedQty: d.ReceivedQty, DamagedQty: d.DamagedQty,
		})
	}
	out := receiptDTO{
		ReceiptID: string(r.ID()), AppointmentID: string(r.AppointmentID()), DoorCode: string(r.DoorCode()),
		State: string(r.State()), OpenedAt: instant(r.OpenedAt()), Lines: lines, Discrepancies: discrepancies,
	}
	if t := r.ClosedAt(); !t.IsZero() {
		out.ClosedAt = instant(t)
	}
	return out
}

func toAppointmentDTO(d *appointment.DockAppointment) appointmentDTO {
	numbers := make([]string, 0, len(d.AsnNumbers()))
	for _, n := range d.AsnNumbers() {
		numbers = append(numbers, string(n))
	}
	return appointmentDTO{
		AppointmentID: string(d.ID()), DoorCode: string(d.DoorCode()), Carrier: d.Carrier(),
		WindowStart: instant(d.Window().Start()), WindowEnd: instant(d.Window().End()),
		AsnNumbers: numbers, State: string(d.State()),
	}
}

// summarizeAsnStatus is the one-line text form of the ASN status.
func summarizeAsnStatus(s asnReceivingStatusDTO) string {
	open, closed, discrepancies := 0, 0, 0
	for _, r := range s.Receipts {
		if r.State == string(receipt.StateOpen) {
			open++
		} else {
			closed++
		}
		discrepancies += len(r.Discrepancies)
	}
	more := ""
	if s.MoreReceipts {
		more = "+"
	}
	return fmt.Sprintf("%s (%s): %s, %d lines; %d%s receipts (%d open, %d closed); %d discrepancies",
		s.Asn.AsnNumber, s.Asn.SupplierRef, s.Asn.State, len(s.Asn.Lines), len(s.Receipts), more, open, closed, discrepancies)
}

// summarizeDockSchedule is the one-line text form of the schedule.
func summarizeDockSchedule(s dockScheduleDTO, args dockScheduleArgs) string {
	var scope []string
	if args.Door != "" {
		scope = append(scope, "door "+args.Door)
	}
	if args.State != "" {
		scope = append(scope, "state "+args.State)
	}
	if args.From != "" {
		scope = append(scope, "from "+args.From)
	}
	if args.To != "" {
		scope = append(scope, "to "+args.To)
	}
	filter := "all doors and states"
	if len(scope) > 0 {
		filter = strings.Join(scope, ", ")
	}
	out := fmt.Sprintf("%d dock appointments (%s)", len(s.Appointments), filter)
	if s.Truncated {
		out += "; more exist, narrow the range"
	}
	return out
}
