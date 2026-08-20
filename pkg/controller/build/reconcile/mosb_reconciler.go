package reconcile

import (
	"context"
	"errors"
	"fmt"

	mcfgv1 "github.com/openshift/api/machineconfiguration/v1"
	mcfgclientset "github.com/openshift/client-go/machineconfiguration/clientset/versioned"
	mcfglistersv1 "github.com/openshift/client-go/machineconfiguration/listers/machineconfiguration/v1"
	"github.com/openshift/machine-config-operator/pkg/apihelpers"
	"github.com/openshift/machine-config-operator/pkg/controller/build/buildrequest"
	"github.com/openshift/machine-config-operator/pkg/controller/build/constants"
	"github.com/openshift/machine-config-operator/pkg/controller/build/imagebuilder"
	"github.com/openshift/machine-config-operator/pkg/controller/build/services"
	"github.com/openshift/machine-config-operator/pkg/controller/build/utils"
	ctrlcommon "github.com/openshift/machine-config-operator/pkg/controller/common"
	k8serrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	clientset "k8s.io/client-go/kubernetes"
	"k8s.io/klog/v2"
)

// MOSBReconciler handles level-based reconciliation of MachineOSBuild objects.
type MOSBReconciler struct {
	mcfgclient mcfgclientset.Interface
	kubeclient clientset.Interface

	mosbLister mcfglistersv1.MachineOSBuildLister
	moscLister mcfglistersv1.MachineOSConfigLister
	mcpLister  mcfglistersv1.MachineConfigPoolLister
	mcLister   mcfglistersv1.MachineConfigLister

	events   services.EventRecorder
	metrics  services.MetricsRecorder
	degraded services.DegradedHandler

	utilListers *utils.Listers
}

// NewMOSBReconciler constructs a MOSBReconciler with injected dependencies.
func NewMOSBReconciler(
	mcfgclient mcfgclientset.Interface,
	kubeclient clientset.Interface,
	mosbLister mcfglistersv1.MachineOSBuildLister,
	moscLister mcfglistersv1.MachineOSConfigLister,
	mcpLister mcfglistersv1.MachineConfigPoolLister,
	mcLister mcfglistersv1.MachineConfigLister,
	events services.EventRecorder,
	metrics services.MetricsRecorder,
	degraded services.DegradedHandler,
	utilListers *utils.Listers,
) *MOSBReconciler {
	return &MOSBReconciler{
		mcfgclient:  mcfgclient,
		kubeclient:  kubeclient,
		mosbLister:  mosbLister,
		moscLister:  moscLister,
		mcpLister:   mcpLister,
		mcLister:    mcLister,
		events:      events,
		metrics:     metrics,
		degraded:    degraded,
		utilListers: utilListers,
	}
}

// ReconcileMOSB is the level-based reconciliation entry point for a
// MachineOSBuild identified by key.
func (r *MOSBReconciler) ReconcileMOSB(ctx context.Context, key string) error {
	mosb, err := r.mosbLister.Get(key)
	if k8serrors.IsNotFound(err) {
		klog.V(4).Infof("MOSBReconciler: MachineOSBuild %q deleted, nothing to do", key)
		return nil
	}
	if err != nil {
		return fmt.Errorf("could not get MachineOSBuild %q: %w", key, err)
	}

	mosb = mosb.DeepCopy()
	state := ctrlcommon.NewMachineOSBuildState(mosb)

	// Terminal states — nothing to do.
	if state.IsBuildSuccess() || state.IsBuildFailure() || state.IsBuildInterrupted() {
		return r.handleTerminalState(ctx, mosb, *state)
	}

	// Transient states — build in progress, let it run.
	if state.IsInTransientState() {
		return nil
	}

	// Synthetic (pre-built) builds should never start a real build.
	if isPreBuiltMOSB(mosb) {
		klog.V(4).Infof("MOSBReconciler: %q is a synthetic pre-built build, skipping", mosb.Name)
		return nil
	}

	// Initial / no-condition state — start a build if one doesn't already exist.
	if state.IsInInitialState() || !state.HasBuildConditions() {
		return r.ensureBuildStarted(ctx, mosb)
	}

	return nil
}

