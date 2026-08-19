package v2

import (
	"context"
	"fmt"

	mcfgv1 "github.com/openshift/api/machineconfiguration/v1"
	mcfgclientset "github.com/openshift/client-go/machineconfiguration/clientset/versioned"
	"github.com/openshift/machine-config-operator/pkg/controller/build/events"
	"github.com/openshift/machine-config-operator/pkg/controller/build/metrics"
	"github.com/openshift/machine-config-operator/pkg/controller/build/utils"
	ctrlcommon "github.com/openshift/machine-config-operator/pkg/controller/common"
	k8serrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/labels"
	clientset "k8s.io/client-go/kubernetes"
	"k8s.io/client-go/util/workqueue"
	"k8s.io/klog/v2"
)

// mcpReconciler detects rendered config changes on MachineConfigPools and
// delegates build creation to the MOSC reconciler via queue enqueue.
type mcpReconciler struct {
	mcfgclient    mcfgclientset.Interface
	kubeclient    clientset.Interface
	*listers
	statusMgr     *MCPStatusManager
	eventRecorder *events.OCLEventRecorder
	moscQueue     workqueue.TypedRateLimitingInterface[string]
}

// newMCPReconciler creates a new MCP reconciler.
func newMCPReconciler(
	mcfgclient mcfgclientset.Interface,
	kubeclient clientset.Interface,
	l *listers,
	statusMgr *MCPStatusManager,
	eventRecorder *events.OCLEventRecorder,
	moscQueue workqueue.TypedRateLimitingInterface[string],
) *mcpReconciler {
	return &mcpReconciler{
		mcfgclient:    mcfgclient,
		kubeclient:    kubeclient,
		listers:       l,
		statusMgr:     statusMgr,
		eventRecorder: eventRecorder,
		moscQueue:     moscQueue,
	}
}

// Sync is the entry point for MachineConfigPool reconciliation. It detects
// rendered config changes and delegates build creation to the MOSC queue.
func (r *mcpReconciler) Sync(ctx context.Context, mcpName string) error {
	mcp, err := r.machineConfigPoolLister.Get(mcpName)
	if k8serrors.IsNotFound(err) {
		klog.V(4).Infof("MachineConfigPool %q not found, skipping", mcpName)
		return nil
	}
	if err != nil {
		return fmt.Errorf("could not get MachineConfigPool %q: %w", mcpName, err)
	}

	// Find MOSC for this pool.
	mosc, err := utils.GetMachineOSConfigForMachineConfigPool(mcp, r.utilListers())
	if err != nil {
		if k8serrors.IsNotFound(err) {
			klog.V(4).Infof("No MachineOSConfig for pool %q, skipping", mcpName)
			return nil
		}
		return fmt.Errorf("could not get MachineOSConfig for pool %q: %w", mcpName, err)
	}

	// Skip if pre-built image seeding is pending.
	if isPreBuiltImageAwaitingSeeding(mosc) {
		klog.V(4).Infof("MachineOSConfig %q awaiting pre-built image seeding, skipping MCP sync", mosc.Name)
		return nil
	}

	oldRendered := mcp.Status.Configuration.Name
	newRendered := mcp.Spec.Configuration.Name

	// Install-time: one or both config names are empty.
	if oldRendered == "" || newRendered == "" {
		klog.V(4).Infof("MachineConfigPool %q has empty config names (old=%q, new=%q), skipping", mcpName, oldRendered, newRendered)
		return nil
	}

	// No config change.
	if oldRendered == newRendered {
		if hasCurrentBuildAnnotation(mosc) {
			// Steady state: update metrics only.
			r.updateMetrics(mcp)
			return nil
		}
		// MOSC exists but has no current build — needs initial build.
		klog.Infof("MachineOSConfig %q for pool %q has no current build, enqueuing", mosc.Name, mcpName)
		r.moscQueue.Add(mosc.Name)
		r.updateMetrics(mcp)
		return nil
	}

	// Config changed.
	klog.Infof("Rendered config for pool %q changed from %q to %q", mcpName, oldRendered, newRendered)
	r.eventRecorder.RecordPoolConfigChanged(mcp, oldRendered, newRendered)
	metrics.RecordConfigChange(mcpName)

	// Determine if a full rebuild is needed or just layer reuse.
	needsRebuild, err := r.requiresRebuild(oldRendered, newRendered)
	if err != nil {
		klog.Warningf("Could not determine if rebuild is needed for pool %q: %v, falling back to full rebuild", mcpName, err)
		needsRebuild = true
	}

	if needsRebuild || !hasCurrentBuildAnnotation(mosc) {
		// Full rebuild needed — enqueue the MOSC.
		klog.Infof("Pool %q requires rebuild, enqueuing MachineOSConfig %q", mcpName, mosc.Name)
		r.moscQueue.Add(mosc.Name)
	} else {
		// Layer-only change — enqueue the MOSC to handle the reuse logic.
		klog.Infof("Pool %q has layer-only change, enqueuing MachineOSConfig %q", mcpName, mosc.Name)
		r.moscQueue.Add(mosc.Name)
	}

	r.updateMetrics(mcp)
	return nil
}

