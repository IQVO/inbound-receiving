package shared

import (
	"errors"
	"strings"
	"testing"
)

func TestNewSKU(t *testing.T) {
	valid := []string{"SKU-1", "a", strings.Repeat("x", MaxSKULength), "ÅÄÖ-1", "a.b_c-d"}
	for _, v := range valid {
		got, err := NewSKU(v)
		if err != nil || got.String() != v {
			t.Errorf("NewSKU(%q) = %q, %v; want valid", v, got, err)
		}
	}
}

func TestNewSKURejects(t *testing.T) {
	invalid := map[string]string{
		"empty":         "",
		"too long":      strings.Repeat("x", MaxSKULength+1),
		"space":         "SKU 1",
		"leading space": " SKU",
		"tab":           "SKU\t1",
		"slash":         "SKU/1",
		"control":       "SKU\x001",
		"leading ctl":   "\x07SKU",
	}
	for name, v := range invalid {
		if got, err := NewSKU(v); !errors.Is(err, ErrInvalidSKU) || got != "" {
			t.Errorf("%s: NewSKU(%q) = %q, %v; want ErrInvalidSKU", name, v, got, err)
		}
	}
}

func TestNewSKUCountsCharactersNotBytes(t *testing.T) {
	// 64 two-byte runes is 128 bytes but a valid 64-character SKU.
	if _, err := NewSKU(strings.Repeat("Å", MaxSKULength)); err != nil {
		t.Fatalf("64 runes rejected: %v", err)
	}
	if _, err := NewSKU(strings.Repeat("Å", MaxSKULength+1)); !errors.Is(err, ErrInvalidSKU) {
		t.Fatalf("65 runes accepted: %v", err)
	}
}

func TestValidateQuantity(t *testing.T) {
	for _, q := range []int64{1, 2, 40, MaxQuantity - 1, MaxQuantity} {
		if err := ValidateQuantity(q); err != nil {
			t.Errorf("ValidateQuantity(%d) = %v; want nil", q, err)
		}
	}
	for _, q := range []int64{0, -1, MaxQuantity + 1, -MaxQuantity} {
		if err := ValidateQuantity(q); !errors.Is(err, ErrInvalidQuantity) {
			t.Errorf("ValidateQuantity(%d) = %v; want ErrInvalidQuantity", q, err)
		}
	}
}

func TestValidateReason(t *testing.T) {
	for _, r := range []string{"", "supplier cancelled", strings.Repeat("r", MaxReasonLength), strings.Repeat("å", MaxReasonLength)} {
		if err := ValidateReason(r); err != nil {
			t.Errorf("ValidateReason(%q) = %v; want nil", r, err)
		}
	}
	for _, r := range []string{strings.Repeat("r", MaxReasonLength+1), "a\nb", "\ttab", "\x00"} {
		if err := ValidateReason(r); !errors.Is(err, ErrInvalidReason) {
			t.Errorf("ValidateReason(%q) = %v; want ErrInvalidReason", r, err)
		}
	}
}
