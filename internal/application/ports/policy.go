package ports

import (
	"context"

	"github.com/claudioed/inbound-receiving/internal/domain/policy"
	"github.com/claudioed/inbound-receiving/internal/domain/shared"
)

// ReceivingPolicyRepository persists ReceivingPolicy aggregates by
// (ownerRef, supplierRef) (ADR 0026). Inside a UnitOfWork it joins the
// transaction carried in ctx.
type ReceivingPolicyRepository interface {
	// Get returns the policy of the exact (ownerRef, supplierRef) key, or
	// repository.ErrPolicyNotFound. There is no fallback to the house
	// default: a client-owned receipt without a client policy is a
	// no-policy receipt.
	Get(ctx context.Context, ownerRef shared.OwnerRef, supplierRef string) (*policy.ReceivingPolicy, error)
	// Replace persists the policy as given (create-or-replace, the upsert of
	// the declaration): one row per key, the version from the aggregate. A
	// concurrent replace loses whole, never field by field.
	Replace(ctx context.Context, p *policy.ReceivingPolicy) error
}
