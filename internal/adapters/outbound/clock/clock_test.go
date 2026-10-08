package clock

import (
	"testing"
	"time"
)

func TestSystemIsUTCNow(t *testing.T) {
	before := time.Now()
	got := System{}.Now()
	if got.Location() != time.UTC || got.Before(before.Add(-time.Second)) || got.After(time.Now().Add(time.Second)) {
		t.Fatalf("Now = %v", got)
	}
}
