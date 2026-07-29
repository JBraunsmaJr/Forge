package provisioner

import (
	"context"
	"time"
)

type InstanceID string

type Instance struct {
	ID        InstanceID
	Pool      string // "hot" | "burst"
	Labels    map[string]string
	CreatedAt time.Time
}

// CloudProvisioner is the only boundary between the autoscaler's decision
// logic and a specific cloud (or a local fake). Every method must be safe
// to call concurrently and idempotent where the underlying API allows it.
type CloudProvisioner interface {
	// ScaleUp provisions n new instances in the given pool, returning only
	// the IDs of the newly created instances. Every method must be safe
	// to call concurrently and idempotent where the underlying API allows it.
	ScaleUp(ctx context.Context, pool string, n int, labels map[string]string) ([]InstanceID, error)

	// ScaleDown terminates specific instances by ID. Targeted, not
	// aggregate — the caller has already decided exactly which idle
	// instance to remove and must not leave that choice to the provider.
	ScaleDown(ctx context.Context, ids []InstanceID) error

	// ListInstances returns every instance this provisioner currently
	// manages, across both pools.
	ListInstances(ctx context.Context) ([]Instance, error)
}
