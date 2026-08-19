package v2

import (
	"context"
	"fmt"

	mcfgv1 "github.com/openshift/api/machineconfiguration/v1"
	mcfgclientset "github.com/openshift/client-go/machineconfiguration/clientset/versioned"
	"github.com/openshift/machine-config-operator/pkg/apihelpers"
	"github.com/openshift/machine-config-operator/pkg/controller/build/buildrequest"
	"github.com/openshift/machine-config-operator/pkg/controller/build/constants"
	"github.com/openshift/machine-config-operator/pkg/controller/build/events"
	"github.com/openshift/machine-config-operator/pkg/controller/build/imagebuilder"
	"github.com/openshift/machine-config-operator/pkg/controller/build/utils"
	k8serrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	clientset "k8s.io/client-go/kubernetes"
	"k8s.io/client-go/util/retry"
	"k8s.io/klog/v2"
)

// moscReconciler implements the level-triggered MachineOSConfig reconciler.
type moscReconciler struct {
	mcfgclient mcfgclientset.Interface
	kubeclient clientset.Interface
	*listers
	statusMgr     *MCPStatusManager
	seeder        *SeedManager
	eventRecorder *events.OCLEventRecorder
}

// newMOSCReconciler creates a new MOSC reconciler.
func newMOSCReconciler(
	mcfgclient mcfgclientset.Interface,
	kubeclient clientset.Interface,
	l *listers,
	statusMgr *MCPStatusManager,
	seeder *SeedManager,
	eventRecorder *events.OCLEventRecorder,
) *moscReconciler {
	return &moscReconciler{
		mcfgclient:    mcfgclient,
		kubeclient:    kubeclient,
		listers:       l,
		statusMgr:     statusMgr,
		seeder:        seeder,
		eventRecorder: eventRecorder,
	}
}

// Sync is the entry point for MachineOSConfig reconciliation.
func (r *moscReconciler) Sync(ctx context.Context, moscName string) error {
	mosc, err := r.machineOSConfigLister.Get(moscName)
	if k8serrors.IsNotFound(err) {
		return r.handleDeletion(ctx, moscName)
	}
	if err != nil {
		return fmt.Errorf("could not get MachineOSConfig %q: %w", moscName, err)
	}

	// --- Seeding lifecycle ---
	if r.seeder.NeedsCleanup(mosc) {
		return r.seeder.Cleanup(ctx, mosc)
	}

	if handled, seedErr := r.seeder.SeedIfNeeded(ctx, mosc); handled {
		return seedErr
	}

	// --- Rebuild annotation ---
	if hasRebuildAnnotation(mosc) {
		return r.handleRebuild(ctx, mosc)
	}

	// --- Validate build config ---
	if err := r.validateMOSC(mosc); err != nil {
		return err
	}

	// --- Determine expected MOSB ---
	mcp, err := r.machineConfigPoolLister.Get(mosc.Spec.MachineConfigPool.Name)
	if err != nil {
		return fmt.Errorf("could not get MachineConfigPool %q: %w", mosc.Spec.MachineConfigPool.Name, err)
	}

	mc, err := r.machineConfigLister.Get(mcp.Spec.Configuration.Name)
	if err != nil {
		return fmt.Errorf("could not get MachineConfig %q: %w", mcp.Spec.Configuration.Name, err)
	}

	expectedMOSB, err := buildrequest.NewMachineOSBuild(buildrequest.MachineOSBuildOpts{
		MachineConfig:     mc,
		MachineConfigPool: mcp,
		MachineOSConfig:   mosc,
	})
	if err != nil {
		return fmt.Errorf("could not generate expected MachineOSBuild for MachineOSConfig %q: %w", moscName, err)
	}

	// --- Check if expected MOSB exists ---
	existingMOSB, err := r.machineOSBuildLister.Get(expectedMOSB.Name)
	if k8serrors.IsNotFound(err) {
		return r.createNewMOSB(ctx, mosc, mcp, expectedMOSB)
	}
	if err != nil {
		return fmt.Errorf("could not get MachineOSBuild %q: %w", expectedMOSB.Name, err)
	}

	// --- Handle existing MOSB based on its state ---
	return r.handleExistingMOSB(ctx, mosc, mcp, existingMOSB)
}

