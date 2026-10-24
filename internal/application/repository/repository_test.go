package repository

import (
	"testing"
	"time"
)

func TestPackLevelActiveAt(t *testing.T) {
	from := time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)
	to := from.Add(24 * time.Hour)
	open := PackLevel{EffectiveFrom: from}
	closed := PackLevel{EffectiveFrom: from, EffectiveTo: to}
	cases := []struct {
		name  string
		level PackLevel
		at    time.Time
		want  bool
	}{
		{"before the start", open, from.Add(-time.Nanosecond), false},
		{"at the start", open, from, true},
		{"long after an open level starts", open, from.Add(1000 * time.Hour), true},
		{"just before the end", closed, to.Add(-time.Nanosecond), true},
		{"at the end", closed, to, false},
		{"after the end", closed, to.Add(time.Hour), false},
	}
	for _, tc := range cases {
		if got := tc.level.ActiveAt(tc.at); got != tc.want {
			t.Errorf("%s: ActiveAt = %v, want %v", tc.name, got, tc.want)
		}
	}
}
