package postgres

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/claudioed/inbound-receiving/internal/application/ports"
	"github.com/claudioed/inbound-receiving/internal/application/repository"
	"github.com/claudioed/inbound-receiving/internal/domain/policy"
	"github.com/claudioed/inbound-receiving/internal/domain/shared"
)

// PolicyRepo is the pgx-backed ports.ReceivingPolicyRepository over
// receiving_policies (ADR 0026): one row per (owner_ref, supplier_ref), an
// empty owner_ref being the house default. Replace is the create-or-replace
// upsert of the declaration; the aggregate's own validation already refused
// a malformed band, and the table's CHECK is the second line of defence.
type PolicyRepo struct {
	pool *pgxpool.Pool
}

// NewPolicyRepo constructs a PolicyRepo over pool.
func NewPolicyRepo(pool *pgxpool.Pool) *PolicyRepo { return &PolicyRepo{pool: pool} }

var _ ports.ReceivingPolicyRepository = (*PolicyRepo)(nil)

// Get implements ports.ReceivingPolicyRepository: the EXACT key's policy, no
// house-default fallback.
func (r *PolicyRepo) Get(ctx context.Context, ownerRef shared.OwnerRef, supplierRef string) (*policy.ReceivingPolicy, error) {
	var p policy.Persisted
	err := queryFor(ctx, r.pool).QueryRow(ctx,
		`SELECT owner_ref, supplier_ref, over_tolerance, under_tolerance, over_hard_stop, under_hard_stop, version, declared_at
		 FROM receiving_policies WHERE owner_ref = $1 AND supplier_ref = $2`,
		string(ownerRef), supplierRef).Scan(
		&p.OwnerRef, &p.SupplierRef, &p.Over, &p.Under, &p.OverStop, &p.UnderStop, &p.Version, &p.DeclaredAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, repository.ErrPolicyNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("get receiving policy %s/%s: %w", ownerRef, supplierRef, err)
	}
	return policy.Rehydrate(p)
}

// Replace implements ports.ReceivingPolicyRepository (create-or-replace).
func (r *PolicyRepo) Replace(ctx context.Context, p *policy.ReceivingPolicy) error {
	stored := p.Persisted()
	_, err := queryFor(ctx, r.pool).Exec(ctx,
		`INSERT INTO receiving_policies (owner_ref, supplier_ref, over_tolerance, under_tolerance, over_hard_stop, under_hard_stop, version, declared_at)
		 VALUES ($1, $2, $3, $4, $5, $6, $7, $8)
		 ON CONFLICT (owner_ref, supplier_ref) DO UPDATE SET
		   over_tolerance  = EXCLUDED.over_tolerance,
		   under_tolerance = EXCLUDED.under_tolerance,
		   over_hard_stop  = EXCLUDED.over_hard_stop,
		   under_hard_stop = EXCLUDED.under_hard_stop,
		   version         = EXCLUDED.version,
		   declared_at     = EXCLUDED.declared_at`,
		stored.OwnerRef, stored.SupplierRef, stored.Over, stored.Under, stored.OverStop, stored.UnderStop, stored.Version, stored.DeclaredAt.UTC())
	if err != nil {
		return fmt.Errorf("replace receiving policy %s: %w", p.Key(), err)
	}
	return nil
}