// handleTerminalState processes a MOSB in a terminal state (success/failure/interrupted).
// It updates metrics and degraded condition as needed.
func (r *MOSBReconciler) handleTerminalState(ctx context.Context, mosb *mcfgv1.MachineOSBuild, state ctrlcommon.MachineOSBuildState) error {
	mosc, err := utils.GetMachineOSConfigForMachineOSBuild(mosb, r.utilListers)
	if err != nil {
		if k8serrors.IsNotFound(err) {
			return nil
		}
		return fmt.Errorf("could not find MOSC for MOSB %q: %w", mosb.Name, err)
	}

	poolName := mosc.Spec.MachineConfigPool.Name
	mcp, err := r.mcpLister.Get(poolName)
	if err != nil {
		return fmt.Errorf("could not get MCP %q: %w", poolName, err)
	}

	switch {
	case state.IsBuildFailure():
		r.events.RecordBuildFailed(mosb)
		r.events.RecordBuildDegraded(mosc)
		return r.degraded.UpdateImageBuildDegraded(ctx, mcp, mosc)

	case state.IsBuildSuccess():
		r.events.RecordBuildCompleted(mosb, string(mosb.Status.DigestedImagePushSpec))
		// Check if we're recovering from degraded.
		if apihelpers.IsMachineConfigPoolConditionTrue(mcp.Status.Conditions, mcfgv1.MachineConfigPoolImageBuildDegraded) {
			r.events.RecordBuildRecovered(mosc)
		}
		// Update the parent MOSC status with the built image pullspec.
		if err := r.updateMOSCImagePullSpec(ctx, mosc, mosb); err != nil {
			return fmt.Errorf("could not update MOSC %q image pullspec: %w", mosc.Name, err)
		}
		// Clean up ephemeral ConfigMaps/Secrets created for the build.
		// Use NewEphemeralCleaner (not NewJobImageBuildCleaner) because the
		// job completed successfully and does not need to be stopped/deleted.
		cleaner := imagebuilder.NewEphemeralCleaner(r.kubeclient, r.mcfgclient, mosb)
		if err := cleaner.Clean(ctx); err != nil {
			klog.Warningf("MOSBReconciler: could not clean ephemeral objects for %q: %v", mosb.Name, err)
		}
		return r.degraded.UpdateImageBuildDegraded(ctx, mcp, mosc)
	}

	return nil
}

// ensureBuildStarted verifies whether a build job exists for this MOSB and
// starts one if not.
func (r *MOSBReconciler) ensureBuildStarted(ctx context.Context, mosb *mcfgv1.MachineOSBuild) error {
	// Verify the MCP still wants this rendered config.
	mcpName := mosb.Labels[constants.TargetMachineConfigPoolLabelKey]
	if mcpName == "" {
		return fmt.Errorf("MachineOSBuild %q missing pool label", mosb.Name)
	}
	mcp, err := r.mcpLister.Get(mcpName)
	if err != nil {
		return fmt.Errorf("could not get MCP %q: %w", mcpName, err)
	}
	if mosb.Labels[constants.RenderedMachineConfigLabelKey] != mcp.Spec.Configuration.Name {
		klog.Infof("MOSBReconciler: %q targets stale config, skipping", mosb.Name)
		return nil
	}

	mosc, err := utils.GetMachineOSConfigForMachineOSBuild(mosb, r.utilListers)
	if err != nil {
		if k8serrors.IsNotFound(err) {
			return nil
		}
		return fmt.Errorf("could not get MOSC for MOSB %q: %w", mosb.Name, err)
	}

	// Check if the builder already exists.
	observer := imagebuilder.NewJobImageBuildObserver(r.kubeclient, r.mcfgclient, mosb, mosc)
	exists, err := observer.Exists(ctx)
	if err != nil {
		return fmt.Errorf("could not check builder for MOSB %q: %w", mosb.Name, err)
	}
	if exists {
		return nil
	}

	return r.startBuild(ctx, mosb, mosc)
}

