package memory

import (
	"context"
	"errors"
	"testing"

	"github.com/claudioed/inbound-receiving/internal/application/repository"
	"github.com/claudioed/inbound-receiving/internal/domain/receipt"
)

func TestSsccSerialsAreMonotonicAndPerPrefix(t *testing.T) {
	s := NewSsccSerials()
	seven, _ := receipt.ParseCompanyPrefix("0614141")
	other, _ := receipt.ParseCompanyPrefix("0614142")
	ctx := context.Background()
	for want := int64(1); want <= 5; want++ {
		if got, err := s.NextSerial(ctx, seven); err != nil || got != want {
			t.Fatalf("NextSerial = %d, %v; want %d", got, err, want)
		}
	}
	if got, err := s.NextSerial(ctx, other); err != nil || got != 1 {
		t.Fatalf("a second prefix starts at 1: %d, %v", got, err)
	}
	if got, _ := s.NextSerial(ctx, seven); got != 6 {
		t.Fatalf("the first prefix goes on at 6, got %d", got)
	}
}

func TestSsccSerialsRunOutAtTheLargestSerialThatFits(t *testing.T) {
	s := NewSsccSerials()
	ten, _ := receipt.ParseCompanyPrefix("0614141123")
	s.last[ten.String()] = 999998
	if got, err := s.NextSerial(context.Background(), ten); err != nil || got != 999999 {
		t.Fatalf("the last serial = %d, %v", got, err)
	}
	if _, err := s.NextSerial(context.Background(), ten); !errors.Is(err, repository.ErrSsccSerialsExhausted) {
		t.Fatalf("past the last serial: %v", err)
	}
	if got := s.last[ten.String()]; got != 999999 {
		t.Fatalf("an exhausted call must not advance the counter: %d", got)
	}
}
