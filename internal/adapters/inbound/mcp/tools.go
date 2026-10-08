package mcp

import (
	"context"
	"fmt"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/claudioed/inbound-receiving/internal/application/usecases"
	"github.com/claudioed/inbound-receiving/internal/domain/appointment"
	"github.com/claudioed/inbound-receiving/internal/domain/asn"
	"github.com/claudioed/inbound-receiving/internal/domain/receipt"
)

// Deps is everything the MCP tools need, injected by the composition root.
// It carries the SAME read use cases the REST adapter uses (GET /asns,
// /asns/{asnNumber}, /appointments, /appointments/{appointmentId}, /receipts,
// /receipts/{receiptId}, /docks); the adapter never constructs an outbound
// adapter itself, and it holds no write use case at all (docs/adr/0005).
type Deps struct {
	GetAsn           *usecases.GetAsn
	ListAsns         *usecases.ListAsns
	GetAppointment   *usecases.GetAppointment
	ListAppointments *usecases.ListAppointments
	GetReceipt       *usecases.GetReceipt
	ListReceipts     *usecases.ListReceipts
	ListDocks        *usecases.ListDocks
}

// --- inputs -------------------------------------------------------------------

type asnNumberInput struct {
	AsnNumber string `json:"asn_number" jsonschema:"the ASN number as supplied by the supplier, e.g. ASN-1001"`
}

type appointmentIDInput struct {
	AppointmentID string `json:"appointment_id" jsonschema:"the appointment id returned when it was booked, e.g. appt-1b4e28ba-2fa1-4d3c-8f6e-0a1b2c3d4e5f"`
}

type receiptIDInput struct {
	ReceiptID string `json:"receipt_id" jsonschema:"the receipt id returned when it was opened, e.g. rcpt-1b4e28ba-2fa1-4d3c-8f6e-0a1b2c3d4e5f"`
}

type listAsnsInput struct {
	Limit  int    `json:"limit,omitempty" jsonschema:"page size, 1..500; omitted or 0 means 100"`
	Cursor string `json:"cursor,omitempty" jsonschema:"the opaque next_cursor returned by the previous page; omitted means the first page"`
	State  string `json:"state,omitempty" jsonschema:"keep only ASNs in this state: Registered, Receiving, Closed or Cancelled"`
}

type listAppointmentsInput struct {
	Limit  int    `json:"limit,omitempty" jsonschema:"page size, 1..500; omitted or 0 means 100"`
	Cursor string `json:"cursor,omitempty" jsonschema:"the opaque next_cursor returned by the previous page; omitted means the first page"`
	Door   string `json:"door,omitempty" jsonschema:"keep only appointments at this dock door code"`
	State  string `json:"state,omitempty" jsonschema:"keep only appointments in this state: Booked, CheckedIn, Completed or Cancelled"`
	From   string `json:"from,omitempty" jsonschema:"RFC 3339 instant; keep only appointments whose window ends after it"`
	To     string `json:"to,omitempty" jsonschema:"RFC 3339 instant; keep only appointments whose window starts before it"`
}

type listReceiptsInput struct {
	Limit     int    `json:"limit,omitempty" jsonschema:"page size, 1..500; omitted or 0 means 100"`
	Cursor    string `json:"cursor,omitempty" jsonschema:"the opaque next_cursor returned by the previous page; omitted means the first page"`
	AsnNumber string `json:"asn_number,omitempty" jsonschema:"keep only receipts of this ASN number"`
	State     string `json:"state,omitempty" jsonschema:"keep only receipts in this state: Open or Closed"`
}

// noInput is the (empty) input of a tool without arguments.
type noInput struct{}

// --- outputs (the REST bodies with snake_case names) --------------------------

type asnLineView struct {
	LineNo      int    `json:"line_no"`
	SKU         string `json:"sku"`
	ExpectedQty int64  `json:"expected_qty"`
}

type asnView struct {
	AsnNumber       string        `json:"asn_number"`
	SupplierRef     string        `json:"supplier_ref"`
	ExpectedArrival string        `json:"expected_arrival,omitempty"`
	State           string        `json:"state"`
	Lines           []asnLineView `json:"lines"`
	Version         int64         `json:"version"`
}

type asnPageView struct {
	Items      []asnView `json:"items"`
	NextCursor string    `json:"next_cursor,omitempty"`
}

