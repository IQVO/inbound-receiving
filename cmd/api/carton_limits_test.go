package main

import (
	"bytes"
	"log/slog"
	"strings"
	"testing"
)

// TestCartonLimits pins CARTON_MAX_GROSS_WEIGHT_G and CARTON_MAX_LONGEST_SIDE_MM
// (ADR 0014): unset or 0 is unchecked, a bad value is unchecked with a warning.
func TestCartonLimits(t *testing.T) {
	tests := []struct {
		name         string
		weight, side string
		wantW, wantS int64
		warn         bool
	}{
		{name: "unset is unchecked"},
		{name: "explicit values", weight: "20000", side: "1200", wantW: 20000, wantS: 1200},
		{name: "zero is unchecked", weight: "0", side: "0"},
		{name: "negative weight is unchecked", weight: "-1", warn: true},
		{name: "garbage side is unchecked", side: "long", warn: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var buf bytes.Buffer
			logger := slog.New(slog.NewTextHandler(&buf, nil))
			env := map[string]string{envCartonMaxGrossWeightG: tt.weight, envCartonMaxLongestSideMm: tt.side}
			got := cartonLimits(func(k string) string { return env[k] }, logger)
			if got.MaxGrossWeightG != tt.wantW || got.MaxLongestSideMm != tt.wantS {
				t.Fatalf("cartonLimits = %+v, want weight %d side %d", got, tt.wantW, tt.wantS)
			}
			if warned := strings.Contains(buf.String(), "level=WARN"); warned != tt.warn {
				t.Fatalf("warned = %v, want %v (log: %s)", warned, tt.warn, buf.String())
			}
		})
	}
}