// handleDeletion processes a deleted MachineOSConfig by removing associated MOSBs.
func (r *moscReconciler) handleDeletion(ctx context.Context, moscName string) error {
	klog.Infof("MachineOSConfig %q not found, cleaning up associated MachineOSBuilds", moscName)

	sel := labels.SelectorFromSet(labels.Set{
		constants.MachineOSConfigNameLabelKey: moscName,
	})
	mosbList, err := r.machineOSBuildLister.List(sel)
	if err != nil {
		return fmt.Errorf("could not list MachineOSBuilds for deleted MachineOSConfig %q: %w", moscName, err)
	}

	for _, mosb := range mosbList {
		klog.Infof("Deleting MachineOSBuild %q for deleted MachineOSConfig %q", mosb.Name, moscName)

		cleaner := imagebuilder.NewJobImageBuildCleaner(r.kubeclient, r.mcfgclient, mosb)
		if cleanErr := cleaner.Clean(ctx); cleanErr != nil {
			klog.Warningf("Could not clean builder for MachineOSBuild %q: %v", mosb.Name, cleanErr)
		}

		if delErr := r.mcfgclient.MachineconfigurationV1().MachineOSBuilds().Delete(ctx, mosb.Name, metav1.DeleteOptions{}); delErr != nil && !k8serrors.IsNotFound(delErr) {
			return fmt.Errorf("could not delete MachineOSBuild %q: %w", mosb.Name, delErr)
		}
	}

	return nil
}

// handleRebuild processes the rebuild annotation by deleting the current MOSB
// and creating a new one.
func (r *moscReconciler) handleRebuild(ctx context.Context, mosc *mcfgv1.MachineOSConfig) error {
	klog.Infof("MachineOSConfig %q has rebuild annotation", mosc.Name)
	r.eventRecorder.RecordRebuildRequested(mosc, "rebuild annotation applied")

	if !hasCurrentBuildAnnotation(mosc) {
		klog.Infof("MachineOSConfig %q has no current build annotation, skipping rebuild", mosc.Name)
		return nil
	}

	mosbName := mosc.Annotations[constants.CurrentMachineOSBuildAnnotationKey]

	// Delete the current MOSB.
	mosb, err := r.machineOSBuildLister.Get(mosbName)
	if err != nil && !k8serrors.IsNotFound(err) {
		return fmt.Errorf("cannot rebuild MachineOSConfig %q: %w", mosc.Name, err)
	}

	if mosb != nil {
		cleaner := imagebuilder.NewJobImageBuildCleaner(r.kubeclient, r.mcfgclient, mosb)
		if cleanErr := cleaner.Clean(ctx); cleanErr != nil {
			klog.Warningf("Could not clean builder for MachineOSBuild %q: %v", mosb.Name, cleanErr)
		}
		if delErr := r.mcfgclient.MachineconfigurationV1().MachineOSBuilds().Delete(ctx, mosbName, metav1.DeleteOptions{}); delErr != nil && !k8serrors.IsNotFound(delErr) {
			return fmt.Errorf("could not delete MachineOSBuild %q for rebuild: %w", mosbName, delErr)
		}
	}

	// Remove the rebuild annotation.
	if err := r.removeAnnotation(ctx, mosc, constants.RebuildMachineOSConfigAnnotationKey); err != nil {
		return fmt.Errorf("could not remove rebuild annotation from MachineOSConfig %q: %w", mosc.Name, err)
	}

	// Clear the current build annotation so a new MOSB will be created on the next sync.
	if err := r.removeAnnotation(ctx, mosc, constants.CurrentMachineOSBuildAnnotationKey); err != nil {
		return fmt.Errorf("could not clear current build annotation from MachineOSConfig %q: %w", mosc.Name, err)
	}

	klog.Infof("MachineOSConfig %q rebuild: deleted current MOSB, next sync will create new one", mosc.Name)
	return nil
}

