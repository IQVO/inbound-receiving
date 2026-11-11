package postgres

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/claudioed/inbound-receiving/internal/application/ports"
	"github.com/claudioed/inbound-receiving/internal/application/repository"
	"github.com/claudioed/inbound-receiving/internal/domain/receipt"
)

// SsccSerials is the Postgres ports.SsccSerials (ADR 0024): one SEQUENCE per
// company prefix, named sscc_serial_<prefix> (the prefix is validated digits, so
// the name is safe), NO CYCLE and capped at the largest serial that fits after the
// prefix. nextval is not transactional: a serial is never given twice, even when the
// caller's transaction rolls back (a gap is allowed). It runs on the pool, not in the
// caller's unit of work, so concurrent receivers never wait on each other.
type SsccSerials struct {
	pool *pgxpool.Pool
}

var _ ports.SsccSerials = (*SsccSerials)(nil)

// NewSsccSerials constructs an SsccSerials over pool.
func NewSsccSerials(pool *pgxpool.Pool) *SsccSerials { return &SsccSerials{pool: pool} }

const (
	undefinedTable       = "42P01"
	sequenceLimitReached = "2200H"
	// ssccSequenceLock serialises the creation of a sequence across replicas:
	// CREATE SEQUENCE IF NOT EXISTS is not race-free on its own.
	ssccSequenceLock = 7249117001
)

func sequenceName(prefix receipt.CompanyPrefix) string { return "sscc_serial_" + prefix.String() }

// EnsureSequence creates the prefix's sequence when it does not exist (idempotent,
// race-free across replicas). It is called at boot, so a missing privilege shows at
// startup, and again by NextSerial when the sequence turns out to be missing.
func (s *SsccSerials) EnsureSequence(ctx context.Context, prefix receipt.CompanyPrefix) error {
	if prefix.IsZero() {
		return receipt.ErrCompanyPrefixNotConfigured
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("ensure sscc sequence: %w", err)
	}
	defer func() { _ = tx.Rollback(context.WithoutCancel(ctx)) }()
	if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock($1)`, int64(ssccSequenceLock)); err != nil {
		return fmt.Errorf("lock sscc sequence creation: %w", err)
	}
	ddl := fmt.Sprintf(`CREATE SEQUENCE IF NOT EXISTS %s AS BIGINT START 1 INCREMENT 1 MINVALUE 1 MAXVALUE %d NO CYCLE`,
		sequenceName(prefix), prefix.MaxSerial())
	if _, err := tx.Exec(ctx, ddl); err != nil {
		return fmt.Errorf("create sscc sequence %s: %w", sequenceName(prefix), err)
	}
	return tx.Commit(ctx)
}

// NextSerial implements ports.SsccSerials.
func (s *SsccSerials) NextSerial(ctx context.Context, prefix receipt.CompanyPrefix) (int64, error) {
	if prefix.IsZero() {
		return 0, receipt.ErrCompanyPrefixNotConfigured
	}
	n, err := s.next(ctx, prefix)
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) && pgErr.Code == undefinedTable {
		if err := s.EnsureSequence(ctx, prefix); err != nil {
			return 0, err
		}
		n, err = s.next(ctx, prefix)
	}
	if errors.As(err, &pgErr) && pgErr.Code == sequenceLimitReached {
		return 0, repository.ErrSsccSerialsExhausted
	}
	if err != nil {
		return 0, fmt.Errorf("next sscc serial of prefix %s: %w", prefix, err)
	}
	return n, nil
}

func (s *SsccSerials) next(ctx context.Context, prefix receipt.CompanyPrefix) (int64, error) {
	var n int64
	err := s.pool.QueryRow(ctx, fmt.Sprintf(`SELECT nextval('%s')`, sequenceName(prefix))).Scan(&n)
	return n, err
}
