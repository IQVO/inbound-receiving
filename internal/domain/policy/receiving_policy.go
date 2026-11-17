// Package policy holds the ReceivingPolicy aggregate (ADR 0026): the
// over/under tolerance band one owner declares for one supplier. Inside the
// band a receipt's variance auto-accepts; between the band and the hard stop
// an override reason code is required; at or beyond the hard stop the close
// or scan is refused.
//
// The policy is keyed by (ownerRef, supplierRef): ownerRef-aware for the 3PL
// multi-client case, an empty ownerRef being the house default. The lookup at
// the enforcement seams is an exact match on the pair — there is no fallback
// to the house default for a client-owned receipt.
package policy

import (
	"errors"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/claudioed/inbound-receiving/internal/domain/asn"
	"github.com/claudioed/inbound-receiving/internal/domain/shared"
)

var (
	// ErrInvalidBand is returned when a side's tolerance and hard stop do not
	// satisfy 0 <= tolerance < hard_stop <= 1 (ADR 0026).
	ErrInvalidBand = errors.New("receiving policy must satisfy 0 <= tolerance < hard_stop <= 1 per side")
	// ErrInvalidVersion is returned when a persisted version is below 1.
	ErrInvalidVersion = errors.New("receiving policy version must be at least 1")
	// ErrInvalidDeclaredAt is returned when a persisted declaration time is
	// the zero time.
	ErrInvalidDeclaredAt = errors.New("receiving policy must carry a declaration time")
)

// maxSupplierRefLength mirrors the ASN's supplier rule so one supplier
// reference means one thing everywhere.
const maxSupplierRefLength = 64

// BandInput is the band of a declaration: the over and under tolerance ratios
// and their hard stops. A side left at zero tolerance with a positive hard
// stop means "every unit of variance on that side needs a reason code".
type BandInput struct {
	Over      float64
	OverStop  float64
	Under     float64
	UnderStop float64
}

// ReceivingPolicy is the aggregate root: what one owner declared for one
// supplier. It is created and replaced only through Declare.
type ReceivingPolicy struct {
	ownerRef    shared.OwnerRef
	supplierRef string
	over        float64
	overStop    float64
	under       float64
	underStop   float64
	declaredAt  time.Time
	version     int64
}

// Persisted is the stored shape of a ReceivingPolicy.
type Persisted struct {
	OwnerRef    string
	SupplierRef string
	Over        float64
	OverStop    float64
	Under       float64
	UnderStop   float64
	DeclaredAt  time.Time
	Version     int64
}

// Event is a domain event raised by the ReceivingPolicy aggregate.
type Event interface {
	EventName() string
	Key() string
	OccurredAt() time.Time
}

// ReceivingPolicyDeclared is raised when a policy is created or replaced
// (ADR 0026): a declaration other contexts will copy later.
type ReceivingPolicyDeclared struct {
	SupplierRef string
	// OwnerRef is empty for the house default.
	OwnerRef shared.OwnerRef
	BandInput
	Version    int64
	DeclaredAt time.Time
}

// EventName returns "ReceivingPolicyDeclared".
func (ReceivingPolicyDeclared) EventName() string { return "ReceivingPolicyDeclared" }

// Key returns the policy key (the supplier reference; see ADR 0026 point 6).
func (e ReceivingPolicyDeclared) Key() string { return e.SupplierRef }

// OccurredAt returns when the declaration happened (UTC).
func (e ReceivingPolicyDeclared) OccurredAt() time.Time { return e.DeclaredAt }

// Declare creates (loadedVersion 0) or replaces (loadedVersion >= 1) the
// policy of (ownerRef, supplierRef), validating the band and the key, and
// raises ReceivingPolicyDeclared. The replace bumps the version by one.
func Declare(ownerRef, supplierRef string, bands BandInput, loadedVersion int64, now time.Time) (*ReceivingPolicy, ReceivingPolicyDeclared, error) {
	if err := validateSupplierRef(supplierRef); err != nil {
		return nil, ReceivingPolicyDeclared{}, err
	}
	owner, err := shared.NewOwnerRef(ownerRef)
	if err != nil {
		return nil, ReceivingPolicyDeclared{}, err
	}
	if loadedVersion < 0 {
		return nil, ReceivingPolicyDeclared{}, ErrInvalidVersion
	}
	if err := validateBand(bands); err != nil {
		return nil, ReceivingPolicyDeclared{}, err
	}
	p := &ReceivingPolicy{
		ownerRef: owner, supplierRef: supplierRef,
		over: bands.Over, overStop: bands.OverStop, under: bands.Under, underStop: bands.UnderStop,
		declaredAt: now.UTC(), version: loadedVersion + 1,
	}
	return p, p.event(), nil
}

