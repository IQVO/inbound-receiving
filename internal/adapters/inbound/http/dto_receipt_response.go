package http

import (
	"time"

	"github.com/claudioed/inbound-receiving/internal/application/usecases"
	"github.com/claudioed/inbound-receiving/internal/domain/receipt"
)

type receiptLineResponse struct {
	LineNo          int    `json:"lineNo"`
	SKU             string `json:"sku"`
	ExpectedQty     int64  `json:"expectedQty"`
	ReceivedGood    int64  `json:"receivedGood"`
	ReceivedDamaged int64  `json:"receivedDamaged"`
	// EarliestBestBefore and EarliestExpiry are derived from the accepted cartons
	// of the line (ADR 0024); absent when none carried a date.
	EarliestBestBefore string `json:"earliestBestBefore,omitempty"`
	EarliestExpiry     string `json:"earliestExpiry,omitempty"`
}

// palletResponse is the earliest dates of the accepted cartons of one pallet.
type palletResponse struct {
	ParentSSCC         string `json:"parentSscc"`
	CartonCount        int    `json:"cartonCount"`
	EarliestBestBefore string `json:"earliestBestBefore,omitempty"`
	EarliestExpiry     string `json:"earliestExpiry,omitempty"`
}

type discrepancyResponse struct {
	LineNo      int    `json:"lineNo"`
	SKU         string `json:"sku"`
	Kind        string `json:"kind"`
	ExpectedQty int64  `json:"expectedQty"`
	ReceivedQty int64  `json:"receivedQty"`
	DamagedQty  int64  `json:"damagedQty"`
	// W13Code and CBVDisposition are the standard coding of the kind (ADR 0023),
	// derived from it; W13Code is absent for a kind the standard has no code for.
	W13Code        string `json:"w13Code,omitempty"`
	CBVDisposition string `json:"cbvDisposition,omitempty"`
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
	// Pallets are the accepted cartons grouped by parent SSCC with their earliest
	// dates (ADR 0024); absent when no accepted carton has a pallet.
	Pallets []palletResponse `json:"pallets,omitempty"`
	Version int64            `json:"version"`
}

// receiptCartonDiscrepancyResponse is a carton discrepancy as the receipt
// keeps it: the scan answer's finding plus when it was raised.
type receiptCartonDiscrepancyResponse struct {
	Kind     string    `json:"kind"`
	SSCC     string    `json:"sscc,omitempty"`
	Expected string    `json:"expected,omitempty"`
	Actual   string    `json:"actual,omitempty"`
	At       time.Time `json:"at"`
	// W13Code and CBVDisposition: see discrepancyResponse (ADR 0023).
	W13Code        string `json:"w13Code,omitempty"`
	CBVDisposition string `json:"cbvDisposition,omitempty"`
	// SupplierRef is set on the label findings of ADR 0024.
	SupplierRef string `json:"supplierRef,omitempty"`
}

type receiptPageResponse struct {
	Items      []receiptResponse `json:"items"`
	NextCursor string            `json:"nextCursor,omitempty"`
}

