package v2

import (
	"context"
	"errors"
	"fmt"

	mcfgv1 "github.com/openshift/api/machineconfiguration/v1"
	mcfgclientset "github.com/openshift/client-go/machineconfiguration/clientset/versioned"
	"github.com/openshift/machine-config-operator/pkg/apihelpers"
	"github.com/openshift/machine-config-operator/pkg/controller/build/buildrequest"
	"github.com/openshift/machine-config-operator/pkg/controller/build/constants"
	"github.com/openshift/machine-config-operator/pkg/controller/build/events"
	"github.com/openshift/machine-config-operator/pkg/controller/build/imagebuilder"
	"github.com/openshift/machine-config-operator/pkg/controller/build/metrics"
	"github.com/openshift/machine-config-operator/pkg/controller/build/utils"
	k8serrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	clientset "k8s.io/client-go/kubernetes"
	"k8s.io/client-go/util/retry"
	"k8s.io/klog/v2"
)

// mosbReconciler implements the level-triggered MOSB state machine.
type mosbReconciler struct {
	mcfgclient mcfgclientset.Interface
	kubeclient clientset.Interface
	*listers
	statusMgr     *MCPStatusManager
	eventRecorder *events.OCLEventRecorder
}

// newMOSBReconciler creates a new MOSB reconciler.
func newMOSBReconciler(
	mcfgclient mcfgclientset.Interface,
	kubeclient clientset.Interface,
	l *listers,
	statusMgr *MCPStatusManager,
	eventRecorder *events.OCLEventRecorder,
) *mosbReconciler {
	return &mosbReconciler{
		mcfgclient:    mcfgclient,
		kubeclient:    kubeclient,
		listers:       l,
		statusMgr:     statusMgr,
		eventRecorder: eventRecorder,
	}
}

// Sync is the core state machine entry point. It reads the current state of
// a MachineOSBuild and drives it toward the desired state.
func (r *mosbReconciler) Sync(ctx context.Context, mosbName string) error {
	mosb, err := r.machineOSBuildLister.Get(mosbName)
	if k8serrors.IsNotFound(err) {
		klog.V(4).Infof("MachineOSBuild %q not found, skipping", mosbName)
		return nil
	}
	if err != nil {
		return fmt.Errorf("could not get MachineOSBuild %q: %w", mosbName, err)
	}

	mosc, err := utils.GetMachineOSConfigForMachineOSBuild(mosb, r.utilListers())
	if k8serrors.IsNotFound(err) {
		klog.V(4).Infof("MachineOSConfig for MachineOSBuild %q not found, skipping", mosbName)
		return nil
	}
	if err != nil {
		return fmt.Errorf("could not get MachineOSConfig for MachineOSBuild %q: %w", mosbName, err)
	}

	mcp, err := r.machineConfigPoolLister.Get(mosc.Spec.MachineConfigPool.Name)
	if err != nil {
		return fmt.Errorf("could not get MachineConfigPool %q: %w", mosc.Spec.MachineConfigPool.Name, err)
	}

	// --- TERMINAL STATES: side-effects only ---

	if apihelpers.IsMachineOSBuildConditionTrue(mosb.Status.Conditions, mcfgv1.MachineOSBuildSucceeded) {
		return r.handleSucceeded(ctx, mosb, mosc, mcp)
	}

	if apihelpers.IsMachineOSBuildConditionTrue(mosb.Status.Conditions, mcfgv1.MachineOSBuildFailed) {
		return r.handleFailed(ctx, mosb, mcp)
	}

	if apihelpers.IsMachineOSBuildConditionTrue(mosb.Status.Conditions, mcfgv1.MachineOSBuildInterrupted) {
		klog.V(4).Infof("MachineOSBuild %q is interrupted, no action needed", mosbName)
		return nil
	}

	// --- TRANSIENT STATES: sync status from builder ---

	if isMOSBInTransientState(mosb) {
		return r.handleTransient(ctx, mosb, mosc)
	}

	// --- INITIAL STATE: decide what to do ---

	return r.handleInitial(ctx, mosb, mosc, mcp)
}