// Rehydrate rebuilds a persisted policy, re-validating every invariant.
func Rehydrate(p Persisted) (*ReceivingPolicy, error) {
	if p.DeclaredAt.IsZero() {
		return nil, ErrInvalidDeclaredAt
	}
	built, _, err := Declare(p.OwnerRef, p.SupplierRef, BandInput{Over: p.Over, OverStop: p.OverStop, Under: p.Under, UnderStop: p.UnderStop}, p.Version-1, p.DeclaredAt)
	if err != nil {
		return nil, err
	}
	return built, nil
}

// OwnerRef returns the owner ("" for the house default).
func (p *ReceivingPolicy) OwnerRef() shared.OwnerRef { return p.ownerRef }

// SupplierRef returns the supplier.
func (p *ReceivingPolicy) SupplierRef() string { return p.supplierRef }

// Key returns the policy's lookup key: ownerRef/supplierRef, or the supplier
// alone for the house default.
func (p *ReceivingPolicy) Key() string {
	if p.ownerRef == "" {
		return p.supplierRef
	}
	return string(p.ownerRef) + "/" + p.supplierRef
}

// OverTolerance returns the over tolerance ratio.
func (p *ReceivingPolicy) OverTolerance() float64 { return p.over }

// OverHardStop returns the over hard-stop ratio.
func (p *ReceivingPolicy) OverHardStop() float64 { return p.overStop }

// UnderTolerance returns the under tolerance ratio.
func (p *ReceivingPolicy) UnderTolerance() float64 { return p.under }

// UnderHardStop returns the under hard-stop ratio.
func (p *ReceivingPolicy) UnderHardStop() float64 { return p.underStop }

// DeclaredAt returns when this version was declared (UTC).
func (p *ReceivingPolicy) DeclaredAt() time.Time { return p.declaredAt }

// Version returns the current version.
func (p *ReceivingPolicy) Version() int64 { return p.version }

// Persisted returns the stored shape.
func (p *ReceivingPolicy) Persisted() Persisted {
	return Persisted{
		OwnerRef: string(p.ownerRef), SupplierRef: p.supplierRef,
		Over: p.over, OverStop: p.overStop, Under: p.under, UnderStop: p.underStop,
		DeclaredAt: p.declaredAt, Version: p.version,
	}
}

func (p *ReceivingPolicy) event() ReceivingPolicyDeclared {
	return ReceivingPolicyDeclared{
		SupplierRef: p.supplierRef, OwnerRef: p.ownerRef,
		BandInput: BandInput{Over: p.over, OverStop: p.overStop, Under: p.under, UnderStop: p.underStop},
		Version:   p.version, DeclaredAt: p.declaredAt,
	}
}

// validateBand pins 0 <= tolerance < hard_stop <= 1 per side (ADR 0026). A
// side may be entirely unset (both zero) only when the other side is set: a
// policy with no band at all is refused like any other malformed band.
func validateBand(b BandInput) error {
	over, under := sideValid(b.Over, b.OverStop), sideValid(b.Under, b.UnderStop)
	if over && under {
		return nil
	}
	if b.Over == 0 && b.OverStop == 0 && under {
		return nil
	}
	if b.Under == 0 && b.UnderStop == 0 && over {
		return nil
	}
	return ErrInvalidBand
}

// sideValid reports 0 <= tolerance < hard_stop <= 1 for one side. Both values
// zero is an UNSET side (valid only alongside a set one).
func sideValid(tolerance, stop float64) bool {
	if tolerance == 0 && stop == 0 {
		return true
	}
	return tolerance >= 0 && tolerance < stop && stop <= 1
}

// validateSupplierRef is the ASN's supplier rule, restated so the policy
// carries the same error (asn.ErrInvalidSupplierRef) and the REST problem it
// already maps to.
func validateSupplierRef(ref string) error {
	if strings.TrimSpace(ref) == "" || utf8.RuneCountInString(ref) > maxSupplierRefLength {
		return asn.ErrInvalidSupplierRef
	}
	if strings.IndexFunc(ref, unicode.IsControl) >= 0 {
		return asn.ErrInvalidSupplierRef
	}
	return nil
}
