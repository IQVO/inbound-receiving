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

type cartonContentRequest struct {
	LineNo int    `json:"lineNo"`
	SKU    string `json:"sku"`
	GTIN   string `json:"gtin"`
	Qty    int64  `json:"qty"`
	Lot    string `json:"lot"`
	Expiry string `json:"expiry"`
}

type cartonDimsRequest struct {
	LengthMm int64 `json:"lengthMm"`
	WidthMm  int64 `json:"widthMm"`
	HeightMm int64 `json:"heightMm"`
}

type cartonRequest struct {
	SSCC         string                 `json:"sscc"`
	ParentSSCC   string                 `json:"parentSscc"`
	Kind         string                 `json:"kind"`
	Contents     []cartonContentRequest `json:"contents"`
	GrossWeightG int64                  `json:"grossWeightG"`
	Dims         *cartonDimsRequest     `json:"dims"`
}

type registerAsnRequest struct {
	AsnNumber       string           `json:"asnNumber"`
	SupplierRef     string           `json:"supplierRef"`
	OwnerRef        string           `json:"ownerRef"`
	ExpectedArrival *time.Time       `json:"expectedArrival"`
	Lines           []asnLineRequest `json:"lines"`
	Cartons         []cartonRequest  `json:"cartons"`
}

func (r registerAsnRequest) cartonInputs() []asn.CartonInput {
	var out []asn.CartonInput
	for _, c := range r.Cartons {
		in := asn.CartonInput{SSCC: c.SSCC, ParentSSCC: c.ParentSSCC, Kind: c.Kind, GrossWeightG: c.GrossWeightG}
		for _, x := range c.Contents {
			in.Contents = append(in.Contents, asn.ContentInput{LineNo: x.LineNo, SKU: x.SKU, GTIN: x.GTIN, Qty: x.Qty, Lot: x.Lot, Expiry: x.Expiry})
		}
		if c.Dims != nil {
			in.Dims = &asn.DimsInput{LengthMm: c.Dims.LengthMm, WidthMm: c.Dims.WidthMm, HeightMm: c.Dims.HeightMm}
		}
		out = append(out, in)
	}
	return out
}

// receiveScanRequest is POST /receipts/{id}/scans: EITHER scan (the raw string
// a scanner delivered) OR kind and id. kind is the kind the screen expects; with
// a raw scan the optional expectedKind plays that part.
type receiveScanRequest struct {
	Scan            string                  `json:"scan"`
	Kind            string                  `json:"kind"`
	ID              string                  `json:"id"`
	Qty             int64                   `json:"qty"`
	ExpectedKind    string                  `json:"expectedKind"`
	Condition       string                  `json:"condition"`
	Lot             string                  `json:"lot"`
	Expiry          string                  `json:"expiry"`
	CountedContents []countedContentRequest `json:"countedContents"`
}

type countedContentRequest struct {
	SKU string `json:"sku"`
	Qty int64  `json:"qty"`
}

func (r receiveScanRequest) counted() []usecases.CountedContent {
	var out []usecases.CountedContent
	for _, c := range r.CountedContents {
		out = append(out, usecases.CountedContent{SKU: c.SKU, Qty: c.Qty})
	}
	return out
}

// cartonLabelResponse is the data of a license plate label.
type cartonLabelResponse struct {
	Lpn           string    `json:"lpn"`
	Symbology     string    `json:"symbology"`
	HumanReadable string    `json:"humanReadable"`
	SKU           string    `json:"sku,omitempty"`
	Units         int64     `json:"units"`
	ReceivedAt    time.Time `json:"receivedAt"`
}

func toCartonLabel(l usecases.CartonLabel) cartonLabelResponse {
	return cartonLabelResponse{Lpn: l.LPN, Symbology: l.Symbology, HumanReadable: l.HumanReadable, SKU: l.SKU, Units: l.Units, ReceivedAt: l.ReceivedAt}
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
	// CartonDiscrepancies are the carton findings raised on the receipt, in
	// raise order (ADR 0015); always present, [] when none.
	CartonDiscrepancies []receiptCartonDiscrepancyResponse `json:"cartonDiscrepancies"`
	Version             int64                              `json:"version"`
}

