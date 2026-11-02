package usecases

import (
	"fmt"

	"github.com/claudioed/inbound-receiving/internal/domain/shared/gs1"
)

func parseScanKind(value string) (ScanKind, error) {
	switch ScanKind(value) {
	case ScanSSCC, ScanCaseGTIN, ScanEachGTIN:
		return ScanKind(value), nil
	default:
		return "", fmt.Errorf("%w: %q", ErrInvalidScanKind, value)
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
