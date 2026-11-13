package memory

import (
	"context"
	"sync"

	"github.com/claudioed/inbound-receiving/internal/application/repository"
	"github.com/claudioed/inbound-receiving/internal/domain/policy"
	"github.com/claudioed/inbound-receiving/internal/domain/shared"
)

// PolicyRepo is the in-memory ports.ReceivingPolicyRepository (ADR 0026):
// one map keyed (ownerRef, supplierRef), snapshotting like every participant
// of the in-memory UnitOfWork so a failed declaration rolls back.
type PolicyRepo struct {
	mu       sync.Mutex
	policies map[string]*policy.ReceivingPolicy
}

// NewPolicyRepo constructs an empty PolicyRepo.
func NewPolicyRepo() *PolicyRepo { return &PolicyRepo{policies: map[string]*policy.ReceivingPolicy{}} }

var _ interface {
	Snapshot() (restore func())
} = (*PolicyRepo)(nil)

func key(ownerRef shared.OwnerRef, supplierRef string) string {
	return string(ownerRef) + "\x00" + supplierRef
}

// Get implements ports.ReceivingPolicyRepository: the EXACT key's policy.
func (r *PolicyRepo) Get(_ context.Context, ownerRef shared.OwnerRef, supplierRef string) (*policy.ReceivingPolicy, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	p, ok := r.policies[key(ownerRef, supplierRef)]
	if !ok {
		return nil, repository.ErrPolicyNotFound
	}
	stored := *p
	return &stored, nil
}

// Replace implements ports.ReceivingPolicyRepository (create-or-replace).
func (r *PolicyRepo) Replace(_ context.Context, p *policy.ReceivingPolicy) error {
	stored := *p
	r.mu.Lock()
	defer r.mu.Unlock()
	r.policies[key(stored.OwnerRef(), stored.SupplierRef())] = &stored
	return nil
}

// Snapshot captures the store for a rollback.
func (r *PolicyRepo) Snapshot() (restore func()) {
	r.mu.Lock()
	defer r.mu.Unlock()
	copied := make(map[string]*policy.ReceivingPolicy, len(r.policies))
	for k, p := range r.policies {
		stored := *p
		copied[k] = &stored
	}
	return func() {
		r.mu.Lock()
		defer r.mu.Unlock()
		r.policies = copied
	}
}