// receiptCartonDiscrepancyResponse is a carton discrepancy as the receipt
// keeps it: the scan answer's finding plus when it was raised.
type receiptCartonDiscrepancyResponse struct {
	Kind     string    `json:"kind"`
	SSCC     string    `json:"sscc,omitempty"`
	Expected string    `json:"expected,omitempty"`
	Actual   string    `json:"actual,omitempty"`
	At       time.Time `json:"at"`
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
	findings := r.CartonDiscrepancies()
	cartonDiscrepancies := make([]receiptCartonDiscrepancyResponse, 0, len(findings))
	for _, f := range findings {
		cartonDiscrepancies = append(cartonDiscrepancies, receiptCartonDiscrepancyResponse{
			Kind: string(f.Kind), SSCC: f.SSCC.String(), Expected: f.Expected, Actual: f.Actual, At: f.At,
		})
	}
	out := receiptResponse{
		ReceiptID: string(r.ID()), AsnNumber: string(r.AsnNumber()), AppointmentID: string(r.AppointmentID()),
		DoorCode: string(r.DoorCode()), State: string(r.State()), Lines: lines, OpenedAt: r.OpenedAt(),
		Discrepancies: discrepancies, CartonDiscrepancies: cartonDiscrepancies, Version: r.Version(),
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

// receiptCartonResponse is a carton of the receipt's ledger: its Lpn is the
// SSCC, or an internal `LPN-<ulid>` for a case received by case GTIN.
type receiptCartonResponse struct {
	Lpn        string                  `json:"lpn"`
	SSCC       string                  `json:"sscc,omitempty"`
	ParentSSCC string                  `json:"parentSscc,omitempty"`
	Kind       string                  `json:"kind"`
	Status     string                  `json:"status"`
	Sealed     bool                    `json:"sealed"`
	Contents   []cartonContentResponse `json:"contents"`
	ReceivedAt *time.Time              `json:"receivedAt,omitempty"`
}

type cartonDiscrepancyResponse struct {
	Kind     string `json:"kind"`
	SSCC     string `json:"sscc,omitempty"`
	Expected string `json:"expected,omitempty"`
	Actual   string `json:"actual,omitempty"`
}

// scanResponse answers POST /receipts/{id}/scans: the updated line totals, the
// cartons the scan accepted (or, for a duplicate, the one already received)
// and the discrepancies it raised.
type scanResponse struct {
	Kind          string                      `json:"kind"`
	Accepted      bool                        `json:"accepted"`
	Lines         []receiptLineResponse       `json:"lines"`
	Cartons       []receiptCartonResponse     `json:"cartons"`
	Discrepancies []cartonDiscrepancyResponse `json:"discrepancies"`
	Version       int64                       `json:"version"`
}

func toScan(o usecases.ScanOutcome) scanResponse {
	lines := toReceipt(o.Receipt).Lines
	out := scanResponse{
		Kind: string(o.Kind), Accepted: o.Result.Accepted, Lines: lines, Version: o.Receipt.Version(),
		Cartons: make([]receiptCartonResponse, 0, len(o.Result.Cartons)), Discrepancies: make([]cartonDiscrepancyResponse, 0, len(o.Result.Discrepancies)),
	}
	for _, c := range o.Result.Cartons {
		rc := receiptCartonResponse{
			Lpn: c.LPN(), SSCC: c.SSCC().String(), ParentSSCC: c.ParentSSCC().String(), Kind: string(c.Kind()),
			Status: string(c.Status()), Sealed: c.Sealed(), Contents: make([]cartonContentResponse, 0, len(c.Contents())),
		}
		for _, x := range c.Contents() {
			rc.Contents = append(rc.Contents, cartonContentResponse{
				LineNo: x.LineNo, SKU: string(x.SKU), GTIN: x.GTIN.String(), Qty: x.Qty, Lot: x.Lot, Expiry: x.Expiry, Condition: string(x.Condition),
			})
		}
		if t := c.ReceivedAt(); !t.IsZero() {
			rc.ReceivedAt = &t
		}
		out.Cartons = append(out.Cartons, rc)
	}
	for _, d := range o.Result.Discrepancies {
		out.Discrepancies = append(out.Discrepancies, cartonDiscrepancyResponse{Kind: string(d.Kind), SSCC: d.SSCC.String(), Expected: d.Expected, Actual: d.Actual})
	}
	return out
}
