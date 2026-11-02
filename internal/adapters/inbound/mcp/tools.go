package mcp

import (
	"context"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/claudioed/inbound-receiving/internal/application/usecases"
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
	// The tool returns every door: follow the use case's pages to the end.
	out := dockListView{Items: []dockView{}}
	cursor := ""
	for {
		list, err := d.ListDocks.Handle(ctx, usecases.ListDocksQuery{Limit: usecases.MaxListLimit, Cursor: cursor})
		if err != nil {
			return dockListView{}, mapError(err)
		}
		out.Mode = string(list.Mode)
		for _, door := range list.Items {
			out.Items = append(out.Items, dockView{DoorCode: string(door.Code), DockFlow: string(door.Flow)})
		}
		if list.NextCursor == "" {
			return out, nil
		}
		cursor = list.NextCursor
	}
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