// validateMOSC validates the MachineOSConfig's build inputs.
// This delegates to the same validation logic used by the top-level package
// but uses our own listers to avoid a circular import.
func (r *moscReconciler) validateMOSC(mosc *mcfgv1.MachineOSConfig) error {
	// Verify the referenced MCP exists.
	_, err := r.machineConfigPoolLister.Get(mosc.Spec.MachineConfigPool.Name)
	if err != nil {
		return fmt.Errorf("MachineConfigPool %q referenced by MachineOSConfig %q not found: %w",
			mosc.Spec.MachineConfigPool.Name, mosc.Name, err)
	}
	return nil
}

// createNewMOSB creates a new MachineOSBuild for the MachineOSConfig.
func (r *moscReconciler) createNewMOSB(ctx context.Context, mosc *mcfgv1.MachineOSConfig, mcp *mcfgv1.MachineConfigPool, expectedMOSB *mcfgv1.MachineOSBuild) error {
	// Check if MCP is degraded (non-build).
	if r.statusMgr.ShouldPreventBuild(mcp) {
		return fmt.Errorf("MachineConfigPool %q is degraded, cannot create new MachineOSBuild", mcp.Name)
	}

	klog.Infof("Creating MachineOSBuild %q for MachineOSConfig %q", expectedMOSB.Name, mosc.Name)

	created, err := r.mcfgclient.MachineconfigurationV1().MachineOSBuilds().Create(ctx, expectedMOSB, metav1.CreateOptions{})
	if err != nil {
		if k8serrors.IsAlreadyExists(err) {
			klog.V(4).Infof("MachineOSBuild %q already exists (race), skipping creation", expectedMOSB.Name)
			return nil
		}
		return fmt.Errorf("could not create MachineOSBuild %q: %w", expectedMOSB.Name, err)
	}

	// Set the current build annotation on the MOSC.
	if err := r.setCurrentBuildAnnotation(ctx, mosc, created.Name); err != nil {
		klog.Warningf("Could not set current build annotation on MachineOSConfig %q: %v", mosc.Name, err)
	}

	r.eventRecorder.RecordConfigReconciled(mosc)
	return nil
}

// handleExistingMOSB handles an existing MOSB based on its current state.
func (r *moscReconciler) handleExistingMOSB(ctx context.Context, mosc *mcfgv1.MachineOSConfig, _ *mcfgv1.MachineConfigPool, mosb *mcfgv1.MachineOSBuild) error {
	// Succeeded: ensure MOSC status is up to date.
	if apihelpers.IsMachineOSBuildConditionTrue(mosb.Status.Conditions, mcfgv1.MachineOSBuildSucceeded) {
		if err := r.ensureMOSCStatusFromMOSB(ctx, mosc, mosb); err != nil {
			return err
		}
		// Clean up non-current MOSBs.
		return r.deleteNonCurrentMOSBs(ctx, mosc, mosb)
	}

	// Transient: ensure the annotation points to this build.
	if isMOSBInTransientState(mosb) {
		return r.setCurrentBuildAnnotation(ctx, mosc, mosb.Name)
	}

	// Failed/Interrupted/Initial: no action from MOSC side.
	return nil
}

