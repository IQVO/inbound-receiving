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
