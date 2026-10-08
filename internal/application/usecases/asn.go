package usecases

import (
	"context"
	"errors"
	"time"

	"github.com/claudioed/inbound-receiving/internal/application/ports"
	"github.com/claudioed/inbound-receiving/internal/application/repository"
	"github.com/claudioed/inbound-receiving/internal/domain/asn"
)

// AsnLineInput is one requested ASN line.
type AsnLineInput struct {
	LineNo      int
	SKU         string
	ExpectedQty int64
}

// RegisterAsnCommand is the input of RegisterAsn. A zero ExpectedArrival
// means the supplier gave none.
type RegisterAsnCommand struct {
	AsnNumber       string
	SupplierRef     string
	ExpectedArrival time.Time
	Lines           []AsnLineInput
}

// RegisterAsn registers an advance ship notice. With SkuMode kafka every
// line's SKU must be in the known_skus local copy (ADR 0003).
type RegisterAsn struct {
	Writer
	SkuMode Mode
	Skus    ports.SkuDirectory
}

// Handle runs the use case. An existing number is ErrAsnAlreadyExists.
func (uc *RegisterAsn) Handle(ctx context.Context, cmd RegisterAsnCommand) (*asn.Asn, error) {
	lines := make([]asn.LineInput, 0, len(cmd.Lines))
	for _, l := range cmd.Lines {
		lines = append(lines, asn.LineInput{LineNo: l.LineNo, SKU: l.SKU, ExpectedQty: l.ExpectedQty})
	}
	a, events, err := asn.Register(cmd.AsnNumber, cmd.SupplierRef, cmd.ExpectedArrival, lines, uc.now())
	if err != nil {
		return nil, err
	}
	if err := uc.checkSkus(ctx, a); err != nil {
		return nil, err
	}
	err = uc.UoW.Do(ctx, func(ctx context.Context) error {
		_, err := uc.Asns.Get(ctx, a.Number())
		if err == nil {
			return ErrAsnAlreadyExists
		}
		if !errors.Is(err, repository.ErrAsnNotFound) {
			return err
		}
		if err := uc.saveAsn(ctx, a, 0, events); err != nil {
			if errors.Is(err, repository.ErrConcurrentModification) {
				return ErrAsnAlreadyExists
			}
			return err
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return a, nil
}

func (uc *RegisterAsn) checkSkus(ctx context.Context, a *asn.Asn) error {
	if uc.SkuMode != ModeKafka {
		return nil
	}
	for _, l := range a.Lines() {
		ok, err := uc.Skus.Exists(ctx, string(l.SKU()))
		if err != nil {
			return err
		}
		if !ok {
			return unknownSKU(string(l.SKU()))
		}
	}
	return nil
}

// CancelAsnCommand is the input of CancelAsn. ExpectedVersion 0 means the
// caller sent no If-Match.
type CancelAsnCommand struct {
	AsnNumber       string
	Reason          string
	ExpectedVersion int64
}

// CancelAsn cancels a Registered ASN.
type CancelAsn struct {
	Writer
}

// Handle runs the use case.
func (uc *CancelAsn) Handle(ctx context.Context, cmd CancelAsnCommand) (*asn.Asn, error) {
	number, err := asn.NewNumber(cmd.AsnNumber)
	if err != nil {
		return nil, err
	}
	var out *asn.Asn
	err = uc.UoW.Do(ctx, func(ctx context.Context) error {
		a, err := uc.Asns.Get(ctx, number)
		if err != nil {
			return err
		}
		loaded := a.Version()
		if err := checkVersion(cmd.ExpectedVersion, loaded); err != nil {
			return err
		}
		events, err := a.Cancel(cmd.Reason, uc.now())
		if err != nil {
			return err
		}
		if err := uc.saveAsn(ctx, a, loaded, events); err != nil {
			return err
		}
		out = a
		return nil
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}