// ensureMOSCStatusFromMOSB updates the MOSC status with the build's pullspec.
func (r *moscReconciler) ensureMOSCStatusFromMOSB(ctx context.Context, mosc *mcfgv1.MachineOSConfig, mosb *mcfgv1.MachineOSBuild) error {
	if mosc.Status.CurrentImagePullSpec == mosb.Status.DigestedImagePushSpec &&
		isCurrentBuildAnnotationEqual(mosc, mosb) {
		return nil // already up to date
	}

	return retry.RetryOnConflict(retry.DefaultRetry, func() error {
		current, err := r.mcfgclient.MachineconfigurationV1().MachineOSConfigs().Get(ctx, mosc.Name, metav1.GetOptions{})
		if err != nil {
			return err
		}

		current.Status.CurrentImagePullSpec = mosb.Status.DigestedImagePushSpec
		current.Status.ObservedGeneration = current.GetGeneration()
		current.Status.MachineOSBuild = &mcfgv1.ObjectReference{
			Name:     mosb.Name,
			Group:    mcfgv1.SchemeGroupVersion.Group,
			Resource: "machineosbuilds",
		}

		if _, err := r.mcfgclient.MachineconfigurationV1().MachineOSConfigs().UpdateStatus(ctx, current, metav1.UpdateOptions{}); err != nil {
			return err
		}

		// Also set the annotation (separate update on the metadata path).
		current2, err := r.mcfgclient.MachineconfigurationV1().MachineOSConfigs().Get(ctx, mosc.Name, metav1.GetOptions{})
		if err != nil {
			return err
		}
		metav1.SetMetaDataAnnotation(&current2.ObjectMeta, constants.CurrentMachineOSBuildAnnotationKey, mosb.Name)
		_, err = r.mcfgclient.MachineconfigurationV1().MachineOSConfigs().Update(ctx, current2, metav1.UpdateOptions{})
		return err
	})
}

// deleteNonCurrentMOSBs removes MOSBs that are not the current successful one.
func (r *moscReconciler) deleteNonCurrentMOSBs(ctx context.Context, mosc *mcfgv1.MachineOSConfig, currentMOSB *mcfgv1.MachineOSBuild) error {
	allMOSBs, err := utils.GetMachineOSBuildsForMachineOSConfig(mosc, r.utilListers())
	if err != nil {
		return err
	}

	for _, mosb := range allMOSBs {
		if mosb.Name == currentMOSB.Name {
			continue
		}
		if apihelpers.IsMachineOSBuildConditionTrue(mosb.Status.Conditions, mcfgv1.MachineOSBuildSucceeded) {
			continue // keep other successful builds
		}

		klog.V(4).Infof("Deleting non-current MachineOSBuild %q for MachineOSConfig %q", mosb.Name, mosc.Name)
		if err := r.mcfgclient.MachineconfigurationV1().MachineOSBuilds().Delete(ctx, mosb.Name, metav1.DeleteOptions{}); err != nil && !k8serrors.IsNotFound(err) {
			klog.Warningf("Could not delete non-current MachineOSBuild %q: %v", mosb.Name, err)
		}
	}

	return nil
}

// setCurrentBuildAnnotation sets the current build annotation on the MOSC.
func (r *moscReconciler) setCurrentBuildAnnotation(ctx context.Context, mosc *mcfgv1.MachineOSConfig, mosbName string) error {
	if hasCurrentBuildAnnotation(mosc) &&
		mosc.Annotations[constants.CurrentMachineOSBuildAnnotationKey] == mosbName {
		return nil
	}

	return retry.RetryOnConflict(retry.DefaultRetry, func() error {
		current, err := r.mcfgclient.MachineconfigurationV1().MachineOSConfigs().Get(ctx, mosc.Name, metav1.GetOptions{})
		if err != nil {
			return err
		}

		metav1.SetMetaDataAnnotation(&current.ObjectMeta, constants.CurrentMachineOSBuildAnnotationKey, mosbName)
		_, err = r.mcfgclient.MachineconfigurationV1().MachineOSConfigs().Update(ctx, current, metav1.UpdateOptions{})
		return err
	})
}

// removeAnnotation removes a single annotation from the MOSC.
func (r *moscReconciler) removeAnnotation(ctx context.Context, mosc *mcfgv1.MachineOSConfig, key string) error {
	return retry.RetryOnConflict(retry.DefaultRetry, func() error {
		current, err := r.mcfgclient.MachineconfigurationV1().MachineOSConfigs().Get(ctx, mosc.Name, metav1.GetOptions{})
		if err != nil {
			return err
		}

		if current.Annotations == nil {
			return nil
		}
		if _, ok := current.Annotations[key]; !ok {
			return nil
		}

		delete(current.Annotations, key)
		_, err = r.mcfgclient.MachineconfigurationV1().MachineOSConfigs().Update(ctx, current, metav1.UpdateOptions{})
		return err
	})
}