type appointmentView struct {
	AppointmentID string   `json:"appointment_id"`
	DoorCode      string   `json:"door_code"`
	Carrier       string   `json:"carrier"`
	WindowStart   string   `json:"window_start"`
	WindowEnd     string   `json:"window_end"`
	AsnNumbers    []string `json:"asn_numbers"`
	State         string   `json:"state"`
	Version       int64    `json:"version"`
}

type appointmentPageView struct {
	Items      []appointmentView `json:"items"`
	NextCursor string            `json:"next_cursor,omitempty"`
}

type receiptLineView struct {
	LineNo          int    `json:"line_no"`
	SKU             string `json:"sku"`
	ExpectedQty     int64  `json:"expected_qty"`
	ReceivedGood    int64  `json:"received_good"`
	ReceivedDamaged int64  `json:"received_damaged"`
}

type discrepancyView struct {
	LineNo      int    `json:"line_no"`
	SKU         string `json:"sku"`
	Kind        string `json:"kind"`
	ExpectedQty int64  `json:"expected_qty"`
	ReceivedQty int64  `json:"received_qty"`
	DamagedQty  int64  `json:"damaged_qty"`
}

type receiptView struct {
	ReceiptID     string            `json:"receipt_id"`
	AsnNumber     string            `json:"asn_number"`
	AppointmentID string            `json:"appointment_id,omitempty"`
	DoorCode      string            `json:"door_code,omitempty"`
	State         string            `json:"state"`
	Lines         []receiptLineView `json:"lines"`
	OpenedAt      string            `json:"opened_at"`
	ClosedAt      string            `json:"closed_at,omitempty"`
	Discrepancies []discrepancyView `json:"discrepancies"`
	Version       int64             `json:"version"`
}

type receiptPageView struct {
	Items      []receiptView `json:"items"`
	NextCursor string        `json:"next_cursor,omitempty"`
}

type dockView struct {
	DoorCode string `json:"door_code"`
	DockFlow string `json:"dock_flow"`
}

type dockListView struct {
	Mode  string     `json:"mode"`
	Items []dockView `json:"items"`
}

// --- mapping ------------------------------------------------------------------

func instant(t time.Time) string { return t.UTC().Format(time.RFC3339Nano) }

func toAsn(a *asn.Asn) asnView {
	lines := make([]asnLineView, 0, len(a.Lines()))
	for _, l := range a.Lines() {
		lines = append(lines, asnLineView{LineNo: l.LineNo(), SKU: string(l.SKU()), ExpectedQty: l.ExpectedQty()})
	}
	out := asnView{
		AsnNumber: string(a.Number()), SupplierRef: a.SupplierRef(), State: string(a.State()),
		Lines: lines, Version: a.Version(),
	}
	if t := a.ExpectedArrival(); !t.IsZero() {
		out.ExpectedArrival = instant(t)
	}
	return out
}

func toAppointment(d *appointment.DockAppointment) appointmentView {
	numbers := make([]string, 0, len(d.AsnNumbers()))
	for _, n := range d.AsnNumbers() {
		numbers = append(numbers, string(n))
	}
	return appointmentView{
		AppointmentID: string(d.ID()), DoorCode: string(d.DoorCode()), Carrier: d.Carrier(),
		WindowStart: instant(d.Window().Start()), WindowEnd: instant(d.Window().End()),
		AsnNumbers: numbers, State: string(d.State()), Version: d.Version(),
	}
}

func toReceipt(r *receipt.Receipt) receiptView {
	lines := make([]receiptLineView, 0, len(r.Lines()))
	for _, l := range r.Lines() {
		lines = append(lines, receiptLineView{
			LineNo: l.LineNo(), SKU: string(l.SKU()), ExpectedQty: l.ExpectedQty(),
			ReceivedGood: l.ReceivedGood(), ReceivedDamaged: l.ReceivedDamaged(),
		})
	}
	ds := r.Discrepancies()
	discrepancies := make([]discrepancyView, 0, len(ds))
	for _, d := range ds {
		discrepancies = append(discrepancies, discrepancyView{
			LineNo: d.LineNo, SKU: string(d.SKU), Kind: string(d.Kind),
			ExpectedQty: d.ExpectedQty, ReceivedQty: d.ReceivedQty, DamagedQty: d.DamagedQty,
		})
	}
	out := receiptView{
		ReceiptID: string(r.ID()), AsnNumber: string(r.AsnNumber()), AppointmentID: string(r.AppointmentID()),
		DoorCode: string(r.DoorCode()), State: string(r.State()), Lines: lines, OpenedAt: instant(r.OpenedAt()),
		Discrepancies: discrepancies, Version: r.Version(),
	}
	if t := r.ClosedAt(); !t.IsZero() {
		out.ClosedAt = instant(t)
	}
	return out
}

