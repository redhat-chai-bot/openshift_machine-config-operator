package reconcile

import "context"

// Reconciler defines the key-based reconciliation interface for the build controller.
// Each method receives a string key (namespace/name for namespaced resources, or
// just name for cluster-scoped resources) and performs the necessary reconciliation.
//
// This interface replaces the old object-passing pattern used by the monolithic
// reconciler. Queue workers dequeue string keys and dispatch to the appropriate
// method, which is responsible for fetching the current state from listers/informers.
type Reconciler interface {
	// ReconcileMOSC reconciles a MachineOSConfig identified by key.
	ReconcileMOSC(ctx context.Context, key string) error

	// ReconcileMOSB reconciles a MachineOSBuild identified by key.
	ReconcileMOSB(ctx context.Context, key string) error

	// ReconcilePool reconciles a MachineConfigPool identified by key.
	ReconcilePool(ctx context.Context, key string) error

	// ReconcileJob reconciles a Job identified by key (namespace/name).
	ReconcileJob(ctx context.Context, key string) error
}