// handleSucceeded processes a MachineOSBuild that has succeeded.
func (r *mosbReconciler) handleSucceeded(ctx context.Context, mosb *mcfgv1.MachineOSBuild, mosc *mcfgv1.MachineOSConfig, mcp *mcfgv1.MachineConfigPool) error {
	klog.V(4).Infof("MachineOSBuild %q succeeded, running side-effects", mosb.Name)

	// Clean up ephemeral build objects (job, configmaps, secrets).
	cleaner := imagebuilder.NewJobImageBuildCleaner(r.kubeclient, r.mcfgclient, mosb)
	if err := cleaner.Clean(ctx); err != nil {
		klog.Warningf("Could not clean ephemeral objects for MachineOSBuild %q: %v", mosb.Name, err)
	}

	// Update MOSC status with the digested image pullspec.
	if err := r.ensureMOSCStatus(ctx, mosc, mosb); err != nil {
		return fmt.Errorf("could not update MachineOSConfig %q status: %w", mosc.Name, err)
	}

	// Clear MCP degraded condition.
	if err := r.statusMgr.SetBuildSucceeded(ctx, mcp); err != nil {
		klog.Warningf("Could not clear MCP degraded condition for pool %q: %v", mcp.Name, err)
	}

	return nil
}

// handleFailed processes a MachineOSBuild that has failed.
func (r *mosbReconciler) handleFailed(ctx context.Context, mosb *mcfgv1.MachineOSBuild, mcp *mcfgv1.MachineConfigPool) error {
	klog.V(4).Infof("MachineOSBuild %q failed", mosb.Name)

	buildErr := getBuildErrorFromMOSB(mosb)
	// SetBuildFailed returns the original build error.
	return r.statusMgr.SetBuildFailed(ctx, mcp, buildErr, mosb.Name)
}

// handleTransient syncs status from the builder for an in-progress build.
func (r *mosbReconciler) handleTransient(ctx context.Context, mosb *mcfgv1.MachineOSBuild, mosc *mcfgv1.MachineOSConfig) error {
	klog.V(4).Infof("MachineOSBuild %q is in transient state, syncing from builder", mosb.Name)
	return r.syncStatusFromBuilder(ctx, mosb, mosc)
}

// handleInitial decides what to do for a MachineOSBuild in its initial state.
func (r *mosbReconciler) handleInitial(ctx context.Context, mosb *mcfgv1.MachineOSBuild, mosc *mcfgv1.MachineOSConfig, mcp *mcfgv1.MachineConfigPool) error {
	// Skip if this is a pre-built image MOSB (handled by seeding).
	if mosb.Labels != nil && mosb.Labels[constants.PreBuiltImageLabelKey] == constants.TrueValue {
		klog.V(4).Infof("MachineOSBuild %q is a pre-built image build, skipping", mosb.Name)
		return nil
	}

	// Skip if the MOSC is awaiting pre-built image seeding.
	if isPreBuiltImageAwaitingSeeding(mosc) {
		klog.V(4).Infof("MachineOSConfig %q is awaiting pre-built image seeding, skipping MachineOSBuild %q", mosc.Name, mosb.Name)
		return nil
	}

	// Skip if this MOSB targets a stale rendered MachineConfig.
	if mosb.Spec.MachineConfig.Name != mcp.Spec.Configuration.Name {
		klog.V(4).Infof("MachineOSBuild %q targets stale rendered MC %q (current: %q), skipping",
			mosb.Name, mosb.Spec.MachineConfig.Name, mcp.Spec.Configuration.Name)
		return nil
	}

	// Check if MCP has non-build degradation that should prevent new builds.
	if r.statusMgr.ShouldPreventBuild(mcp) {
		klog.Warningf("MachineConfigPool %q is degraded (non-build), preventing build for MachineOSBuild %q", mcp.Name, mosb.Name)
		return fmt.Errorf("MachineConfigPool %q is degraded, cannot start build", mcp.Name)
	}

	// Check if a Job already exists for this build.
	observer := imagebuilder.NewJobImageBuildObserver(r.kubeclient, r.mcfgclient, mosb, mosc)
	exists, err := observer.Exists(ctx)
	if err != nil {
		return fmt.Errorf("could not check if build job exists for MachineOSBuild %q: %w", mosb.Name, err)
	}

	if exists {
		klog.V(4).Infof("Build job already exists for MachineOSBuild %q, syncing status", mosb.Name)
		return r.syncStatusFromBuilder(ctx, mosb, mosc)
	}

	// Start a new build.
	return r.startBuild(ctx, mosb, mosc, mcp)
}

