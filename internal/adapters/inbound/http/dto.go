package http

import (
	"time"

	"github.com/claudioed/inbound-receiving/internal/application/usecases"
	"github.com/claudioed/inbound-receiving/internal/domain/appointment"
	"github.com/claudioed/inbound-receiving/internal/domain/asn"
	"github.com/claudioed/inbound-receiving/internal/domain/receipt"
)

// Request bodies. Numeric fields are plain ints: a missing field becomes 0,
// which the domain rejects with the matching slug (invalid-line-no,
// invalid-quantity, ...).

type asnLineRequest struct {
	LineNo      int    `json:"lineNo"`
	SKU         string `json:"sku"`
	ExpectedQty int64  `json:"expectedQty"`
}

type registerAsnRequest struct {
	AsnNumber       string           `json:"asnNumber"`
	SupplierRef     string           `json:"supplierRef"`
	ExpectedArrival *time.Time       `json:"expectedArrival"`
	Lines           []asnLineRequest `json:"lines"`
}

type reasonRequest struct {
	Reason string `json:"reason"`
}

type bookAppointmentRequest struct {
	DoorCode    string    `json:"doorCode"`
	Carrier     string    `json:"carrier"`
	WindowStart time.Time `json:"windowStart"`
	WindowEnd   time.Time `json:"windowEnd"`
	AsnNumbers  []string  `json:"asnNumbers"`
}

type openReceiptRequest struct {
	AsnNumber     string `json:"asnNumber"`
	AppointmentID string `json:"appointmentId"`
}

type receiveLineRequest struct {
	LineNo    int    `json:"lineNo"`
	Quantity  int64  `json:"quantity"`
	Condition string `json:"condition"`
}

// Response bodies (camelCase; events are snake_case).

type asnLineResponse struct {
	LineNo      int    `json:"lineNo"`
	SKU         string `json:"sku"`
	ExpectedQty int64  `json:"expectedQty"`
}

type asnResponse struct {
	AsnNumber       string            `json:"asnNumber"`
	SupplierRef     string            `json:"supplierRef"`
	ExpectedArrival *time.Time        `json:"expectedArrival,omitempty"`
	State           string            `json:"state"`
	Lines           []asnLineResponse `json:"lines"`
	Version         int64             `json:"version"`
}

type asnPageResponse struct {
	Items      []asnResponse `json:"items"`
	NextCursor string        `json:"nextCursor,omitempty"`
}

func toAsn(a *asn.Asn) asnResponse {
	lines := make([]asnLineResponse, 0, len(a.Lines()))
	for _, l := range a.Lines() {
		lines = append(lines, asnLineResponse{LineNo: l.LineNo(), SKU: string(l.SKU()), ExpectedQty: l.ExpectedQty()})
	}
	out := asnResponse{AsnNumber: string(a.Number()), SupplierRef: a.SupplierRef(), State: string(a.State()), Lines: lines, Version: a.Version()}
	if t := a.ExpectedArrival(); !t.IsZero() {
		out.ExpectedArrival = &t
	}
	return out
}

type appointmentResponse struct {
	AppointmentID string    `json:"appointmentId"`
	DoorCode      string    `json:"doorCode"`
	Carrier       string    `json:"carrier"`
	WindowStart   time.Time `json:"windowStart"`
	WindowEnd     time.Time `json:"windowEnd"`
	AsnNumbers    []string  `json:"asnNumbers"`
	State         string    `json:"state"`
	Version       int64     `json:"version"`
}

type appointmentPageResponse struct {
	Items      []appointmentResponse `json:"items"`
	NextCursor string                `json:"nextCursor,omitempty"`
}

func toAppointment(d *appointment.DockAppointment) appointmentResponse {
	numbers := make([]string, 0, len(d.AsnNumbers()))
	for _, n := range d.AsnNumbers() {
		numbers = append(numbers, string(n))
	}
	return appointmentResponse{
		AppointmentID: string(d.ID()), DoorCode: string(d.DoorCode()), Carrier: d.Carrier(),
		WindowStart: d.Window().Start(), WindowEnd: d.Window().End(), AsnNumbers: numbers,
		State: string(d.State()), Version: d.Version(),
	}
}

type receiptLineResponse struct {
	LineNo          int    `json:"lineNo"`
	SKU             string `json:"sku"`
	ExpectedQty     int64  `json:"expectedQty"`
	ReceivedGood    int64  `json:"receivedGood"`
	ReceivedDamaged int64  `json:"receivedDamaged"`
}

type discrepancyResponse struct {
	LineNo      int    `json:"lineNo"`
	SKU         string `json:"sku"`
	Kind        string `json:"kind"`
	ExpectedQty int64  `json:"expectedQty"`
	ReceivedQty int64  `json:"receivedQty"`
	DamagedQty  int64  `json:"damagedQty"`
}

type receiptResponse struct {
	ReceiptID     string                `json:"receiptId"`
	AsnNumber     string                `json:"asnNumber"`
	AppointmentID string                `json:"appointmentId,omitempty"`
	DoorCode      string                `json:"doorCode,omitempty"`
	State         string                `json:"state"`
	Lines         []receiptLineResponse `json:"lines"`
	OpenedAt      time.Time             `json:"openedAt"`
	ClosedAt      *time.Time            `json:"closedAt,omitempty"`
	Discrepancies []discrepancyResponse `json:"discrepancies"`
	Version       int64                 `json:"version"`
}

type receiptPageResponse struct {
	Items      []receiptResponse `json:"items"`
	NextCursor string            `json:"nextCursor,omitempty"`
}

func toReceipt(r *receipt.Receipt) receiptResponse {
	lines := make([]receiptLineResponse, 0, len(r.Lines()))
	for _, l := range r.Lines() {
		lines = append(lines, receiptLineResponse{
			LineNo: l.LineNo(), SKU: string(l.SKU()), ExpectedQty: l.ExpectedQty(),
			ReceivedGood: l.ReceivedGood(), ReceivedDamaged: l.ReceivedDamaged(),
		})
	}
	ds := r.Discrepancies()
	discrepancies := make([]discrepancyResponse, 0, len(ds))
	for _, d := range ds {
		discrepancies = append(discrepancies, discrepancyResponse{
			LineNo: d.LineNo, SKU: string(d.SKU), Kind: string(d.Kind),
			ExpectedQty: d.ExpectedQty, ReceivedQty: d.ReceivedQty, DamagedQty: d.DamagedQty,
		})
	}
	out := receiptResponse{
		ReceiptID: string(r.ID()), AsnNumber: string(r.AsnNumber()), AppointmentID: string(r.AppointmentID()),
		DoorCode: string(r.DoorCode()), State: string(r.State()), Lines: lines, OpenedAt: r.OpenedAt(),
		Discrepancies: discrepancies, Version: r.Version(),
	}
	if t := r.ClosedAt(); !t.IsZero() {
		out.ClosedAt = &t
	}
	return out
}

type dockResponse struct {
	DoorCode string `json:"doorCode"`
	DockFlow string `json:"dockFlow"`
}

type dockListResponse struct {
	Mode  string         `json:"mode"`
	Items []dockResponse `json:"items"`
}

func toDockList(l usecases.DockList) dockListResponse {
	items := make([]dockResponse, 0, len(l.Items))
	for _, d := range l.Items {
		items = append(items, dockResponse{DoorCode: string(d.Code), DockFlow: string(d.Flow)})
	}
	return dockListResponse{Mode: string(l.Mode), Items: items}
}
