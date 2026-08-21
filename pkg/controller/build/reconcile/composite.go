package reconcile

import "context"

// Compile-time interface check.
var _ Reconciler = &compositeReconciler{}

// compositeReconciler delegates each Reconciler method to the appropriate
// sub-reconciler. It is the single Reconciler implementation used by the
// build controller's workqueue dispatch.
type compositeReconciler struct {
	mosc *MOSCReconciler
	mosb *MOSBReconciler
	pool *PoolReconciler
	job  *JobReconciler
}

// NewCompositeReconciler constructs a Reconciler that delegates to the four
// sub-reconcilers built from the shared Deps container.
func NewCompositeReconciler(d Deps) Reconciler {
	return &compositeReconciler{
		mosc: NewMOSCReconciler(d),
		mosb: NewMOSBReconciler(d),
		pool: NewPoolReconciler(d),
		job:  NewJobReconciler(d),
	}
}

func (c *compositeReconciler) ReconcileMOSC(ctx context.Context, key string) error {
	return c.mosc.ReconcileMOSC(ctx, key)
}
func (c *compositeReconciler) ReconcileMOSB(ctx context.Context, key string) error {
	return c.mosb.ReconcileMOSB(ctx, key)
}
func (c *compositeReconciler) ReconcilePool(ctx context.Context, key string) error {
	return c.pool.ReconcilePool(ctx, key)
}
func (c *compositeReconciler) ReconcileJob(ctx context.Context, key string) error {
	return c.job.ReconcileJob(ctx, key)
}