// startBuild creates the build Job and records events/metrics.
func (r *mosbReconciler) startBuild(ctx context.Context, mosb *mcfgv1.MachineOSBuild, mosc *mcfgv1.MachineOSConfig, mcp *mcfgv1.MachineConfigPool) error {
	poolName := mosc.Spec.MachineConfigPool.Name

	// Delete any other non-terminal builds for this MOSC.
	if err := r.deleteOtherBuildsForMOSC(ctx, mosb, mosc); err != nil {
		return fmt.Errorf("could not delete other builds for MachineOSConfig %q: %w", mosc.Name, err)
	}

	// Record events and metrics.
	r.eventRecorder.RecordBuildStarted(mosb, mosc)
	r.eventRecorder.RecordBuildPreparing(mosb, fmt.Sprintf("creating build job for pool %q", poolName))
	metrics.RecordBuildStarted(poolName)

	// Create and start the build Job via the imagebuilder.
	builder := imagebuilder.NewJobImageBuilder(r.kubeclient, r.mcfgclient, mosb, mosc)
	if err := builder.Start(ctx); err != nil {
		// If the containerfile is invalid, mark the build as failed without creating a Job.
		var validationErr *buildrequest.ContainerfileValidationError
		if errors.As(err, &validationErr) {
			klog.Warningf("MachineOSBuild %q has an invalid Containerfile; marking as failed: %v", mosb.Name, validationErr)
			return r.markBuildFailed(ctx, mosb)
		}
		return fmt.Errorf("imagebuilder could not start build for MachineOSBuild %q: %w", mosb.Name, err)
	}

	klog.Infof("Started new build %s for MachineOSBuild %q", utils.GetBuildJobName(mosb), mosb.Name)

	// Update MOSC annotation to point to this build.
	if err := r.ensureMOSCAnnotation(ctx, mosc, mosb); err != nil {
		klog.Warningf("Could not update MachineOSConfig %q annotation: %v", mosc.Name, err)
	}

	// Clear MCP degraded condition (allow retry after previous failure).
	if err := r.statusMgr.SetBuildStarted(ctx, mcp); err != nil {
		klog.Warningf("Could not clear MCP degraded condition for pool %q: %v", mcp.Name, err)
	}

	return nil
}

// syncStatusFromBuilder reads the current status from the build Job and
// updates the MachineOSBuild if needed.
func (r *mosbReconciler) syncStatusFromBuilder(ctx context.Context, mosb *mcfgv1.MachineOSBuild, mosc *mcfgv1.MachineOSConfig) error {
	observer := imagebuilder.NewJobImageBuildObserver(r.kubeclient, r.mcfgclient, mosb, mosc)

	exists, err := observer.Exists(ctx)
	if err != nil {
		return fmt.Errorf("could not check if build job exists for MachineOSBuild %q: %w", mosb.Name, err)
	}

	if !exists {
		klog.Warningf("Build job for MachineOSBuild %q not found, marking as interrupted", mosb.Name)
		return r.markBuildInterrupted(ctx, mosb, "build job was deleted")
	}

	newStatus, err := observer.MachineOSBuildStatus(ctx)
	if err != nil {
		return fmt.Errorf("could not get status from builder for MachineOSBuild %q: %w", mosb.Name, err)
	}

	needed, reason := isMOSBStatusUpdateNeeded(mosb.Status, newStatus)
	if !needed {
		klog.V(4).Infof("No status update needed for MachineOSBuild %q: %s", mosb.Name, reason)
		return nil
	}

	klog.V(4).Infof("Updating MachineOSBuild %q status: %s", mosb.Name, reason)
	return r.updateMOSBStatus(ctx, mosb, newStatus)
}

// updateMOSBStatus applies the given status to the MachineOSBuild using retry-on-conflict.
func (r *mosbReconciler) updateMOSBStatus(ctx context.Context, mosb *mcfgv1.MachineOSBuild, newStatus mcfgv1.MachineOSBuildStatus) error {
	return retry.RetryOnConflict(retry.DefaultRetry, func() error {
		current, err := r.mcfgclient.MachineconfigurationV1().MachineOSBuilds().Get(ctx, mosb.Name, metav1.GetOptions{})
		if err != nil {
			return err
		}

		current.Status = newStatus
		_, err = r.mcfgclient.MachineconfigurationV1().MachineOSBuilds().UpdateStatus(ctx, current, metav1.UpdateOptions{})
		return err
	})
}

