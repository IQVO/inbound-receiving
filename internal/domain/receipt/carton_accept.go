package receipt

import (
	"strconv"
	"strings"
	"time"

	"github.com/claudioed/inbound-receiving/internal/domain/asn"
	"github.com/claudioed/inbound-receiving/internal/domain/shared"
	"github.com/claudioed/inbound-receiving/internal/domain/shared/gs1"
)

// AcceptCarton accepts the manifest of the scanned SSCC: every content line
// is booked through the same rules as ReceiveLine (so ReceiptLineReceived
// stays the sole quantity authority), then the carton's identity is recorded
// on ReceiptCartonReceived. A scan that cannot be accepted books nothing and
// raises its findings as CartonDiscrepancyRaised; the receipt still changes
// version, because the findings are part of its history.
func (r *Receipt) AcceptCarton(in CartonScan, at time.Time) (ScanResult, error) {
	if err := r.requireOpen(); err != nil {
		return ScanResult{}, err
	}
	if !in.Condition.valid() {
		return ScanResult{}, ErrInvalidCondition
	}
	idx := r.cartonIndex(in.SSCC)
	if idx >= 0 && r.cartons[idx].status == CartonAccepted {
		return r.reject(at, []Carton{r.cartons[idx]}, CartonDiscrepancy{Kind: KindDuplicateSscc, SSCC: in.SSCC}), nil
	}
	if in.ReceivedElsewhere {
		return r.reject(at, nil, CartonDiscrepancy{Kind: KindDuplicateSscc, SSCC: in.SSCC}), nil
	}
	if idx < 0 {
		return r.reject(at, nil, CartonDiscrepancy{Kind: KindUnexpectedCarton, SSCC: in.SSCC}), nil
	}
	if len(in.Counted) > 0 && r.cartons[idx].kind != asn.CartonMixed {
		return ScanResult{}, ErrCountedContentsNotMixed
	}
	if findings := checkCarton(r.cartons[idx], in); len(findings) > 0 {
		return r.reject(at, nil, findings...), nil
	}
	return r.acceptCarton(idx, in, at)
}

// checkCarton compares a homogeneous carton with what its label says and with
// the active CASE level of its SKU. A mixed carton has no pack GTIN and a
// label that need not be homogeneous, so it is never checked.
func checkCarton(c Carton, in CartonScan) []CartonDiscrepancy {
	if c.kind != asn.CartonHomogeneousCase {
		return nil
	}
	sku, qty := c.contents[0].SKU, cartonQty(c)
	var out []CartonDiscrepancy
	if !in.LabelContentGTIN.IsZero() {
		if in.LabelContentSKU == "" {
			out = append(out, CartonDiscrepancy{Kind: KindUnknownGtin, SSCC: in.SSCC, Actual: in.LabelContentGTIN.String()})
		} else if in.LabelContentSKU != sku {
			out = append(out, CartonDiscrepancy{Kind: KindMixedCaseDeclaredAsCasePack, SSCC: in.SSCC, Expected: string(sku), Actual: string(in.LabelContentSKU)})
		}
	}
	if in.LabelCount != 0 && in.LabelCount != qty {
		out = append(out, mismatch(in.SSCC, qty, in.LabelCount))
	}
	if pack, ok := in.CasePacks[sku]; ok && pack.UnitsPerPack != qty {
		out = append(out, mismatch(in.SSCC, pack.UnitsPerPack, qty))
	}
	return out
}

func mismatch(sscc gs1.SSCC, expected, actual int64) CartonDiscrepancy {
	return CartonDiscrepancy{Kind: KindCaseQtyMismatch, SSCC: sscc, Expected: strconv.FormatInt(expected, 10), Actual: strconv.FormatInt(actual, 10)}
}

func cartonQty(c Carton) int64 {
	var total int64
	for _, x := range c.contents {
		total += x.Qty
	}
	return total
}