// requiresRebuild determines if the MC change requires a full image rebuild
// (kernel args, extensions, osImageURL changed) vs. a layer-only change.
func (r *mcpReconciler) requiresRebuild(oldMCName, newMCName string) (bool, error) {
	oldMC, err := r.machineConfigLister.Get(oldMCName)
	if err != nil {
		return false, fmt.Errorf("could not get old MachineConfig %q: %w", oldMCName, err)
	}

	newMC, err := r.machineConfigLister.Get(newMCName)
	if err != nil {
		return false, fmt.Errorf("could not get new MachineConfig %q: %w", newMCName, err)
	}

	return ctrlcommon.RequiresRebuild(oldMC, newMC), nil
}

// updateMetrics updates the rollout and MOSC count metrics for a pool.
func (r *mcpReconciler) updateMetrics(mcp *mcfgv1.MachineConfigPool) {
	metrics.UpdateOCLRolloutCounts(mcp.Name, mcp.Status.UpdatedMachineCount, mcp.Status.MachineCount)

	// Update the MOSC count metric.
	moscList, err := r.machineOSConfigLister.List(labels.Everything())
	if err == nil {
		metrics.SetMOSCCount(len(moscList))
	}
}

// SetMCPBuildability checks the MOSC count for the MCP and sets the MCP's
// build readiness accordingly. Exactly 1 MOSC is required for a pool to be
// buildable.
func (r *mcpReconciler) SetMCPBuildability(ctx context.Context, mcp *mcfgv1.MachineConfigPool) error {
	moscCount := r.getMOSCCountForPool(mcp.Name)

	switch {
	case moscCount == 1:
		return r.statusMgr.MarkMCPBuildable(ctx, mcp,
			"MOSCReady",
			fmt.Sprintf("MachineConfigPool %q has exactly 1 MachineOSConfig, ready for builds", mcp.Name),
		)
	case moscCount == 0:
		return r.statusMgr.MarkMCPNotBuildable(ctx, mcp,
			"NoMOSC",
			fmt.Sprintf("MachineConfigPool %q has no MachineOSConfig, cannot build", mcp.Name),
		)
	default:
		return r.statusMgr.MarkMCPNotBuildable(ctx, mcp,
			"MultipleMOSC",
			fmt.Sprintf("MachineConfigPool %q has %d MachineOSConfigs, expected exactly 1", mcp.Name, moscCount),
		)
	}
}

// getMOSCCountForPool returns the number of MachineOSConfigs targeting the
// given pool name.
func (r *mcpReconciler) getMOSCCountForPool(poolName string) int {
	moscList, err := r.machineOSConfigLister.List(labels.Everything())
	if err != nil {
		klog.Warningf("Could not list MachineOSConfigs: %v", err)
		return 0
	}

	count := 0
	for _, mosc := range moscList {
		if mosc.Spec.MachineConfigPool.Name == poolName {
			count++
		}
	}
	return count
}

// getMOSCForPool returns the MachineOSConfig targeting the given pool, if any.
// This is equivalent to the label-based lookup in utils but uses spec matching.
func getMOSCForPool(moscList []*mcfgv1.MachineOSConfig, poolName string) *mcfgv1.MachineOSConfig {
	for _, mosc := range moscList {
		if mosc.Spec.MachineConfigPool.Name == poolName {
			return mosc
		}
	}
	return nil
}

// enqueueMOSCForPool finds and enqueues the MOSC for the given pool name.
func (r *mcpReconciler) enqueueMOSCForPool(poolName string) {
	moscList, err := r.machineOSConfigLister.List(labels.Everything())
	if err != nil {
		klog.Warningf("Could not list MachineOSConfigs to find one for pool %q: %v", poolName, err)
		return
	}

	mosc := getMOSCForPool(moscList, poolName)
	if mosc != nil {
		r.moscQueue.Add(mosc.Name)
	}
}

// MarkMCPBuildable is a convenience that sets ImageBuildDegraded=False.
func (r *mcpReconciler) MarkMCPBuildable(ctx context.Context, mcp *mcfgv1.MachineConfigPool, reason, message string) error {
	return r.statusMgr.MarkMCPBuildable(ctx, mcp, reason, message)
}

// MarkMCPNotBuildable is a convenience that sets ImageBuildDegraded=True.
func (r *mcpReconciler) MarkMCPNotBuildable(ctx context.Context, mcp *mcfgv1.MachineConfigPool, reason, message string) error {
	return r.statusMgr.MarkMCPNotBuildable(ctx, mcp, reason, message)
}
