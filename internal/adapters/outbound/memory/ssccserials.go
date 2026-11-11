package memory

import (
	"context"
	"sync"

	"github.com/claudioed/inbound-receiving/internal/application/ports"
	"github.com/claudioed/inbound-receiving/internal/application/repository"
	"github.com/claudioed/inbound-receiving/internal/domain/receipt"
)

// SsccSerials is the in-memory ports.SsccSerials for tests and the BDD world: a
// counter per company prefix that gives 1, 2, 3, ... and never the same twice.
type SsccSerials struct {
	mu   sync.Mutex
	last map[string]int64
}

var _ ports.SsccSerials = (*SsccSerials)(nil)

// NewSsccSerials constructs an empty SsccSerials.
func NewSsccSerials() *SsccSerials { return &SsccSerials{last: map[string]int64{}} }

// EnsureSequence is a no-op: the counter of a prefix exists as soon as it is used.
func (s *SsccSerials) EnsureSequence(context.Context, receipt.CompanyPrefix) error { return nil }

// NextSerial implements ports.SsccSerials.
func (s *SsccSerials) NextSerial(_ context.Context, prefix receipt.CompanyPrefix) (int64, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	next := s.last[prefix.String()] + 1
	if next > prefix.MaxSerial() {
		return 0, repository.ErrSsccSerialsExhausted
	}
	s.last[prefix.String()] = next
	return next, nil
}