func (r *Receipt) acceptCarton(idx int, in CartonScan, at time.Time) (ScanResult, error) {
	c := r.cartons[idx]
	lines := r.Lines()
	manifest := c.contents
	var findings []CartonDiscrepancy
	if len(in.Counted) > 0 {
		var err error
		if manifest, findings, err = r.audit(c, in); err != nil {
			return ScanResult{}, err
		}
	}
	contents := make([]CartonContent, 0, len(manifest))
	var events []Event
	for _, x := range manifest {
		if err := bookLine(lines, x.LineNo, x.Qty, in.Condition); err != nil {
			return ScanResult{}, err
		}
		x.Condition = in.Condition
		contents = append(contents, x)
		line := ReceiptLineReceived{
			Header: r.header(at), LineNo: x.LineNo, SKU: x.SKU, Quantity: x.Qty, Condition: in.Condition,
			OwnerRef: r.ownerRef, SSCC: in.SSCC,
		}
		if pack, ok := in.CasePacks[x.SKU]; ok && c.kind == asn.CartonHomogeneousCase {
			line.PackGTIN, line.Cases = pack.GTIN, 1
		}
		events = append(events, line)
	}
	c.contents, c.status, c.sealed, c.receivedAt = contents, CartonAccepted, true, at.UTC()
	events = append(events, r.cartonReceived(c, at))
	if f, over := overLimit(in); over {
		findings = append(findings, f)
	}
	events = append(events, r.raise(findings, at)...)
	r.lines, r.cartons[idx] = lines, c
	r.version++
	return ScanResult{Events: events, Accepted: true, Cartons: []Carton{c}, Discrepancies: findings}, nil
}

// overLimit compares the declared weight and longest side with the limits;
// one finding names every breached limit (ADR 0014).
func overLimit(in CartonScan) (CartonDiscrepancy, bool) {
	var expected, actual []string
	if in.Limits.MaxGrossWeightG > 0 && in.Declared.GrossWeightG > in.Limits.MaxGrossWeightG {
		expected = append(expected, "grossWeightG:"+strconv.FormatInt(in.Limits.MaxGrossWeightG, 10))
		actual = append(actual, "grossWeightG:"+strconv.FormatInt(in.Declared.GrossWeightG, 10))
	}
	if in.Limits.MaxLongestSideMm > 0 && in.Declared.LongestSideMm > in.Limits.MaxLongestSideMm {
		expected = append(expected, "longestSideMm:"+strconv.FormatInt(in.Limits.MaxLongestSideMm, 10))
		actual = append(actual, "longestSideMm:"+strconv.FormatInt(in.Declared.LongestSideMm, 10))
	}
	if len(expected) == 0 {
		return CartonDiscrepancy{}, false
	}
	return CartonDiscrepancy{Kind: KindCartonOverLimit, SSCC: in.SSCC, Expected: strings.Join(expected, ","), Actual: strings.Join(actual, ",")}, true
}

// audit turns the counted contents of a mixed carton into the contents that
// are received (the COUNTED quantities) and one MixedCartonContentMismatch
// per SKU whose count differs from the manifest.
func (r *Receipt) audit(c Carton, in CartonScan) ([]CartonContent, []CartonDiscrepancy, error) {
	counted := make(map[shared.SKU]CountedContent, len(in.Counted))
	for _, x := range in.Counted {
		if _, dup := counted[x.SKU]; dup || x.Qty < 0 {
			return nil, nil, ErrInvalidCount
		}
		counted[x.SKU] = x
	}
	var contents []CartonContent
	var findings []CartonDiscrepancy
	listed := map[shared.SKU]bool{}
	for _, m := range c.contents {
		listed[m.SKU] = true
		got := counted[m.SKU].Qty
		if got != m.Qty {
			findings = append(findings, skuMismatch(in.SSCC, m.SKU, m.Qty, got))
		}
		if got > 0 {
			m.Qty = got
			contents = append(contents, m)
		}
	}
	for _, x := range in.Counted {
		if listed[x.SKU] {
			continue
		}
		lineNo, ok := r.LineOfSKU(x.SKU)
		if !ok {
			return nil, nil, ErrLineNotOnAsn
		}
		if x.Qty > 0 {
			findings = append(findings, skuMismatch(in.SSCC, x.SKU, 0, x.Qty))
			contents = append(contents, CartonContent{LineNo: lineNo, SKU: x.SKU, GTIN: x.GTIN, Qty: x.Qty})
		}
	}
	return contents, findings, nil
}

func skuMismatch(sscc gs1.SSCC, sku shared.SKU, expected, actual int64) CartonDiscrepancy {
	return CartonDiscrepancy{
		Kind: KindMixedCartonContentMismatch, SSCC: sscc,
		Expected: string(sku) + ":" + strconv.FormatInt(expected, 10), Actual: string(sku) + ":" + strconv.FormatInt(actual, 10),
	}
}

func (r *Receipt) cartonReceived(c Carton, at time.Time) ReceiptCartonReceived {
	return ReceiptCartonReceived{
		Header: r.header(at), OwnerRef: r.ownerRef, SSCC: c.sscc, ParentSSCC: c.parentSscc,
		Sealed: c.sealed, LPN: c.lpn, Contents: c.Contents(),
	}
}
