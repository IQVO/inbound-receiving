package housekeeping_test

import (
	"bytes"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/claudioed/inbound-receiving/internal/housekeeping"
)

// TestDuration pins how every housekeeping knob is read (gap fix D24):
// unset = the default; Go durations and plain seconds both parse; an
// explicit zero is preserved (it disables that part); a negative or
// malformed value logs a WARN and uses the default, so a typo never
// silently disables the sweep.
func TestDuration(t *testing.T) {
	const def = time.Hour
	cases := []struct {
		raw  string
		want time.Duration
		warn bool
	}{
		{"", def, false},
		{"30m", 30 * time.Minute, false},
		{"720h", 720 * time.Hour, false},
		{"90", 90 * time.Second, false},
		{"0", 0, false},
		{"0s", 0, false},
		{"-1h", def, true},
		{"-5", def, true},
		{"soon", def, true},
	}
	for _, c := range cases {
		t.Run(c.raw, func(t *testing.T) {
			var buf bytes.Buffer
			logger := slog.New(slog.NewTextHandler(&buf, nil))
			env := func(k string) string {
				if k == "OUTBOX_RETENTION" {
					return c.raw
				}
				return ""
			}
			if got := housekeeping.Duration(env, "OUTBOX_RETENTION", def, logger); got != c.want {
				t.Errorf("Duration(%q) = %v, want %v", c.raw, got, c.want)
			}
			warned := strings.Contains(buf.String(), "level=WARN") && strings.Contains(buf.String(), "OUTBOX_RETENTION")
			if warned != c.warn {
				t.Errorf("Duration(%q) warned=%v, want %v (log: %s)", c.raw, warned, c.warn, buf.String())
			}
		})
	}
}
