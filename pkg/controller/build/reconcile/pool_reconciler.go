package reconcile

import (
	"context"
	"fmt"

	mcfgv1 "github.com/openshift/api/machineconfiguration/v1"
	mcfgclientset "github.com/openshift/client-go/machineconfiguration/clientset/versioned"
	mcfglistersv1 "github.com/openshift/client-go/machineconfiguration/listers/machineconfiguration/v1"
	"github.com/openshift/machine-config-operator/pkg/controller/build/buildrequest"
	"github.com/openshift/machine-config-operator/pkg/controller/build/services"
	"github.com/openshift/machine-config-operator/pkg/controller/build/utils"
	k8serrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/klog/v2"
)

// PoolReconciler handles level-based reconciliation of MachineConfigPool objects.
type PoolReconciler struct {
	mcfgclient mcfgclientset.Interface

	mcpLister  mcfglistersv1.MachineConfigPoolLister
	moscLister mcfglistersv1.MachineOSConfigLister
	mosbLister mcfglistersv1.MachineOSBuildLister
	mcLister   mcfglistersv1.MachineConfigLister

	events   services.EventRecorder
	metrics  services.MetricsRecorder
	degraded services.DegradedHandler

	utilListers *utils.Listers
}

// NewPoolReconciler constructs a PoolReconciler with injected dependencies.
func NewPoolReconciler(
	mcfgclient mcfgclientset.Interface,
	mcpLister mcfglistersv1.MachineConfigPoolLister,
	moscLister mcfglistersv1.MachineOSConfigLister,
	mosbLister mcfglistersv1.MachineOSBuildLister,
	mcLister mcfglistersv1.MachineConfigLister,
	events services.EventRecorder,
	metrics services.MetricsRecorder,
	degraded services.DegradedHandler,
	utilListers *utils.Listers,
) *PoolReconciler {
	return &PoolReconciler{
		mcfgclient:  mcfgclient,
		mcpLister:   mcpLister,
		moscLister:  moscLister,
		mosbLister:  mosbLister,
		mcLister:    mcLister,
		events:      events,
		metrics:     metrics,
		degraded:    degraded,
		utilListers: utilListers,
	}
}

// ReconcilePool is the level-based reconciliation entry point for a
// MachineConfigPool identified by key.
func (r *PoolReconciler) ReconcilePool(ctx context.Context, key string) error {
	mcp, err := r.mcpLister.Get(key)
	if k8serrors.IsNotFound(err) {
		klog.V(4).Infof("PoolReconciler: MachineConfigPool %q deleted, nothing to do", key)
		return nil
	}
	if err != nil {
		return fmt.Errorf("could not get MachineConfigPool %q: %w", key, err)
	}

	mcp = mcp.DeepCopy()

	// Find the associated MachineOSConfig for this pool.
	mosc, err := utils.GetMachineOSConfigForMachineConfigPool(mcp, r.utilListers)
	if k8serrors.IsNotFound(err) {
		klog.V(4).Infof("PoolReconciler: no MachineOSConfig for pool %q, nothing to do", key)
		return nil
	}
	if err != nil {
		return fmt.Errorf("could not find MOSC for pool %q: %w", key, err)
	}

	// Update rollout counts.
	r.metrics.UpdateOCLRolloutCounts(mcp.Name, mcp.Status.UpdatedMachineCount, mcp.Status.MachineCount)

	// Update degraded condition from current build status.
	if err := r.degraded.UpdateImageBuildDegraded(ctx, mcp, mosc); err != nil {
		klog.Warningf("PoolReconciler: could not update degraded condition for %q: %v", key, err)
	}

	// Ensure a MachineOSBuild exists for the current desired rendered config.
	return r.ensureBuildForPool(ctx, mcp, mosc)
}

// ensureBuildForPool creates a new MachineOSBuild if the pool's desired
// rendered config doesn't have one yet.
func (r *PoolReconciler) ensureBuildForPool(ctx context.Context, mcp *mcfgv1.MachineConfigPool, mosc *mcfgv1.MachineOSConfig) error {
	mc, err := r.mcLister.Get(mcp.Spec.Configuration.Name)
	if err != nil {
		return fmt.Errorf("could not get MC %q: %w", mcp.Spec.Configuration.Name, err)
	}

	desiredMOSB, err := buildrequest.NewMachineOSBuild(buildrequest.MachineOSBuildOpts{
		MachineConfig:     mc,
		MachineOSConfig:   mosc,
		MachineConfigPool: mcp,
	})
	if err != nil {
		return fmt.Errorf("could not compute desired MOSB name: %w", err)
	}

	// Check if it already exists.
	_, err = r.mosbLister.Get(desiredMOSB.Name)
	if err == nil {
		// MOSB exists — nothing to do.
		return nil
	}
	if !k8serrors.IsNotFound(err) {
		return fmt.Errorf("could not check for MOSB %q: %w", desiredMOSB.Name, err)
	}

	// Create the MOSB.
	oref := metav1.NewControllerRef(mosc, mcfgv1.SchemeGroupVersion.WithKind("MachineOSConfig"))
	desiredMOSB.SetOwnerReferences([]metav1.OwnerReference{*oref})

	_, err = r.mcfgclient.MachineconfigurationV1().MachineOSBuilds().Create(ctx, desiredMOSB, metav1.CreateOptions{})
	if err != nil {
		if k8serrors.IsAlreadyExists(err) {
			return nil
		}
		return fmt.Errorf("could not create MOSB for pool %q: %w", mcp.Name, err)
	}

	klog.Infof("PoolReconciler: created MachineOSBuild %q for pool %q", desiredMOSB.Name, mcp.Name)
	r.events.RecordPoolConfigChanged(mcp, "", mcp.Spec.Configuration.Name)
	r.metrics.RecordConfigChange(mcp.Name)
	return nil
}
