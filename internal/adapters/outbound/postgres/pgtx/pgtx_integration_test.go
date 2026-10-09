//go:build integration

// Package pgtx_test proves the transaction-context mechanism against a REAL
// Postgres (testcontainers): postgres.UnitOfWork carries its pgx transaction
// through pgtx, the repositories transparently join it, and the
// save+publish bracket is atomic — the aggregate row and its outbox rows
// commit together or not at all. These are the invariants the transactional
// outbox pattern exists for; they cannot be proven without a real database.
//
// One container serves the whole package, migrated once into a template
// database; each test gets a private clone. Never an external DATABASE_URL,
// never t.Skip.
package pgtx_test

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"os"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/testcontainers/testcontainers-go"
	tcpostgres "github.com/testcontainers/testcontainers-go/modules/postgres"
	"github.com/testcontainers/testcontainers-go/wait"

	"github.com/claudioed/inbound-receiving/internal/adapters/outbound/idgen"
	outboundkafka "github.com/claudioed/inbound-receiving/internal/adapters/outbound/kafka"
	"github.com/claudioed/inbound-receiving/internal/adapters/outbound/postgres"
	"github.com/claudioed/inbound-receiving/internal/application/repository"
	"github.com/claudioed/inbound-receiving/internal/application/usecases"
	"github.com/claudioed/inbound-receiving/internal/domain/asn"
)

// One Postgres container serves the whole package (containers are slow to
// boot). TestMain starts it, applies the embedded migrations ONCE into a
// template database, and each test then gets its own database cloned from
// that template.
const templateDB = "pgtx_migrated_template"

var (
	sharedBaseURL string
	dbSeq         atomic.Uint64
)

func TestMain(m *testing.M) {
	os.Exit(runTests(m))
}

func runTests(m *testing.M) int {
	ctx := context.Background()
	container, err := tcpostgres.Run(ctx, "postgres:16-alpine",
		tcpostgres.WithDatabase("inbound_pgtx"),
		tcpostgres.WithUsername("inbound"),
		tcpostgres.WithPassword("inbound"),
		testcontainers.WithWaitStrategy(
			wait.ForLog("database system is ready to accept connections").
				WithOccurrence(2).
				WithStartupTimeout(90*time.Second),
		),
	)
	if err != nil {
		fmt.Fprintf(os.Stderr, "start postgres container: %v\n", err)
		return 1
	}
	defer func() {
		if err := testcontainers.TerminateContainer(container); err != nil {
			fmt.Fprintf(os.Stderr, "terminate postgres container: %v\n", err)
		}
	}()

	sharedBaseURL, err = container.ConnectionString(ctx, "sslmode=disable")
	if err != nil {
		fmt.Fprintf(os.Stderr, "postgres connection string: %v\n", err)
		return 1
	}
	if err := createDatabase(ctx, templateDB); err != nil {
		fmt.Fprintf(os.Stderr, "create template database: %v\n", err)
		return 1
	}
	if err := postgres.RunMigrations(withDB(sharedBaseURL, templateDB)); err != nil {
		fmt.Fprintf(os.Stderr, "migrate template: %v\n", err)
		return 1
	}
	return m.Run()
}

// withDB rewrites the path of a connection URL to the named database.
func withDB(baseURL, name string) string {
	u, err := url.Parse(baseURL)
	if err != nil {
		panic(err)
	}
	u.Path = "/" + name
	return u.String()
}

// createDatabase creates an empty database inside the shared container.
func createDatabase(ctx context.Context, name string) error {
	conn, err := pgx.Connect(ctx, sharedBaseURL)
	if err != nil {
		return fmt.Errorf("connect: %w", err)
	}
	defer conn.Close(ctx)
	if _, err := conn.Exec(ctx, fmt.Sprintf("CREATE DATABASE %q", name)); err != nil {
		return fmt.Errorf("create database %s: %w", name, err)
	}
	return nil
}

// migratedPool hands the test a pool on its own private database cloned
// from the migrated template.
func migratedPool(t *testing.T) *pgxpool.Pool {
	t.Helper()
	name := fmt.Sprintf("pgtx_%d", dbSeq.Add(1))
	conn, err := pgx.Connect(context.Background(), sharedBaseURL)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	defer conn.Close(context.Background())
	if _, err := conn.Exec(context.Background(), fmt.Sprintf(
		"CREATE DATABASE %q WITH TEMPLATE %q", name, templateDB)); err != nil {
		t.Fatalf("clone database: %v", err)
	}
	pool, err := postgres.NewPool(context.Background(), withDB(sharedBaseURL, name))
	if err != nil {
		t.Fatalf("open pool: %v", err)
	}
	t.Cleanup(pool.Close)
	return pool
}