// optionalInstant parses an optional RFC 3339 filter; a malformed one is an
// invalid-query error exactly as the REST adapter reports it.
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

// --- handlers -----------------------------------------------------------------

func (d Deps) getAsn(ctx context.Context, in asnNumberInput) (asnView, error) {
	a, err := d.GetAsn.Handle(ctx, in.AsnNumber)
	if err != nil {
		return asnView{}, mapError(err)
	}
	return toAsn(a), nil
}

func (d Deps) listAsns(ctx context.Context, in listAsnsInput) (asnPageView, error) {
	page, err := d.ListAsns.Handle(ctx, usecases.ListAsnsQuery{Limit: in.Limit, Cursor: in.Cursor, State: in.State})
	if err != nil {
		return asnPageView{}, mapError(err)
	}
	out := asnPageView{Items: make([]asnView, 0, len(page.Items)), NextCursor: page.NextCursor}
	for _, a := range page.Items {
		out.Items = append(out.Items, toAsn(a))
	}
	return out, nil
}

func (d Deps) getAppointment(ctx context.Context, in appointmentIDInput) (appointmentView, error) {
	a, err := d.GetAppointment.Handle(ctx, in.AppointmentID)
	if err != nil {
		return appointmentView{}, mapError(err)
	}
	return toAppointment(a), nil
}

func (d Deps) listAppointments(ctx context.Context, in listAppointmentsInput) (appointmentPageView, error) {
	from, err := optionalInstant("from", in.From)
	if err != nil {
		return appointmentPageView{}, mapError(err)
	}
	to, err := optionalInstant("to", in.To)
	if err != nil {
		return appointmentPageView{}, mapError(err)
	}
	page, err := d.ListAppointments.Handle(ctx, usecases.ListAppointmentsQuery{
		Limit: in.Limit, Cursor: in.Cursor, Door: in.Door, State: in.State, From: from, To: to,
	})
	if err != nil {
		return appointmentPageView{}, mapError(err)
	}
	out := appointmentPageView{Items: make([]appointmentView, 0, len(page.Items)), NextCursor: page.NextCursor}
	for _, a := range page.Items {
		out.Items = append(out.Items, toAppointment(a))
	}
	return out, nil
}

func (d Deps) getReceipt(ctx context.Context, in receiptIDInput) (receiptView, error) {
	r, err := d.GetReceipt.Handle(ctx, in.ReceiptID)
	if err != nil {
		return receiptView{}, mapError(err)
	}
	return toReceipt(r), nil
}

func (d Deps) listReceipts(ctx context.Context, in listReceiptsInput) (receiptPageView, error) {
	page, err := d.ListReceipts.Handle(ctx, usecases.ListReceiptsQuery{
		Limit: in.Limit, Cursor: in.Cursor, AsnNumber: in.AsnNumber, State: in.State,
	})
	if err != nil {
		return receiptPageView{}, mapError(err)
	}
	out := receiptPageView{Items: make([]receiptView, 0, len(page.Items)), NextCursor: page.NextCursor}
	for _, r := range page.Items {
		out.Items = append(out.Items, toReceipt(r))
	}
	return out, nil
}

func (d Deps) listDocks(ctx context.Context, _ noInput) (dockListView, error) {
	list, err := d.ListDocks.Handle(ctx)
	if err != nil {
		return dockListView{}, mapError(err)
	}
	out := dockListView{Mode: string(list.Mode), Items: make([]dockView, 0, len(list.Items))}
	for _, door := range list.Items {
		out.Items = append(out.Items, dockView{DoorCode: string(door.Code), DockFlow: string(door.Flow)})
	}
	return out, nil
}

// --- registry -----------------------------------------------------------------

