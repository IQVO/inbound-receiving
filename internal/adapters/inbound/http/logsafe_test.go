package http

import "testing"

func TestSanitizeLog(t *testing.T) {
	cases := []struct{ in, want string }{
		{"", ""},
		{"clean value", "clean value"},
		{"line1\nline2", "line1line2"},
		{"cr\ronly", "cronly"},
		{"mixed\r\nend", "mixedend"},
	}
	for _, tc := range cases {
		if got := sanitizeLog(tc.in); got != tc.want {
			t.Errorf("sanitizeLog(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}
