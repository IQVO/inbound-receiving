package usecases

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/claudioed/inbound-receiving/internal/application/repository"
	"github.com/claudioed/inbound-receiving/internal/domain/receipt"
	"github.com/claudioed/inbound-receiving/internal/domain/shared"
	"github.com/claudioed/inbound-receiving/internal/domain/shared/gs1"
)

func (uc *ReceiveScan) acceptCarton(ctx context.Context, r *receipt.Receipt, in scanInput, condition receipt.Condition, now time.Time) (receipt.ScanResult, error) {
	scan := receipt.CartonScan{SSCC: in.sscc, Condition: condition, LabelCount: in.labelQty, LabelContentGTIN: in.labelGTIN}
	elsewhere, err := uc.Receipts.ReceivedSsccs(ctx, []gs1.SSCC{in.sscc})
	if err != nil {
		return receipt.ScanResult{}, err
	}
	scan.ReceivedElsewhere = len(elsewhere) > 0
	if carton, ok := r.Carton(in.sscc); ok {
		if scan.CasePacks, err = uc.casePacks(ctx, carton, now); err != nil {
			return receipt.ScanResult{}, err
		}
		if scan.Declared, err = uc.declared(ctx, r, in.sscc); err != nil {
			return receipt.ScanResult{}, err
		}
		scan.Limits = uc.Limits
		if scan.Counted, err = uc.counted(ctx, carton, in.counted, now); err != nil {
			return receipt.ScanResult{}, err
		}
	}
	if !in.labelGTIN.IsZero() {
		if scan.LabelContentSKU, err = uc.skuOf(ctx, in.labelGTIN); err != nil {
			return receipt.ScanResult{}, err
		}
	}
	return r.AcceptCarton(scan, now)
}

// declared reads the weight and longest side the ASN declared for the carton.
// Nothing is read when no limit is configured.
func (uc *ReceiveScan) declared(ctx context.Context, r *receipt.Receipt, sscc gs1.SSCC) (receipt.DeclaredCarton, error) {
	if uc.Limits.MaxGrossWeightG <= 0 && uc.Limits.MaxLongestSideMm <= 0 {
		return receipt.DeclaredCarton{}, nil
	}
	a, err := uc.Asns.Get(ctx, r.AsnNumber())
	if err != nil {
		return receipt.DeclaredCarton{}, err
	}
	for _, c := range a.Cartons() {
		if c.SSCC() != sscc {
			continue
		}
		out := receipt.DeclaredCarton{GrossWeightG: c.GrossWeightG()}
		if d, ok := c.Dims(); ok {
			out.LongestSideMm = max(d.LengthMm(), d.WidthMm(), d.HeightMm())
		}
		return out, nil
	}
	return receipt.DeclaredCarton{}, nil
}

// counted validates the audit count and gives every SKU the manifest does not
// list its each GTIN from the PackagingView.
func (uc *ReceiveScan) counted(ctx context.Context, carton receipt.Carton, in []CountedContent, now time.Time) ([]receipt.CountedContent, error) {
	listed := map[shared.SKU]bool{}
	for _, x := range carton.Contents() {
		listed[x.SKU] = true
	}
	out := make([]receipt.CountedContent, 0, len(in))
	for _, x := range in {
		sku, err := shared.NewSKU(x.SKU)
		if err != nil {
			return nil, err
		}
		item := receipt.CountedContent{SKU: sku, Qty: x.Qty}
		if !listed[sku] && x.Qty > 0 {
			level, err := uc.Packaging.ActiveLevel(ctx, sku, shared.PackEach, now)
			if errors.Is(err, repository.ErrPackLevelNotFound) {
				return nil, fmt.Errorf("%w: %s", ErrCountedSkuUnknown, sku)
			}
			if err != nil {
				return nil, err
			}
			item.GTIN = level.GTIN
		}
		out = append(out, item)
	}
	return out, nil
}

// casePacks reads the active CASE level of every SKU in the carton.
func (uc *ReceiveScan) casePacks(ctx context.Context, c receipt.Carton, now time.Time) (map[shared.SKU]receipt.CasePack, error) {
	packs := map[shared.SKU]receipt.CasePack{}
	for _, x := range c.Contents() {
		level, err := uc.Packaging.ActiveLevel(ctx, x.SKU, shared.PackCase, now)
		if errors.Is(err, repository.ErrPackLevelNotFound) {
			continue
		}
		if err != nil {
			return nil, err
		}
		packs[x.SKU] = receipt.CasePack{GTIN: level.GTIN, UnitsPerPack: level.UnitsPerPack}
	}
	return packs, nil
}

// skuOf resolves a GTIN to its SKU ("" when the PackagingView does not know it).
func (uc *ReceiveScan) skuOf(ctx context.Context, gtin gs1.GTIN) (shared.SKU, error) {
	level, err := uc.Packaging.ByGTIN(ctx, gtin)
	if errors.Is(err, repository.ErrPackLevelNotFound) {
		return "", nil
	}
	return level.SKU, err
}

func (uc *ReceiveScan) receiveCases(r *receipt.Receipt, in scanInput, level repository.PackLevel, condition receipt.Condition, now time.Time) (ScanKind, receipt.ScanResult, error) {
	if err := expectKind(in.expected, ScanCaseGTIN); err != nil {
		return "", receipt.ScanResult{}, err
	}
	cases := defaultQty(in.qty)
	if cases < 1 || cases > receipt.MaxCasesPerScan {
		return "", receipt.ScanResult{}, receipt.ErrInvalidCases
	}
	lpns := make([]string, 0, cases)
	for i := int64(0); i < cases; i++ {
		lpns = append(lpns, uc.IDs.NewLPN())
	}
	res, err := r.ReceiveCases(receipt.CaseScan{
		SKU: level.SKU, Pack: receipt.CasePack{GTIN: level.GTIN, UnitsPerPack: level.UnitsPerPack}, ChildGTIN: level.ChildGTIN,
		Cases: cases, Condition: condition, Lot: in.lot, Expiry: in.expiry, LPNs: lpns,
	}, now)
	return ScanCaseGTIN, res, err
}

func (uc *ReceiveScan) receiveEaches(r *receipt.Receipt, in scanInput, level repository.PackLevel, condition receipt.Condition, now time.Time) (ScanKind, receipt.ScanResult, error) {
	if err := expectKind(in.expected, ScanEachGTIN); err != nil {
		return "", receipt.ScanResult{}, err
	}
	lineNo, ok := r.LineOfSKU(level.SKU)
	if !ok {
		return "", receipt.ScanResult{}, receipt.ErrLineNotOnAsn
	}
	events, err := r.ReceiveLine(lineNo, defaultQty(in.qty), condition, now)
	if err != nil {
		return "", receipt.ScanResult{}, err
	}
	return ScanEachGTIN, receipt.ScanResult{Events: events, Accepted: true}, nil
}

// defaultQty reads an absent quantity as 1.
func defaultQty(q int64) int64 {
	if q == 0 {
		return 1
	}
	return q
}