// registerTools adds the seven read-only tools. Every tool is annotated
// read-only, idempotent and closed-world; TestToolSurface pins the set and
// fails the build on any write-verb name (docs/adr/0005).
func (d Deps) registerTools(server *mcp.Server) {
	closedWorld := false
	readOnly := &mcp.ToolAnnotations{ReadOnlyHint: true, IdempotentHint: true, OpenWorldHint: &closedWorld}

	addTool(server, &mcp.Tool{
		Name: "get_asn",
		Description: "Read one advance ship notice (ASN) by number: supplier_ref, expected_arrival (omitted when none), state " +
			"(Registered, Receiving, Closed or Cancelled), its lines (line_no, sku, expected_qty) and version. Read-only; " +
			"fails with asn-not-found for an unknown number and invalid-asn-number for a malformed one.",
		Annotations: readOnly,
	}, d.getAsn)

	addTool(server, &mcp.Tool{
		Name: "list_asns",
		Description: "List ASNs in ascending number order, a page at a time (default 100, max 500), optionally only those in " +
			"one state. Pass the returned next_cursor to get the next page; it is absent on the last page. Read-only; a bad " +
			"limit, cursor or state fails with invalid-query.",
		Annotations: readOnly,
	}, d.listAsns)

	addTool(server, &mcp.Tool{
		Name: "get_appointment",
		Description: "Read one carrier dock appointment by id: door_code, carrier, window_start and window_end (UTC), the " +
			"asn_numbers it covers, state (Booked, CheckedIn, Completed or Cancelled) and version. Read-only; fails with " +
			"appointment-not-found for an unknown id and invalid-appointment-id for a malformed one.",
		Annotations: readOnly,
	}, d.getAppointment)

	addTool(server, &mcp.Tool{
		Name: "list_appointments",
		Description: "List dock appointments in ascending window-start order, a page at a time (default 100, max 500), " +
			"optionally only those at one door, in one state, and/or overlapping a from/to time range (from: window ends " +
			"after it; to: window starts before it; both RFC 3339). Pass the returned next_cursor to get the next page; it " +
			"is absent on the last page. Read-only; a bad limit, cursor, state or instant fails with invalid-query and a " +
			"malformed door with invalid-door-code.",
		Annotations: readOnly,
	}, d.listAppointments)

	addTool(server, &mcp.Tool{
		Name: "get_receipt",
		Description: "Read one receipt by id: asn_number, appointment_id and door_code (omitted for a walk-in), state (Open or " +
			"Closed), per-line expected_qty, received_good and received_damaged, opened_at, closed_at (omitted while open), " +
			"the discrepancies as the receipt stands (kind Short, Over or Damaged; final once Closed, provisional while " +
			"Open) and version. Read-only; fails with " +
			"receipt-not-found for an unknown id and invalid-receipt-id for a malformed one.",
		Annotations: readOnly,
	}, d.getReceipt)

	addTool(server, &mcp.Tool{
		Name: "list_receipts",
		Description: "List receipts in ascending id order, a page at a time (default 100, max 500), optionally only those of " +
			"one ASN (asn_number) and/or in one state (Open or Closed). Pass the returned next_cursor to get the next page; " +
			"it is absent on the last page. Read-only; a bad limit, cursor or state fails with invalid-query and a malformed " +
			"asn_number with invalid-asn-number.",
		Annotations: readOnly,
	}, d.listReceipts)

	addTool(server, &mcp.Tool{
		Name: "list_docks",
		Description: "List the inbound dock doors this service knows (door_code and dock_flow Inbound or Both) in ascending " +
			"code order, with the mode the local copy runs in: permissive (any door code can be booked, the list may be " +
			"empty) or kafka (only the listed doors can be booked). Takes no arguments. Read-only.",
		Annotations: readOnly,
	}, d.listDocks)
}

// addTool registers one tool. A handler error is returned to the SDK as the
// handler's error, which the SDK turns into an isError tool result (never a
// transport/protocol failure), carrying the error text.
func addTool[In, Out any](
	server *mcp.Server,
	tool *mcp.Tool,
	handle func(context.Context, In) (Out, error),
) {
	mcp.AddTool(server, tool, func(ctx context.Context, _ *mcp.CallToolRequest, in In) (*mcp.CallToolResult, Out, error) {
		out, err := handle(ctx, in)
		if err != nil {
			var zero Out
			return nil, zero, err
		}
		return nil, out, nil
	})
}