// sentinel is a distinctive error the failing scopes return.
var sentinel = errors.New("pgtx-itest: deliberate failure inside the unit of work")

// counts reads the asns and outbox_events row counts of one database.
func counts(t *testing.T, pool *pgxpool.Pool) (asns, outbox int) {
	t.Helper()
	ctx := context.Background()
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM asns`).Scan(&asns); err != nil {
		t.Fatalf("count asns: %v", err)
	}
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM outbox_events`).Scan(&outbox); err != nil {
		t.Fatalf("count outbox_events: %v", err)
	}
	return asns, outbox
}

// TestPgtxUnitOfWorkCommitsSaveAndPublishAtomically proves the happy-path
// bracket: a RegisterAsn inside UnitOfWork writes the aggregate row AND its
// CloudEvents outbox row in ONE transaction — after commit both are
// visible; a second connection sees them too.
func TestPgtxUnitOfWorkCommitsSaveAndPublishAtomically(t *testing.T) {
	pool := migratedPool(t)
	ctx := context.Background()
	w := usecases.Writer{
		Asns:    postgres.NewAsnRepo(pool),
		Outbox:  postgres.NewOutboxRepo(pool),
		Encoder: outboundkafka.NewEncoder(),
		UoW:     postgres.NewUnitOfWork(pool),
		Clock:   systemClock{},
		IDs:     idgen.UUID{},
	}

	// Inside the unit of work the writes are visible on the SAME tx (a
	// repo read through pgtx joins it); after Do returns they are visible
	// to a fresh connection as well.
	seenInside := 0
	err := w.UoW.Do(ctx, func(ctx context.Context) error {
		a, events, err := asn.Register("ASN-PGTX-OK", "ACME", time.Time{},
			[]asn.LineInput{{LineNo: 1, SKU: "SKU-1", ExpectedQty: 3}}, time.Now().UTC())
		if err != nil {
			return err
		}
		if err := w.Asns.Save(ctx, a, 0); err != nil {
			return err
		}
		msgs, err := w.Encoder.EncodeAsn(events...)
		if err != nil {
			return err
		}
		if err := w.Outbox.Insert(ctx, msgs...); err != nil {
			return err
		}
		// The repo's Get runs on the unit-of-work transaction (pgtx), so
		// the just-saved row is readable before commit.
		if _, err := w.Asns.Get(ctx, a.Number()); err != nil {
			return fmt.Errorf("read inside the unit of work: %w", err)
		}
		seenInside++
		return nil
	})
	if err != nil {
		t.Fatalf("unit of work: %v", err)
	}
	if seenInside != 1 {
		t.Fatalf("the in-tx read never ran")
	}

	asns, outbox := counts(t, pool)
	if asns != 1 || outbox != 1 {
		t.Fatalf("after commit: asns %d outbox %d, want 1 and 1 (save+publish atomically)", asns, outbox)
	}
}

// TestPgtxUnitOfWorkRollsBackEverythingOnError proves the failure bracket:
// an error anywhere inside the scope — here raised AFTER the aggregate save
// and the outbox insert both succeeded — rolls back EVERYTHING. Neither the
// aggregate row nor the outbox row survives, and the number can be
// re-registered cleanly afterwards.
func TestPgtxUnitOfWorkRollsBackEverythingOnError(t *testing.T) {
	pool := migratedPool(t)
	ctx := context.Background()
	w := usecases.Writer{
		Asns:    postgres.NewAsnRepo(pool),
		Outbox:  postgres.NewOutboxRepo(pool),
		Encoder: outboundkafka.NewEncoder(),
		UoW:     postgres.NewUnitOfWork(pool),
		Clock:   systemClock{},
		IDs:     idgen.UUID{},
	}

	err := w.UoW.Do(ctx, func(ctx context.Context) error {
		a, events, err := asn.Register("ASN-PGTX-ROLL", "ACME", time.Time{},
			[]asn.LineInput{{LineNo: 1, SKU: "SKU-1", ExpectedQty: 3}}, time.Now().UTC())
		if err != nil {
			return err
		}
		if err := w.Asns.Save(ctx, a, 0); err != nil {
			return err
		}
		msgs, err := w.Encoder.EncodeAsn(events...)
		if err != nil {
			return err
		}
		if err := w.Outbox.Insert(ctx, msgs...); err != nil {
			return err
		}
		return sentinel // everything above must be rolled back
	})
	if !errors.Is(err, sentinel) {
		t.Fatalf("unit of work error = %v, want the sentinel", err)
	}

	asns, outbox := counts(t, pool)
	if asns != 0 || outbox != 0 {
		t.Fatalf("after rollback: asns %d outbox %d, want 0 and 0 (all-or-nothing)", asns, outbox)
	}

	// The number is free again: a clean registration succeeds and is the
	// only row left behind.
	if _, err := (&usecases.RegisterAsn{Writer: w}).Handle(ctx, usecases.RegisterAsnCommand{
		AsnNumber: "ASN-PGTX-ROLL", SupplierRef: "ACME",
		Lines: []usecases.AsnLineInput{{LineNo: 1, SKU: "SKU-1", ExpectedQty: 3}},
	}); err != nil {
		t.Fatalf("re-register after rollback: %v", err)
	}
	asns, outbox = counts(t, pool)
	if asns != 1 || outbox != 1 {
		t.Fatalf("after re-register: asns %d outbox %d, want 1 and 1", asns, outbox)
	}
}

