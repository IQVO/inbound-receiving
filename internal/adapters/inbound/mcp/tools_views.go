package mcp

import (
	"fmt"
	"time"

	"github.com/claudioed/inbound-receiving/internal/application/usecases"
	"github.com/claudioed/inbound-receiving/internal/domain/appointment"
	"github.com/claudioed/inbound-receiving/internal/domain/asn"
	"github.com/claudioed/inbound-receiving/internal/domain/receipt"
)

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