// markBuildFailed sets the MachineOSBuild to Failed status.
func (r *mosbReconciler) markBuildFailed(ctx context.Context, mosb *mcfgv1.MachineOSBuild) error {
	failedStatus := mcfgv1.MachineOSBuildStatus{
		Conditions: apihelpers.MachineOSBuildFailedConditions(),
	}
	return r.updateMOSBStatus(ctx, mosb, failedStatus)
}

// markBuildInterrupted sets the MachineOSBuild to Interrupted status.
func (r *mosbReconciler) markBuildInterrupted(ctx context.Context, mosb *mcfgv1.MachineOSBuild, reason string) error {
	conditions := apihelpers.MachineOSBuildInterruptedConditions()
	// Update the reason on the Interrupted condition.
	for i := range conditions {
		if conditions[i].Type == string(mcfgv1.MachineOSBuildInterrupted) {
			conditions[i].Message = reason
		}
	}
	interruptedStatus := mcfgv1.MachineOSBuildStatus{
		Conditions: conditions,
	}
	r.eventRecorder.RecordBuildInterrupted(mosb, reason)
	return r.updateMOSBStatus(ctx, mosb, interruptedStatus)
}

// ensureMOSCStatus updates the MachineOSConfig status with the digested image
// from a successful MachineOSBuild.
func (r *mosbReconciler) ensureMOSCStatus(ctx context.Context, mosc *mcfgv1.MachineOSConfig, mosb *mcfgv1.MachineOSBuild) error {
	if mosc.Status.CurrentImagePullSpec == mosb.Status.DigestedImagePushSpec {
		return nil // already up-to-date
	}

	return retry.RetryOnConflict(retry.DefaultRetry, func() error {
		current, err := r.mcfgclient.MachineconfigurationV1().MachineOSConfigs().Get(ctx, mosc.Name, metav1.GetOptions{})
		if err != nil {
			return err
		}

		current.Status.CurrentImagePullSpec = mosb.Status.DigestedImagePushSpec
		current.Status.ObservedGeneration = current.GetGeneration()

		_, err = r.mcfgclient.MachineconfigurationV1().MachineOSConfigs().UpdateStatus(ctx, current, metav1.UpdateOptions{})
		return err
	})
}

// ensureMOSCAnnotation sets the current build annotation on the MachineOSConfig.
func (r *mosbReconciler) ensureMOSCAnnotation(ctx context.Context, mosc *mcfgv1.MachineOSConfig, mosb *mcfgv1.MachineOSBuild) error {
	if isCurrentBuildAnnotationEqual(mosc, mosb) {
		return nil
	}

	return retry.RetryOnConflict(retry.DefaultRetry, func() error {
		current, err := r.mcfgclient.MachineconfigurationV1().MachineOSConfigs().Get(ctx, mosc.Name, metav1.GetOptions{})
		if err != nil {
			return err
		}

		metav1.SetMetaDataAnnotation(&current.ObjectMeta, constants.CurrentMachineOSBuildAnnotationKey, mosb.Name)

		_, err = r.mcfgclient.MachineconfigurationV1().MachineOSConfigs().Update(ctx, current, metav1.UpdateOptions{})
		return err
	})
}

// deleteOtherBuildsForMOSC deletes all non-terminal MachineOSBuilds for the
// given MachineOSConfig except the current one.
func (r *mosbReconciler) deleteOtherBuildsForMOSC(ctx context.Context, currentMOSB *mcfgv1.MachineOSBuild, mosc *mcfgv1.MachineOSConfig) error {
	mosbs, err := utils.GetMachineOSBuildsForMachineOSConfig(mosc, r.utilListers())
	if err != nil {
		return err
	}

	for _, other := range mosbs {
		if other.Name == currentMOSB.Name {
			continue
		}
		if isMOSBInTerminalState(other) {
			continue
		}

		klog.V(4).Infof("Deleting non-terminal MachineOSBuild %q (superseded by %q)", other.Name, currentMOSB.Name)

		// Clean up the builder first.
		cleaner := imagebuilder.NewJobImageBuildCleaner(r.kubeclient, r.mcfgclient, other)
		if err := cleaner.Clean(ctx); err != nil {
			klog.Warningf("Could not clean builder for superseded MachineOSBuild %q: %v", other.Name, err)
		}

		if err := r.mcfgclient.MachineconfigurationV1().MachineOSBuilds().Delete(ctx, other.Name, metav1.DeleteOptions{}); err != nil && !k8serrors.IsNotFound(err) {
			return fmt.Errorf("could not delete superseded MachineOSBuild %q: %w", other.Name, err)
		}
	}

	return nil
}
