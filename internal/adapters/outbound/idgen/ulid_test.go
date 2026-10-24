package idgen

import (
	"math/big"
	"strings"
	"testing"
	"time"
)

// decode reads a 26-character Crockford base32 string back into 128 bits.
func decode(t *testing.T, s string) *big.Int {
	t.Helper()
	if len(s) != 26 {
		t.Fatalf("ulid %q has %d characters", s, len(s))
	}
	n := new(big.Int)
	for _, c := range s {
		i := strings.IndexRune(crockford, c)
		if i < 0 {
			t.Fatalf("ulid %q has the character %q", s, c)
		}
		n.Lsh(n, 5)
		n.Or(n, big.NewInt(int64(i)))
	}
	return n
}

func TestULIDCarriesTheMillisecondTimestampInItsTop48Bits(t *testing.T) {
	for _, at := range []time.Time{time.UnixMilli(0), time.UnixMilli(1), time.UnixMilli(1792838400123), time.UnixMilli(1<<48 - 1)} {
		n := decode(t, newULID(at))
		if n.BitLen() > 128 {
			t.Fatalf("more than 128 bits: %d", n.BitLen())
		}
		ms := new(big.Int).Rsh(n, 80).Int64()
		if ms != at.UnixMilli() {
			t.Fatalf("timestamp = %d, want %d", ms, at.UnixMilli())
		}
	}
}

func TestULIDRandomPartDiffersBetweenCalls(t *testing.T) {
	at := time.UnixMilli(1792838400123)
	a, b := decode(t, newULID(at)), decode(t, newULID(at))
	mask := new(big.Int).Sub(new(big.Int).Lsh(big.NewInt(1), 80), big.NewInt(1))
	if new(big.Int).And(a, mask).Cmp(new(big.Int).And(b, mask)) == 0 {
		t.Fatal("two ULIDs of the same millisecond share their 80 random bits")
	}
}

func TestULIDsSortByTime(t *testing.T) {
	early, late := newULID(time.UnixMilli(1000)), newULID(time.UnixMilli(2000))
	if early >= late {
		t.Fatalf("%s must sort before %s", early, late)
	}
}
