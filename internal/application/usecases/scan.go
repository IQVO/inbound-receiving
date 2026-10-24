package usecases

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/claudioed/inbound-receiving/internal/application/ports"
	"github.com/claudioed/inbound-receiving/internal/application/repository"
	"github.com/claudioed/inbound-receiving/internal/domain/receipt"
	"github.com/claudioed/inbound-receiving/internal/domain/shared"
	"github.com/claudioed/inbound-receiving/internal/domain/shared/gs1"
)

// ScanKind is what a scan resolves to (contracts section 6).
type ScanKind string

// The scan kinds.
const (
	// ScanSSCC identifies a carton: its manifest is accepted.
	ScanSSCC ScanKind = "SSCC"
	// ScanCaseGTIN identifies cases by their case GTIN: cases x units per pack.
	ScanCaseGTIN ScanKind = "CASE_GTIN"
	// ScanEachGTIN identifies single units by their each GTIN.
	ScanEachGTIN ScanKind = "EACH_GTIN"
)

func parseScanKind(value string) (ScanKind, error) {
	switch ScanKind(value) {
	case ScanSSCC, ScanCaseGTIN, ScanEachGTIN:
		return ScanKind(value), nil
	default:
		return "", fmt.Errorf("%w: %q", ErrInvalidScanKind, value)
	}
}

// ReceiveScanCommand is the input of ReceiveScan. Give EITHER Scan (the raw
// string a scanner delivered, parsed with gs1.ParseScan) OR Kind and ID (a
// client that already parsed it; the service re-validates). Kind is also the
// kind the screen expects: with a raw Scan, ExpectedKind plays that part and
// may be empty. Qty is cases for a case GTIN and eaches for an each GTIN
// (default 1); it is ignored for an SSCC. Condition defaults to Good.
type ReceiveScanCommand struct {
	ReceiptID       string
	Scan            string
	Kind            string
	ID              string
	Qty             int64
	ExpectedKind    string
	Condition       string
	Lot             string
	Expiry          string
	CountedContents []CountedContent
	ExpectedVersion int64
}

// CountedContent is one line of the audit count of a mixed carton: Qty
// eaches of SKU (0 = none found).
type CountedContent struct {
	SKU string
	Qty int64
}

// MaxCountedContents bounds the audit count of one scan.
const MaxCountedContents = 200

// ScanOutcome is the answer of ReceiveScan: the receipt after the scan, what
// the scan resolved to and what it did.
type ScanOutcome struct {
	Receipt *receipt.Receipt
	Kind    ScanKind
	Result  receipt.ScanResult
}

// ReceiveScan receives by scan (ADR 0012 decision 6): an SSCC accepts the
// carton's manifest, a case GTIN books cases x units per pack, an each GTIN
// books eaches. Every booking goes through the receipt's own rules, so
// ReceiptLineReceived stays the sole quantity authority and the carton events
// are identity only. A scan that is refused with a finding (unexpected,
// duplicate, unknown GTIN, case quantity) is NOT an error: it commits the
// finding as CartonDiscrepancyRaised and books nothing. A scan of the wrong
// kind is an error and changes nothing.
type ReceiveScan struct {
	Writer
	Packaging ports.PackagingDirectory
	// Limits are the carton handling limits of ADR 0014; zero = unchecked.
	Limits  receipt.CartonLimits
	metrics ports.ReceiptMetrics
}

// WithMetrics attaches the business-metric port (optional).
func (uc *ReceiveScan) WithMetrics(m ports.ReceiptMetrics) *ReceiveScan {
	uc.metrics = m
	return uc
}

// scanInput is a command after parsing: what was scanned and what the caller
// expected.
type scanInput struct {
	expected  ScanKind // "" when the caller did not say
	sscc      gs1.SSCC
	gtin      gs1.GTIN
	labelGTIN gs1.GTIN // AI 02 of a case label
	labelQty  int64    // AI 37 of a case label
	qty       int64
	lot       string
	expiry    string
	counted   []CountedContent
}

// Handle runs the use case.
func (uc *ReceiveScan) Handle(ctx context.Context, cmd ReceiveScanCommand) (ScanOutcome, error) {
	id, err := receipt.NewID(cmd.ReceiptID)
	if err != nil {
		return ScanOutcome{}, err
	}
	condition, err := receipt.ParseCondition(defaultCondition(cmd.Condition))
	if err != nil {
		return ScanOutcome{}, err
	}
	in, err := parseScan(cmd)
	if err != nil {
		return ScanOutcome{}, err
	}
	var out ScanOutcome
	err = uc.UoW.Do(ctx, func(ctx context.Context) error {
		r, err := uc.Receipts.Get(ctx, id)
		if err != nil {
			return err
		}
		loaded := r.Version()
		if err := checkVersion(cmd.ExpectedVersion, loaded); err != nil {
			return err
		}
		kind, res, err := uc.dispatch(ctx, r, in, condition)
		if err != nil {
			return err
		}
		if err := uc.saveReceipt(ctx, r, loaded, res.Events); err != nil {
			return err
		}
		out = ScanOutcome{Receipt: r, Kind: kind, Result: res}
		return nil
	})
	if err != nil {
		return ScanOutcome{}, err
	}
	uc.recordMetrics(ctx, out.Result)
	return out, nil
}