func toReceipt(r *receipt.Receipt) receiptResponse {
	lines := make([]receiptLineResponse, 0, len(r.Lines()))
	for _, l := range r.Lines() {
		dates := r.LineDates(l.LineNo())
		lines = append(lines, receiptLineResponse{
			LineNo: l.LineNo(), SKU: string(l.SKU()), ExpectedQty: l.ExpectedQty(),
			ReceivedGood: l.ReceivedGood(), ReceivedDamaged: l.ReceivedDamaged(),
			EarliestBestBefore: dates.BestBefore, EarliestExpiry: dates.Expiry,
		})
	}
	ds := r.Discrepancies()
	discrepancies := make([]discrepancyResponse, 0, len(ds))
	for _, d := range ds {
		w13, cbv := coding(d.Kind)
		discrepancies = append(discrepancies, discrepancyResponse{
			LineNo: d.LineNo, SKU: string(d.SKU), Kind: string(d.Kind),
			ExpectedQty: d.ExpectedQty, ReceivedQty: d.ReceivedQty, DamagedQty: d.DamagedQty,
			W13Code: w13, CBVDisposition: cbv,
		})
	}
	findings := r.CartonDiscrepancies()
	cartonDiscrepancies := make([]receiptCartonDiscrepancyResponse, 0, len(findings))
	for _, f := range findings {
		w13, cbv := coding(f.Kind)
		cartonDiscrepancies = append(cartonDiscrepancies, receiptCartonDiscrepancyResponse{
			Kind: string(f.Kind), SSCC: f.SSCC.String(), Expected: f.Expected, Actual: f.Actual, At: f.At,
			W13Code: w13, CBVDisposition: cbv, SupplierRef: f.SupplierRef,
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
	for _, p := range r.Pallets() {
		out.Pallets = append(out.Pallets, palletResponse{
			ParentSSCC: p.ParentSSCC.String(), CartonCount: p.CartonCount,
			EarliestBestBefore: p.Earliest.BestBefore, EarliestExpiry: p.Earliest.Expiry,
		})
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
	// NextCursor is omitted on the last page.
	NextCursor string `json:"nextCursor,omitempty"`
}

func toDockList(l usecases.DockList) dockListResponse {
	items := make([]dockResponse, 0, len(l.Items))
	for _, d := range l.Items {
		items = append(items, dockResponse{DoorCode: string(d.Code), DockFlow: string(d.Flow)})
	}
	return dockListResponse{Mode: string(l.Mode), Items: items, NextCursor: l.NextCursor}
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
	// SSCCSource says who gave the carton its SSCC: supplier or receiver (ADR 0024).
	SSCCSource string `json:"ssccSource,omitempty"`
	// What the scanned label said (ADR 0024); every field is optional.
	BestBefore     string `json:"bestBefore,omitempty"`
	Expiry         string `json:"expiry,omitempty"`
	ProductionDate string `json:"productionDate,omitempty"`
	PackagingDate  string `json:"packagingDate,omitempty"`
	Serial         string `json:"serial,omitempty"`
	PurchaseOrder  string `json:"purchaseOrder,omitempty"`
	ShipToGLN      string `json:"shipToGln,omitempty"`
	ShipForGLN     string `json:"shipForGln,omitempty"`
}

type cartonDiscrepancyResponse struct {
	Kind     string `json:"kind"`
	SSCC     string `json:"sscc,omitempty"`
	Expected string `json:"expected,omitempty"`
	Actual   string `json:"actual,omitempty"`
	// W13Code and CBVDisposition: see discrepancyResponse (ADR 0023).
	W13Code        string `json:"w13Code,omitempty"`
	CBVDisposition string `json:"cbvDisposition,omitempty"`
	// SupplierRef is set on the label findings of ADR 0024.
	SupplierRef string `json:"supplierRef,omitempty"`
}

// coding is the standard vocabulary of a kind (ADR 0023); empty for none.
func coding(k receipt.Kind) (w13, cbv string) {
	c, _ := receipt.CodingOf(k)
	return string(c.W13), string(c.CBV)
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
		out.Cartons = append(out.Cartons, toReceiptCarton(c))
	}
	out.Discrepancies = toCartonDiscrepancies(o.Result.Discrepancies)
	return out
}

func toReceiptCarton(c receipt.Carton) receiptCartonResponse {
	l := c.Label()
	rc := receiptCartonResponse{
		Lpn: c.LPN(), SSCC: c.SSCC().String(), ParentSSCC: c.ParentSSCC().String(), Kind: string(c.Kind()),
		Status: string(c.Status()), Sealed: c.Sealed(), Contents: make([]cartonContentResponse, 0, len(c.Contents())),
		SSCCSource: string(c.SSCCSource()), BestBefore: l.BestBefore, Expiry: l.Expiry, ProductionDate: l.ProductionDate,
		PackagingDate: l.PackagingDate, Serial: l.Serial, PurchaseOrder: l.PurchaseOrder, ShipToGLN: l.ShipToGLN, ShipForGLN: l.ShipForGLN,
	}
	for _, x := range c.Contents() {
		rc.Contents = append(rc.Contents, cartonContentResponse{
			LineNo: x.LineNo, SKU: string(x.SKU), GTIN: x.GTIN.String(), Qty: x.Qty, Lot: x.Lot, Expiry: x.Expiry, Condition: string(x.Condition),
		})
	}
	if t := c.ReceivedAt(); !t.IsZero() {
		rc.ReceivedAt = &t
	}
	return rc
}

func toCartonDiscrepancies(in []receipt.CartonDiscrepancy) []cartonDiscrepancyResponse {
	out := make([]cartonDiscrepancyResponse, 0, len(in))
	for _, d := range in {
		w13, cbv := coding(d.Kind)
		out = append(out, cartonDiscrepancyResponse{
			Kind: string(d.Kind), SSCC: d.SSCC.String(), Expected: d.Expected, Actual: d.Actual, W13Code: w13, CBVDisposition: cbv, SupplierRef: d.SupplierRef,
		})
	}
	return out
}

// assignmentResponse answers POST /receipts/{id}/sscc-assignments: the SSCC and
// what a printer needs to render it (ADR 0024). The service has no printer
// driver and no ZPL: the floor client prints.
type assignmentResponse struct {
	Lpn           string `json:"lpn"`
	SSCC          string `json:"sscc"`
	HumanReadable string `json:"humanReadable"`
	// ElementString is the data of the GS1-128 symbol: AI 00 and the 18 digits,
	// without parentheses.
	ElementString string                      `json:"elementString"`
	Symbology     string                      `json:"symbology"`
	SSCCSource    string                      `json:"ssccSource"`
	SKU           string                      `json:"sku,omitempty"`
	Units         int64                       `json:"units"`
	Discrepancies []cartonDiscrepancyResponse `json:"discrepancies"`
	Version       int64                       `json:"version"`
}

func toAssignment(o usecases.AssignOutcome) assignmentResponse {
	label := usecases.LabelOf(o.Carton)
	return assignmentResponse{
		Lpn: o.Carton.LPN(), SSCC: o.SSCC.String(), HumanReadable: o.SSCC.HumanReadable(), ElementString: "00" + o.SSCC.String(),
		Symbology: usecases.SsccLabelSymbology, SSCCSource: string(o.Carton.SSCCSource()), SKU: label.SKU, Units: label.Units,
		Discrepancies: toCartonDiscrepancies(o.Result.Discrepancies), Version: o.Receipt.Version(),
	}
}
