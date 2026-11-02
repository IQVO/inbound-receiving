package http

import (
	"time"

	"github.com/claudioed/inbound-receiving/internal/application/usecases"
	"github.com/claudioed/inbound-receiving/internal/domain/appointment"
	"github.com/claudioed/inbound-receiving/internal/domain/asn"
)

// Response bodies (camelCase; events are snake_case).

type asnLineResponse struct {
	LineNo      int    `json:"lineNo"`
	SKU         string `json:"sku"`
	ExpectedQty int64  `json:"expectedQty"`
}

type cartonContentResponse struct {
	LineNo    int    `json:"lineNo"`
	SKU       string `json:"sku"`
	GTIN      string `json:"gtin"`
	Qty       int64  `json:"qty"`
	Lot       string `json:"lot,omitempty"`
	Expiry    string `json:"expiry,omitempty"`
	Condition string `json:"condition,omitempty"`
}

type cartonDimsResponse struct {
	LengthMm int64 `json:"lengthMm"`
	WidthMm  int64 `json:"widthMm"`
	HeightMm int64 `json:"heightMm"`
}

// cartonResponse is an announced carton; Status is set by GET
// /asns/{number}/cartons only (expected, accepted or missing).
type cartonResponse struct {
	SSCC         string                  `json:"sscc"`
	ParentSSCC   string                  `json:"parentSscc,omitempty"`
	Kind         string                  `json:"kind"`
	Contents     []cartonContentResponse `json:"contents"`
	GrossWeightG int64                   `json:"grossWeightG,omitempty"`
	Dims         *cartonDimsResponse     `json:"dims,omitempty"`
	Status       string                  `json:"status,omitempty"`
}

type asnResponse struct {
	AsnNumber       string            `json:"asnNumber"`
	SupplierRef     string            `json:"supplierRef"`
	OwnerRef        string            `json:"ownerRef,omitempty"`
	ExpectedArrival *time.Time        `json:"expectedArrival,omitempty"`
	State           string            `json:"state"`
	Lines           []asnLineResponse `json:"lines"`
	Cartons         []cartonResponse  `json:"cartons,omitempty"`
	Version         int64             `json:"version"`
}

type asnCartonsResponse struct {
	AsnNumber string           `json:"asnNumber"`
	OwnerRef  string           `json:"ownerRef,omitempty"`
	Cartons   []cartonResponse `json:"cartons"`
}

func toCarton(c asn.Carton) cartonResponse {
	out := cartonResponse{
		SSCC: c.SSCC().String(), ParentSSCC: c.ParentSSCC().String(), Kind: string(c.Kind()),
		Contents: make([]cartonContentResponse, 0, len(c.Contents())), GrossWeightG: c.GrossWeightG(),
	}
	for _, x := range c.Contents() {
		out.Contents = append(out.Contents, cartonContentResponse{
			LineNo: x.LineNo(), SKU: string(x.SKU()), GTIN: x.GTIN().String(), Qty: x.Qty(), Lot: x.Lot(), Expiry: x.Expiry(),
		})
	}
	if d, ok := c.Dims(); ok {
		out.Dims = &cartonDimsResponse{LengthMm: d.LengthMm(), WidthMm: d.WidthMm(), HeightMm: d.HeightMm()}
	}
	return out
}

func toAsnCartons(v usecases.AsnCartons) asnCartonsResponse {
	out := asnCartonsResponse{AsnNumber: string(v.Asn.Number()), OwnerRef: string(v.Asn.OwnerRef()), Cartons: make([]cartonResponse, 0, len(v.Items))}
	for _, it := range v.Items {
		c := toCarton(it.Carton)
		c.Status = it.Status
		out.Cartons = append(out.Cartons, c)
	}
	return out
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
	out := asnResponse{
		AsnNumber: string(a.Number()), SupplierRef: a.SupplierRef(), OwnerRef: string(a.OwnerRef()),
		State: string(a.State()), Lines: lines, Version: a.Version(),
	}
	for _, c := range a.Cartons() {
		out.Cartons = append(out.Cartons, toCarton(c))
	}
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
