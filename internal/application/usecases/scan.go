package usecases

import (
	"context"
	"errors"
	"fmt"
	"strings"

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
