package analyticsstore

import (
	"testing"
)

func TestMemory_Contract(t *testing.T) {
	runContract(t, func(*testing.T) store { return NewMemory() })
}

func TestEarliestIgnoresNilLikeSQLLeast(t *testing.T) {
	a, b := at("2026-10-05T00:00:00Z"), at("2026-10-06T00:00:00Z")
	if earliest(nil, nil) != nil || !earliest(nil, &a).Equal(a) || !earliest(&a, nil).Equal(a) ||
		!earliest(&a, &b).Equal(a) || !earliest(&b, &a).Equal(a) {
		t.Fatal("earliest is not LEAST")
	}
	if got := earliest(&a, &a); got != &a {
		t.Fatal("a tie keeps the stored value")
	}
}

func TestSecondsNeverNegative(t *testing.T) {
	a, b := at("2026-10-05T00:00:00Z"), at("2026-10-05T00:01:00Z")
	if seconds(a, b) != 60 || seconds(b, a) != 0 {
		t.Fatalf("seconds = %v / %v, want 60 / 0", seconds(a, b), seconds(b, a))
	}
}