func defaultCondition(c string) string {
	if c == "" {
		return string(receipt.ConditionGood)
	}
	return c
}

// recordMetrics counts the units accepted once the unit of work committed.
func (uc *ReceiveScan) recordMetrics(ctx context.Context, res receipt.ScanResult) {
	if uc.metrics == nil {
		return
	}
	for _, e := range res.Events {
		if line, ok := e.(receipt.ReceiptLineReceived); ok {
			uc.metrics.LineReceived(ctx, strings.ToLower(string(line.Condition)), line.Quantity)
		}
	}
}

// parseScan turns the command into a scanInput without touching any port.
func parseScan(cmd ReceiveScanCommand) (scanInput, error) {
	in := scanInput{qty: cmd.Qty, lot: cmd.Lot, expiry: cmd.Expiry, counted: cmd.CountedContents}
	if len(in.counted) > MaxCountedContents {
		return scanInput{}, fmt.Errorf("%w: at most %d counted contents", ErrInvalidScan, MaxCountedContents)
	}
	if cmd.Scan != "" {
		if cmd.Kind != "" || cmd.ID != "" {
			return scanInput{}, fmt.Errorf("%w: give either a raw scan or a kind and id, not both", ErrInvalidScan)
		}
		return parseRawScan(in, cmd)
	}
	kind, err := parseScanKind(cmd.Kind)
	if err != nil {
		return scanInput{}, err
	}
	in.expected = kind
	if len(in.counted) > 0 && kind != ScanSSCC {
		return scanInput{}, fmt.Errorf("%w: countedContents apply to an SSCC scan only", ErrInvalidScan)
	}
	if kind == ScanSSCC {
		if in.sscc, err = gs1.ParseSSCC(cmd.ID); err != nil {
			return scanInput{}, err
		}
		return in, nil
	}
	if in.gtin, err = gs1.ParseGTIN(cmd.ID); err != nil {
		return scanInput{}, err
	}
	return in, nil
}

func parseRawScan(in scanInput, cmd ReceiveScanCommand) (scanInput, error) {
	if cmd.ExpectedKind != "" {
		kind, err := parseScanKind(cmd.ExpectedKind)
		if err != nil {
			return scanInput{}, err
		}
		in.expected = kind
	}
	scan, err := gs1.ParseScan(cmd.Scan)
	if err != nil {
		return scanInput{}, fmt.Errorf("%w: %w", ErrInvalidScan, err)
	}
	in.sscc, in.gtin, in.labelGTIN, in.labelQty = scan.SSCC, scan.GTIN, scan.ContentGTIN, int64(scan.Count)
	if len(in.counted) > 0 && in.sscc.IsZero() {
		return scanInput{}, fmt.Errorf("%w: countedContents apply to an SSCC scan only", ErrInvalidScan)
	}
	if in.lot == "" {
		in.lot = scan.Lot
	}
	if in.expiry == "" {
		in.expiry = scan.Expiry.String()
	}
	return in, nil
}

// dispatch resolves the scan against the PackagingView and runs the matching
// receipt operation.
func (uc *ReceiveScan) dispatch(ctx context.Context, r *receipt.Receipt, in scanInput, condition receipt.Condition) (ScanKind, receipt.ScanResult, error) {
	now := uc.now()
	if !in.sscc.IsZero() {
		if err := expectKind(in.expected, ScanSSCC); err != nil {
			return "", receipt.ScanResult{}, err
		}
		res, err := uc.acceptCarton(ctx, r, in, condition, now)
		return ScanSSCC, res, err
	}
	level, err := uc.Packaging.ByGTIN(ctx, in.gtin)
	if errors.Is(err, repository.ErrPackLevelNotFound) {
		res, err := r.RejectUnknownGtin(in.gtin, now)
		return gtinKind(in.expected), res, err
	}
	if err != nil {
		return "", receipt.ScanResult{}, err
	}
	switch level.Level {
	case shared.PackCase:
		return uc.receiveCases(r, in, level, condition, now)
	case shared.PackEach:
		return uc.receiveEaches(r, in, level, condition, now)
	default:
		return "", receipt.ScanResult{}, fmt.Errorf("%w: %s", ErrUnsupportedPackLevel, level.Level)
	}
}

// gtinKind is the kind reported for a GTIN that did not resolve: the one the
// caller expected, else the each GTIN.
func gtinKind(expected ScanKind) ScanKind {
	if expected == ScanCaseGTIN {
		return ScanCaseGTIN
	}
	return ScanEachGTIN
}

func expectKind(expected, resolved ScanKind) error {
	if expected != "" && expected != resolved {
		return fmt.Errorf("%w: the scan is %s but %s was expected", ErrScanKindMismatch, resolved, expected)
	}
	return nil
}

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
