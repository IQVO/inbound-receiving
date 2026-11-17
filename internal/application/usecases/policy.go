package usecases

import (
	"context"
	"errors"

	"github.com/claudioed/inbound-receiving/internal/application/repository"
	"github.com/claudioed/inbound-receiving/internal/domain/asn"
	"github.com/claudioed/inbound-receiving/internal/domain/policy"
	"github.com/claudioed/inbound-receiving/internal/domain/receipt"
	"github.com/claudioed/inbound-receiving/internal/domain/shared"
)

// DeclareReceivingPolicyCommand is the input of DeclareReceivingPolicy, the
// declarative admin command of ADR 0026. OwnerRef is empty for the house
// default. ExpectedVersion 0 means "not sent".
type DeclareReceivingPolicyCommand struct {
	OwnerRef        string
	SupplierRef     string
	OverTolerance   float64
	UnderTolerance  float64
	OverHardStop    float64
	UnderHardStop   float64
	ExpectedVersion int64
}

// DeclareReceivingPolicy creates or replaces the receiving policy of one
// (ownerRef, supplierRef) pair and raises ReceivingPolicyDeclared in the same
// unit of work (ADR 0026). It is a declaration, not a booking: no receipt is
// touched here; the enforcement seams read the policy at close and scan time.
type DeclareReceivingPolicy struct {
	Writer
}

// Handle runs the use case and returns the stored policy and whether this
// call created it (false = replaced). A replace of a missing row is a create
// (version 1); ExpectedVersion guards nothing here — the declaration is
// idempotent through the HTTP Idempotency-Key and last-write-wins by design.
func (uc *DeclareReceivingPolicy) Handle(ctx context.Context, cmd DeclareReceivingPolicyCommand) (*policy.ReceivingPolicy, bool, error) {
	var out *policy.ReceivingPolicy
	created := false
	err := uc.UoW.Do(ctx, func(ctx context.Context) error {
		loaded := int64(0)
		if existing, err := uc.Policies.Get(ctx, mustOwnerRef(cmd.OwnerRef), cmd.SupplierRef); err == nil {
			loaded = existing.Version()
			created = false
			if err := checkVersion(cmd.ExpectedVersion, loaded); err != nil {
				return err
			}
		} else if errors.Is(err, repository.ErrPolicyNotFound) {
			created = true
		} else {
			return err
		}
		p, event, err := policy.Declare(cmd.OwnerRef, cmd.SupplierRef, policy.BandInput{
			Over: cmd.OverTolerance, OverStop: cmd.OverHardStop,
			Under: cmd.UnderTolerance, UnderStop: cmd.UnderHardStop,
		}, loaded, uc.now())
		if err != nil {
			return err
		}
		if err := uc.Policies.Replace(ctx, p); err != nil {
			return err
		}
		msgs, err := uc.Encoder.EncodePolicy(event)
		if err := uc.enqueue(ctx, msgs, err); err != nil {
			return err
		}
		out = p
		return nil
	})
	return out, created, err
}

func mustOwnerRef(ref string) shared.OwnerRef { return shared.OwnerRef(ref) }

// GetReceivingPolicy is the read side of the admin route: the policy of the
// exact (ownerRef, supplierRef) key (ADR 0026).
type GetReceivingPolicy struct {
	Writer
}

// Handle returns the policy or repository.ErrPolicyNotFound.
func (uc *GetReceivingPolicy) Handle(ctx context.Context, ownerRef, supplierRef string) (*policy.ReceivingPolicy, error) {
	return uc.Policies.Get(ctx, mustOwnerRef(ownerRef), supplierRef)
}

// appliedPolicy resolves the tolerance the receipt's close or scan is judged
// under (ADR 0026): the exact (ownerRef, supplierRef) policy — the ownerRef
// from the receipt, the supplier from its ASN — or the zero AppliedPolicy
// when none is declared (or no policy store is wired, which only a test may
// do). asn_ is the receipt's ASN, already loaded by the caller.
func (w Writer) appliedPolicy(ctx context.Context, r *receipt.Receipt, a *asn.Asn) (receipt.AppliedPolicy, error) {
	if w.Policies == nil {
		return receipt.AppliedPolicy{}, nil
	}
	p, err := w.Policies.Get(ctx, r.OwnerRef(), a.SupplierRef())
	if errors.Is(err, repository.ErrPolicyNotFound) {
		return receipt.AppliedPolicy{}, nil
	}
	if err != nil {
		return receipt.AppliedPolicy{}, err
	}
	return receipt.AppliedPolicy{Band: receipt.Band{
		Over: p.OverTolerance(), OverStop: p.OverHardStop(),
		Under: p.UnderTolerance(), UnderStop: p.UnderHardStop(),
	}}, nil
}

// parseOverrideReason validates an optional reason code from a command: empty
// is "not given" (valid), anything else must be on the closed list of ADR
// 0026.
func parseOverrideReason(value string) (receipt.OverrideReason, error) {
	if value == "" {
		return "", nil
	}
	return receipt.ParseOverrideReason(value)
}

// bookedLines returns the line numbers a scan booked, in first-seen order,
// from the ReceiptLineReceived events it raised.
func bookedLines(events []receipt.Event) []int {
	var out []int
	seen := map[int]bool{}
	for _, e := range events {
		if line, ok := e.(receipt.ReceiptLineReceived); ok && !seen[line.LineNo] {
			seen[line.LineNo] = true
			out = append(out, line.LineNo)
		}
	}
	return out
}

// enforceScanTolerance is the carton-accept seam of ADR 0026: after a scan
// booked its lines (the aggregate's in-memory state), the over side of those
// lines is judged against the policy. A refusal must abort the unit of work,
// so nothing is saved. asn_ is the receipt's ASN; now is unused booking time
// kept for symmetry with the close.
func (uc *ReceiveScan) enforceScanTolerance(ctx context.Context, r *receipt.Receipt, a *asn.Asn, events []receipt.Event, reason receipt.OverrideReason) error {
	lines := bookedLines(events)
	if len(lines) == 0 {
		return nil
	}
	applied, err := uc.appliedPolicy(ctx, r, a)
	if err != nil {
		return err
	}
	if !applied.Band.OverDeclared() {
		return nil
	}
	return r.CheckScanTolerance(applied.Band, lines, reason)
}
