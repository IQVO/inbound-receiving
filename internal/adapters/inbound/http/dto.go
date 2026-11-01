package http

import (
	"time"

	"github.com/claudioed/inbound-receiving/internal/application/usecases"
	"github.com/claudioed/inbound-receiving/internal/domain/asn"
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
