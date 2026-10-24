package shared

import (
	"errors"
	"strings"
	"testing"
)

func TestNewOwnerRef(t *testing.T) {
	valid := []string{"", "a", "z", "A", "Z", "0", "9", "tenant-7f3a", "a.b_c-d", strings.Repeat("o", MaxOwnerRefLength)}
	for _, v := range valid {
		got, err := NewOwnerRef(v)
		if err != nil || got.String() != v {
			t.Errorf("NewOwnerRef(%q) = %q, %v; want valid", v, got, err)
		}
	}
}

func TestNewOwnerRefRejects(t *testing.T) {
	invalid := map[string]string{
		"too long":   strings.Repeat("o", MaxOwnerRefLength+1),
		"space":      "a b",
		"slash":      "a/b",
		"colon":      "a:b",
		"at":         "a@b",
		"bracket":    "a[b",
		"backtick":   "a`b",
		"brace":      "a{b",
		"non ascii":  "tenant-é",
		"control":    "a\x00b",
		"before a":   "@",
		"after z":    "{",
		"before 0":   "/",
		"after 9":    ":",
		"after Z":    "[",
		"before Z+1": "`",
		"comma":      "a,b",
		"plus":       "a+b",
	}
	for name, v := range invalid {
		if got, err := NewOwnerRef(v); !errors.Is(err, ErrInvalidOwnerRef) || got != "" {
			t.Errorf("%s: NewOwnerRef(%q) = %q, %v; want ErrInvalidOwnerRef", name, v, got, err)
		}
	}
}

func TestParsePackLevel(t *testing.T) {
	for _, v := range []PackLevel{PackEach, PackInner, PackCase, PackPallet} {
		got, err := ParsePackLevel(string(v))
		if err != nil || got != v || got.String() != string(v) {
			t.Errorf("ParsePackLevel(%q) = %q, %v", v, got, err)
		}
	}
	for _, v := range []string{"", "each", "Case", "UNIT", "CASE "} {
		if got, err := ParsePackLevel(v); !errors.Is(err, ErrInvalidPackLevel) || got != "" {
			t.Errorf("ParsePackLevel(%q) = %q, %v; want ErrInvalidPackLevel", v, got, err)
		}
	}
}
