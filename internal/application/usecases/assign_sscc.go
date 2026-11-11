package usecases

import (
	"context"
	"errors"
	"fmt"

	"github.com/claudioed/inbound-receiving/internal/application/ports"
	"github.com/claudioed/inbound-receiving/internal/application/repository"
	"github.com/claudioed/inbound-receiving/internal/domain/receipt"
	"github.com/claudioed/inbound-receiving/internal/domain/shared"
	"github.com/claudioed/inbound-receiving/internal/domain/shared/gs1"
)

// AssignSsccCommand is the input of AssignSscc: the case GTIN of a case that
// arrived without an SSCC label. Condition defaults to Good; ExpectedVersion 0
// means "not sent".
type AssignSsccCommand struct {
	ReceiptID       string
	GTIN            string
	Condition       string
	Lot             string
	Expiry          string
	ExpectedVersion int64
}

// AssignOutcome is the answer of AssignSscc: the receipt after the command, the
// SSCC allocated, the carton it labels and what the receipt did.
type AssignOutcome struct {
	Receipt *receipt.Receipt
	SSCC    gs1.SSCC
	Carton  receipt.Carton
	Result  receipt.ScanResult
}

// AssignSscc gives a case that arrived without a label an SSCC of the
// receiver's own (ADR 0024): it allocates the next serial under the configured
// GS1 company prefix and receives ONE case of the scanned case GTIN under that
// SSCC (marked receiver-assigned). Without a configured prefix it answers
// receipt.ErrCompanyPrefixNotConfigured and changes nothing: the prefix is a
// licensed asset the business supplies, there is no default.
type AssignSscc struct {
	Writer
	Packaging ports.PackagingDirectory
	// Prefix is RECEIVER_GS1_COMPANY_PREFIX; the zero value is "not configured".
	Prefix  receipt.CompanyPrefix
	Serials ports.SsccSerials
}

// Handle runs the use case.
func (uc *AssignSscc) Handle(ctx context.Context, cmd AssignSsccCommand) (AssignOutcome, error) {
	id, err := receipt.NewID(cmd.ReceiptID)
	if err != nil {
		return AssignOutcome{}, err
	}
	gtin, err := gs1.ParseGTIN(cmd.GTIN)
	if err != nil {
		return AssignOutcome{}, err
	}
	condition, err := receipt.ParseCondition(defaultCondition(cmd.Condition))
	if err != nil {
		return AssignOutcome{}, err
	}
	if uc.Prefix.IsZero() {
		return AssignOutcome{}, receipt.ErrCompanyPrefixNotConfigured
	}
	var out AssignOutcome
	err = uc.UoW.Do(ctx, func(ctx context.Context) error {
		r, err := uc.Receipts.Get(ctx, id)
		if err != nil {
			return err
		}
		loaded := r.Version()
		if err := checkVersion(cmd.ExpectedVersion, loaded); err != nil {
			return err
		}
		// Everything that can refuse comes before a serial is taken.
		if r.State() != receipt.StateOpen {
			return receipt.ErrReceiptClosed
		}
		level, err := uc.caseLevel(ctx, r, gtin)
		if err != nil {
			return err
		}
		sscc, err := uc.allocate(ctx)
		if err != nil {
			return err
		}
		res, err := r.AssignSscc(receipt.CaseScan{
			SKU: level.SKU, Pack: receipt.CasePack{GTIN: level.GTIN, UnitsPerPack: level.UnitsPerPack}, ChildGTIN: level.ChildGTIN,
			Cases: 1, Condition: condition, Lot: cmd.Lot, Expiry: cmd.Expiry, LPNs: []string{sscc.String()}, AssignedSSCC: sscc,
		}, uc.now())
		if err != nil {
			return err
		}
		if err := uc.saveReceipt(ctx, r, loaded, res.Events); err != nil {
			return err
		}
		out = AssignOutcome{Receipt: r, SSCC: sscc, Carton: res.Cartons[0], Result: res}
		return nil
	})
	if err != nil {
		return AssignOutcome{}, err
	}
	return out, nil
}

// caseLevel resolves the GTIN to its CASE level and checks its SKU is on the ASN.
func (uc *AssignSscc) caseLevel(ctx context.Context, r *receipt.Receipt, gtin gs1.GTIN) (repository.PackLevel, error) {
	level, err := uc.Packaging.ByGTIN(ctx, gtin)
	if errors.Is(err, repository.ErrPackLevelNotFound) {
		return repository.PackLevel{}, fmt.Errorf("%w: %s", ErrUnknownGtin, gtin)
	}
	if err != nil {
		return repository.PackLevel{}, err
	}
	if level.Level != shared.PackCase {
		return repository.PackLevel{}, fmt.Errorf("%w: AssignSscc needs a case GTIN, %s is %s", ErrScanKindMismatch, gtin, level.Level)
	}
	if _, ok := r.LineOfSKU(level.SKU); !ok {
		return repository.PackLevel{}, receipt.ErrLineNotOnAsn
	}
	return level, nil
}

func (uc *AssignSscc) allocate(ctx context.Context) (gs1.SSCC, error) {
	serial, err := uc.Serials.NextSerial(ctx, uc.Prefix)
	if err != nil {
		return gs1.SSCC{}, err
	}
	return uc.Prefix.Build(serial)
}
