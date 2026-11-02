package usecases

import (
	"context"
	"time"

	"github.com/claudioed/inbound-receiving/internal/application/ports"
	"github.com/claudioed/inbound-receiving/internal/application/repository"
	"github.com/claudioed/inbound-receiving/internal/domain/asn"
	"github.com/claudioed/inbound-receiving/internal/domain/receipt"
	"github.com/claudioed/inbound-receiving/internal/domain/shared"
)

// Statuses of a carton in GET /asns/{number}/cartons.
const (
	// CartonStatusExpected: announced, not yet received (the receipt is open or absent).
	CartonStatusExpected = "expected"
	// CartonStatusAccepted: its SSCC was scanned and the manifest accepted.
	CartonStatusAccepted = "accepted"
	// CartonStatusMissing: the receipt closed without it.
	CartonStatusMissing = "missing"
)

// AsnCartonItem is one announced carton with its receive status.
type AsnCartonItem struct {
	Carton asn.Carton
	Status string
}

// AsnCartons is the carton tree of an ASN with the receive status of each
// carton. A flat ASN has no items.
type AsnCartons struct {
	Asn   *asn.Asn
	Items []AsnCartonItem
}

// GetAsnCartons returns the announced cartons of an ASN and, from its
// receipt, which of them were accepted or went missing. (`audited` is
// reserved for the carton audit, which is not built yet.)
type GetAsnCartons struct {
	Asns     ports.AsnRepository
	Receipts ports.ReceiptRepository
}

// Handle validates the number and loads the ASN
// (repository.ErrAsnNotFound when unknown).
func (uc *GetAsnCartons) Handle(ctx context.Context, rawNumber string) (AsnCartons, error) {
	number, err := asn.NewNumber(rawNumber)
	if err != nil {
		return AsnCartons{}, err
	}
	a, err := uc.Asns.Get(ctx, number)
	if err != nil {
		return AsnCartons{}, err
	}
	out := AsnCartons{Asn: a, Items: []AsnCartonItem{}}
	cartons := a.Cartons()
	if len(cartons) == 0 {
		return out, nil
	}
	receipts, err := uc.Receipts.List(ctx, repository.ReceiptFilter{AsnNumber: number}, "", 1)
	if err != nil {
		return AsnCartons{}, err
	}
	for _, c := range cartons {
		out.Items = append(out.Items, AsnCartonItem{Carton: c, Status: cartonStatus(c, receipts)})
	}
	return out, nil
}

func cartonStatus(c asn.Carton, receipts []*receipt.Receipt) string {
	if len(receipts) == 0 {
		return CartonStatusExpected
	}
	r := receipts[0]
	if got, ok := r.Carton(c.SSCC()); ok && got.Status() == receipt.CartonAccepted {
		return CartonStatusAccepted
	}
	if r.State() == receipt.StateClosed {
		return CartonStatusMissing
	}
	return CartonStatusExpected
}

// CartonLabel is the DATA of a license plate label (R11); the floor client
// renders and prints it, the service has no printer driver.
type CartonLabel struct {
	LPN           string
	Symbology     string
	HumanReadable string
	SKU           string
	Units         int64
	ReceivedAt    time.Time
}

// LabelSymbology is the barcode symbology of every license plate label.
const LabelSymbology = "CODE128"

// GetCartonLabel returns the label data of an accepted carton of a receipt.
type GetCartonLabel struct {
	Receipts ports.ReceiptRepository
}

// Handle validates the receipt id and finds the carton by its LPN
// (ErrCartonNotFound when the receipt has none accepted).
func (uc *GetCartonLabel) Handle(ctx context.Context, rawID, lpn string) (CartonLabel, error) {
	id, err := receipt.NewID(rawID)
	if err != nil {
		return CartonLabel{}, err
	}
	r, err := uc.Receipts.Get(ctx, id)
	if err != nil {
		return CartonLabel{}, err
	}
	c, ok := r.CartonByLPN(lpn)
	if !ok || c.Status() != receipt.CartonAccepted {
		return CartonLabel{}, ErrCartonNotFound
	}
	return labelOf(c), nil
}

func labelOf(c receipt.Carton) CartonLabel {
	label := CartonLabel{LPN: c.LPN(), Symbology: LabelSymbology, HumanReadable: c.LPN(), ReceivedAt: c.ReceivedAt()}
	if !c.SSCC().IsZero() {
		label.HumanReadable = "(00)" + c.SSCC().String()
	}
	skus := map[shared.SKU]bool{}
	for _, x := range c.Contents() {
		label.Units += x.Qty
		skus[x.SKU] = true
		label.SKU = string(x.SKU)
	}
	if len(skus) != 1 {
		label.SKU = ""
	}
	return label
}