// TestPgtxNestedDoJoinsTheOuterTransactionInsideASavepoint proves the
// nesting contract: a UnitOfWork.Do inside another Do joins the outer
// transaction through a SAVEPOINT — the inner scope's writes commit with
// the outer one, and an inner failure rolls back only the inner writes
// while the outer transaction stays usable (the idempotency middleware
// depends on exactly this).
func TestPgtxNestedDoJoinsTheOuterTransactionInsideASavepoint(t *testing.T) {
	pool := migratedPool(t)
	ctx := context.Background()
	uow := postgres.NewUnitOfWork(pool)
	repo := postgres.NewAsnRepo(pool)
	outboxRepo := postgres.NewOutboxRepo(pool)
	encoder := outboundkafka.NewEncoder()

	mustSave := func(ctx context.Context, number string) error {
		a, events, err := asn.Register(number, "ACME", time.Time{},
			[]asn.LineInput{{LineNo: 1, SKU: "SKU-1", ExpectedQty: 1}}, time.Now().UTC())
		if err != nil {
			return err
		}
		if err := repo.Save(ctx, a, 0); err != nil {
			return err
		}
		msgs, err := encoder.EncodeAsn(events...)
		if err != nil {
			return err
		}
		return outboxRepo.Insert(ctx, msgs...)
	}

	// Outer commit + inner commit: both survive.
	err := uow.Do(ctx, func(ctx context.Context) error {
		if err := mustSave(ctx, "ASN-PGTX-OUTER"); err != nil {
			return err
		}
		return uow.Do(ctx, func(ctx context.Context) error {
			return mustSave(ctx, "ASN-PGTX-INNER")
		})
	})
	if err != nil {
		t.Fatalf("nested commit: %v", err)
	}
	asns, outbox := counts(t, pool)
	if asns != 2 || outbox != 2 {
		t.Fatalf("after nested commit: asns %d outbox %d, want 2 and 2", asns, outbox)
	}

	// Outer commit + inner FAILURE: only the inner writes roll back; the
	// outer transaction commits its own writes.
	err = uow.Do(ctx, func(ctx context.Context) error {
		if err := mustSave(ctx, "ASN-PGTX-KEEP"); err != nil {
			return err
		}
		if err := uow.Do(ctx, func(ctx context.Context) error {
			if err := mustSave(ctx, "ASN-PGTX-DISCARD"); err != nil {
				return err
			}
			return sentinel
		}); !errors.Is(err, sentinel) {
			return fmt.Errorf("inner scope: %w", err)
		}
		// The outer transaction is still usable and commits.
		return nil
	})
	if err != nil {
		t.Fatalf("outer commit after inner failure: %v", err)
	}
	asns, outbox = counts(t, pool)
	if asns != 3 || outbox != 3 {
		t.Fatalf("after inner rollback: asns %d outbox %d, want 3 and 3 (only the inner writes rolled back)", asns, outbox)
	}

	// The discarded number is free again; the kept one is not.
	if _, err := repo.Get(ctx, asn.Number("ASN-PGTX-DISCARD")); !errors.Is(err, repository.ErrAsnNotFound) {
		t.Fatalf("discarded asn readable: %v", err)
	}
	if _, err := repo.Get(ctx, asn.Number("ASN-PGTX-KEEP")); err != nil {
		t.Fatalf("kept asn must survive: %v", err)
	}
}

// systemClock is the ports.Clock adapter for time.Now.
type systemClock struct{}

func (systemClock) Now() time.Time { return time.Now().UTC() }