// startBuild creates a new build job for the given MOSB.
func (r *MOSBReconciler) startBuild(ctx context.Context, mosb *mcfgv1.MachineOSBuild, mosc *mcfgv1.MachineOSConfig) error {
	poolName := mosc.Spec.MachineConfigPool.Name

	r.events.RecordBuildStarted(mosb, mosc)
	r.events.RecordBuildPreparing(mosb, fmt.Sprintf("creating build job for pool %q", poolName))
	r.metrics.RecordBuildStarted(poolName)

	if err := imagebuilder.NewJobImageBuilder(r.kubeclient, r.mcfgclient, mosb, mosc).Start(ctx); err != nil {
		var validationErr *buildrequest.ContainerfileValidationError
		if errors.As(err, &validationErr) {
			klog.Warningf("MOSBReconciler: %q has invalid Containerfile, marking failed: %v", mosb.Name, validationErr)
			return r.markBuildFailed(ctx, mosb)
		}
		return fmt.Errorf("could not start build for MOSB %q: %w", mosb.Name, err)
	}

	klog.Infof("MOSBReconciler: started build for %q", mosb.Name)

	// Update degraded condition now that build has started.
	mcp, err := r.mcpLister.Get(poolName)
	if err != nil {
		klog.Warningf("MOSBReconciler: could not get MCP %q for degraded update: %v", poolName, err)
	} else {
		if err := r.degraded.UpdateImageBuildDegraded(ctx, mcp, mosc); err != nil {
			klog.Warningf("MOSBReconciler: could not update degraded condition: %v", err)
		}
	}

	return nil
}

// markBuildFailed sets the MachineOSBuild to failed status.
func (r *MOSBReconciler) markBuildFailed(ctx context.Context, mosb *mcfgv1.MachineOSBuild) error {
	desiredStatus := mosb.Status.DeepCopy()
	desiredStatus.Conditions = apihelpers.MachineOSBuildFailedConditions()

	// Guard against redundant status writes.
	updateNeeded, reason := IsMachineOSBuildStatusUpdateNeeded(mosb.Status, *desiredStatus)
	logStatusGuardResult(mosb.Name, updateNeeded, reason)
	if !updateNeeded {
		return nil
	}

	mosb.Status = *desiredStatus
	_, err := r.mcfgclient.MachineconfigurationV1().MachineOSBuilds().UpdateStatus(ctx, mosb, metav1.UpdateOptions{})
	return err
}

// updateMOSCImagePullSpec patches the parent MOSC's status with the built
// image pullspec from the successful MOSB. This mirrors what the old
// reconciler did in updateMachineOSConfigStatus().
func (r *MOSBReconciler) updateMOSCImagePullSpec(ctx context.Context, mosc *mcfgv1.MachineOSConfig, mosb *mcfgv1.MachineOSBuild) error {
	if mosb.Status.DigestedImagePushSpec == "" {
		return nil
	}

	// Re-read the MOSC to avoid stale-write conflicts.
	fresh, err := r.moscLister.Get(mosc.Name)
	if err != nil {
		return fmt.Errorf("could not re-read MOSC %q: %w", mosc.Name, err)
	}
	mosc = fresh.DeepCopy()

	// Set the current build annotation on the MOSC metadata.
	metav1.SetMetaDataAnnotation(&mosc.ObjectMeta, constants.CurrentMachineOSBuildAnnotationKey, mosb.Name)
	updated, err := r.mcfgclient.MachineconfigurationV1().MachineOSConfigs().Update(ctx, mosc, metav1.UpdateOptions{})
	if err != nil {
		return fmt.Errorf("could not update MOSC %q annotations: %w", mosc.Name, err)
	}

	// Use the object returned by Update() so that we have the current
	// resourceVersion for the subsequent status write.
	updated.Status.CurrentImagePullSpec = mosb.Status.DigestedImagePushSpec
	updated.Status.MachineOSBuild = &mcfgv1.ObjectReference{
		Name:     mosb.Name,
		Group:    mcfgv1.SchemeGroupVersion.Group,
		Resource: "machineosbuilds",
	}
	if _, err := r.mcfgclient.MachineconfigurationV1().MachineOSConfigs().UpdateStatus(ctx, updated, metav1.UpdateOptions{}); err != nil {
		return fmt.Errorf("could not update MOSC %q status: %w", mosc.Name, err)
	}

	klog.Infof("MOSBReconciler: updated MOSC %q currentImagePullSpec to %q", mosc.Name, mosb.Status.DigestedImagePushSpec)
	return nil
}

// isPreBuiltMOSB checks if the MOSB is a synthetic pre-built-image build.
func isPreBuiltMOSB(mosb *mcfgv1.MachineOSBuild) bool {
	if mosb.Labels == nil {
		return false
	}
	return mosb.Labels[constants.PreBuiltImageLabelKey] == constants.TrueValue
}
