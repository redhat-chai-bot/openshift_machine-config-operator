package reconcile

import (
	"context"
	"fmt"

	mcfgv1 "github.com/openshift/api/machineconfiguration/v1"
	mcfgclientset "github.com/openshift/client-go/machineconfiguration/clientset/versioned"
	mcfglistersv1 "github.com/openshift/client-go/machineconfiguration/listers/machineconfiguration/v1"
	"github.com/openshift/machine-config-operator/pkg/controller/build/buildrequest"
	"github.com/openshift/machine-config-operator/pkg/controller/build/constants"
	"github.com/openshift/machine-config-operator/pkg/controller/build/services"
	"github.com/openshift/machine-config-operator/pkg/controller/build/utils"
	ctrlcommon "github.com/openshift/machine-config-operator/pkg/controller/common"
	k8serrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	clientset "k8s.io/client-go/kubernetes"
	"k8s.io/klog/v2"
)

// MOSCReconciler handles level-based reconciliation of MachineOSConfig objects.
type MOSCReconciler struct {
	mcfgclient mcfgclientset.Interface
	kubeclient clientset.Interface
	moscLister mcfglistersv1.MachineOSConfigLister
	mosbLister mcfglistersv1.MachineOSBuildLister
	mcpLister  mcfglistersv1.MachineConfigPoolLister
	mcLister   mcfglistersv1.MachineConfigLister

	events  services.EventRecorder
	metrics services.MetricsRecorder
	seeder  services.Seeder
	reuse   services.ImageReuseChecker
}

// NewMOSCReconciler constructs a MOSCReconciler with injected dependencies.
func NewMOSCReconciler(
	mcfgclient mcfgclientset.Interface,
	kubeclient clientset.Interface,
	moscLister mcfglistersv1.MachineOSConfigLister,
	mosbLister mcfglistersv1.MachineOSBuildLister,
	mcpLister mcfglistersv1.MachineConfigPoolLister,
	mcLister mcfglistersv1.MachineConfigLister,
	events services.EventRecorder,
	metrics services.MetricsRecorder,
	seeder services.Seeder,
	reuse services.ImageReuseChecker,
) *MOSCReconciler {
	return &MOSCReconciler{
		mcfgclient: mcfgclient,
		kubeclient: kubeclient,
		moscLister: moscLister,
		mosbLister: mosbLister,
		mcpLister:  mcpLister,
		mcLister:   mcLister,
		events:     events,
		metrics:    metrics,
		seeder:     seeder,
		reuse:      reuse,
	}
}

// ReconcileMOSC is the level-based reconciliation entry point for a
// MachineOSConfig identified by key.
func (r *MOSCReconciler) ReconcileMOSC(ctx context.Context, key string) error {
	mosc, err := r.moscLister.Get(key)
	if k8serrors.IsNotFound(err) {
		klog.V(4).Infof("MOSCReconciler: MachineOSConfig %q deleted, cleaning up build resources", key)
		return r.cleanupBuildResources(ctx, key)
	}
	if err != nil {
		return fmt.Errorf("could not get MachineOSConfig %q: %w", key, err)
	}

	mosc = mosc.DeepCopy()

	// Handle pre-built image seeding.
	if r.seeder.ShouldSeed(mosc) {
		image, _ := r.seeder.GetPreBuiltImage(mosc)
		klog.Infof("MOSCReconciler: seeding MachineOSConfig %q with image %q", mosc.Name, image)
		return r.seeder.Seed(ctx, mosc, image)
	}

	// Clean up pre-built image annotation after seeding is complete.
	if r.seeder.NeedsAnnotationCleanup(mosc) {
		return r.cleanupPreBuiltAnnotation(ctx, mosc)
	}

	// Handle rebuild annotation.
	if hasRebuildAnnotation(mosc) {
		r.events.RecordRebuildRequested(mosc, "rebuild annotation applied")
		return r.handleRebuild(ctx, mosc)
	}

	// Ensure a MachineOSBuild exists for the current desired state.
	return r.ensureBuildExists(ctx, mosc)
}

// cleanupPreBuiltAnnotation removes the pre-built image annotation after seeding completes.
func (r *MOSCReconciler) cleanupPreBuiltAnnotation(ctx context.Context, mosc *mcfgv1.MachineOSConfig) error {
	klog.Infof("MOSCReconciler: removing pre-built image annotation from %q", mosc.Name)
	delete(mosc.Annotations, constants.PreBuiltImageAnnotationKey)
	_, err := r.mcfgclient.MachineconfigurationV1().MachineOSConfigs().Update(ctx, mosc, metav1.UpdateOptions{})
	if err != nil {
		return fmt.Errorf("could not remove pre-built image annotation from MachineOSConfig %q: %w", mosc.Name, err)
	}
	return nil
}

// handleRebuild processes the rebuild annotation by deleting the current MOSB
// and creating a new one.
func (r *MOSCReconciler) handleRebuild(ctx context.Context, mosc *mcfgv1.MachineOSConfig) error {
	buildAnno, ok := mosc.Annotations[constants.CurrentMachineOSBuildAnnotationKey]
	if !ok || buildAnno == "" {
		klog.Infof("MOSCReconciler: %q has rebuild annotation but no current build, skipping", mosc.Name)
		return nil
	}

	// Delete the current MOSB so a new one can be created.
	if err := r.mcfgclient.MachineconfigurationV1().MachineOSBuilds().Delete(ctx, buildAnno, metav1.DeleteOptions{}); err != nil && !k8serrors.IsNotFound(err) {
		return fmt.Errorf("could not delete MachineOSBuild %q for rebuild: %w", buildAnno, err)
	}

	return r.createMachineOSBuild(ctx, mosc)
}

// ensureBuildExists checks whether a matching MachineOSBuild exists and
// creates one if not.
func (r *MOSCReconciler) ensureBuildExists(ctx context.Context, mosc *mcfgv1.MachineOSConfig) error {
	sel := utils.MachineOSBuildForPoolSelector(mosc)
	mosbs, err := r.mosbLister.List(sel)
	if err != nil {
		return fmt.Errorf("could not list MachineOSBuilds for MachineOSConfig %q: %w", mosc.Name, err)
	}

	// If the MOSC already has a current build that exists, nothing to do.
	for _, mosb := range mosbs {
		if isMOSBCurrentForMOSC(mosc, mosb) {
			klog.V(4).Infof("MOSCReconciler: %q already has current build %q", mosc.Name, mosb.Name)
			return nil
		}
	}

	// Check if we should skip because seeding hasn't happened yet.
	if r.seeder.ShouldSeed(mosc) {
		klog.V(4).Infof("MOSCReconciler: %q awaiting seeding, skipping build creation", mosc.Name)
		return nil
	}

	// Try to reuse an existing MOSB before creating a new one.
	desiredMOSB, err := r.buildDesiredMOSB(mosc)
	if err != nil {
		return err
	}

	existingMOSB, err := r.mosbLister.Get(desiredMOSB.Name)
	if err == nil && existingMOSB != nil {
		result, reuseErr := r.reuse.EvaluateReuse(ctx, mosc, existingMOSB)
		if reuseErr != nil {
			return fmt.Errorf("could not evaluate reuse for %q: %w", existingMOSB.Name, reuseErr)
		}
		if result.CanReuse {
			klog.Infof("MOSCReconciler: reusing existing MachineOSBuild %q", existingMOSB.Name)
			return r.updateMOSCStatus(ctx, mosc, existingMOSB)
		}
		// If NeedsRebuild, delete and fall through to create.
		if result.NeedsRebuild {
			if err := r.mcfgclient.MachineconfigurationV1().MachineOSBuilds().Delete(ctx, existingMOSB.Name, metav1.DeleteOptions{}); err != nil && !k8serrors.IsNotFound(err) {
				return fmt.Errorf("could not delete stale MOSB %q: %w", existingMOSB.Name, err)
			}
		}
	} else if err != nil && !k8serrors.IsNotFound(err) {
		return fmt.Errorf("could not check for existing MOSB: %w", err)
	}

	return r.createMachineOSBuild(ctx, mosc)
}

// createMachineOSBuild generates and creates a new MachineOSBuild for the MOSC.
func (r *MOSCReconciler) createMachineOSBuild(ctx context.Context, mosc *mcfgv1.MachineOSConfig) error {
	mosb, err := r.buildDesiredMOSB(mosc)
	if err != nil {
		return err
	}

	oref := metav1.NewControllerRef(mosc, mcfgv1.SchemeGroupVersion.WithKind("MachineOSConfig"))
	mosb.SetOwnerReferences([]metav1.OwnerReference{*oref})

	_, err = r.mcfgclient.MachineconfigurationV1().MachineOSBuilds().Create(ctx, mosb, metav1.CreateOptions{})
	if err != nil {
		if k8serrors.IsAlreadyExists(err) {
			klog.Infof("MOSCReconciler: MachineOSBuild %q already exists", mosb.Name)
			return nil
		}
		return fmt.Errorf("could not create MachineOSBuild for %q: %w", mosc.Name, err)
	}

	klog.Infof("MOSCReconciler: created MachineOSBuild %q for MachineOSConfig %q", mosb.Name, mosc.Name)
	r.events.RecordConfigReconciled(mosc)
	r.metrics.RecordConfigChange(mosc.Spec.MachineConfigPool.Name)
	return nil
}

// buildDesiredMOSB constructs the desired MachineOSBuild from current MCP/MC state.
func (r *MOSCReconciler) buildDesiredMOSB(mosc *mcfgv1.MachineOSConfig) (*mcfgv1.MachineOSBuild, error) {
	mcp, err := r.mcpLister.Get(mosc.Spec.MachineConfigPool.Name)
	if err != nil {
		return nil, fmt.Errorf("could not get MCP %q: %w", mosc.Spec.MachineConfigPool.Name, err)
	}
	mc, err := r.mcLister.Get(mcp.Spec.Configuration.Name)
	if err != nil {
		return nil, fmt.Errorf("could not get MC %q: %w", mcp.Spec.Configuration.Name, err)
	}

	mosb, err := buildrequest.NewMachineOSBuild(buildrequest.MachineOSBuildOpts{
		MachineConfig:     mc,
		MachineOSConfig:   mosc,
		MachineConfigPool: mcp,
	})
	if err != nil {
		return nil, fmt.Errorf("could not instantiate MachineOSBuild: %w", err)
	}
	return mosb, nil
}

// updateMOSCStatus sets the current build annotation and image pullspec on the MOSC.
func (r *MOSCReconciler) updateMOSCStatus(ctx context.Context, mosc *mcfgv1.MachineOSConfig, mosb *mcfgv1.MachineOSBuild) error {
	fresh, err := r.moscLister.Get(mosc.Name)
	if err != nil {
		return err
	}
	mosc = fresh.DeepCopy()

	metav1.SetMetaDataAnnotation(&mosc.ObjectMeta, constants.CurrentMachineOSBuildAnnotationKey, mosb.Name)
	updated, err := r.mcfgclient.MachineconfigurationV1().MachineOSConfigs().Update(ctx, mosc, metav1.UpdateOptions{})
	if err != nil {
		return fmt.Errorf("could not update MOSC %q annotations: %w", mosc.Name, err)
	}

	if mosb.Status.DigestedImagePushSpec != "" {
		// Use the object returned by Update() so that we have the
		// current resourceVersion for the subsequent status write.
		updated.Status.CurrentImagePullSpec = mosb.Status.DigestedImagePushSpec
		updated.Status.MachineOSBuild = &mcfgv1.ObjectReference{
			Name:     mosb.Name,
			Group:    mcfgv1.SchemeGroupVersion.Group,
			Resource: "machineosbuilds",
		}
		if _, err := r.mcfgclient.MachineconfigurationV1().MachineOSConfigs().UpdateStatus(ctx, updated, metav1.UpdateOptions{}); err != nil {
			return fmt.Errorf("could not update MOSC %q status: %w", mosc.Name, err)
		}
	}

	return nil
}

// cleanupBuildResources removes build Jobs and ephemeral objects (ConfigMaps,
// Secrets) associated with a deleted MachineOSConfig. MOSBs are cleaned by
// Kubernetes GC via ownerRefs, but Jobs are standalone and would be orphaned.
func (r *MOSCReconciler) cleanupBuildResources(ctx context.Context, moscName string) error {
	if r.kubeclient == nil {
		return nil
	}

	sel := labels.SelectorFromSet(map[string]string{
		constants.MachineOSConfigNameLabelKey: moscName,
	})
	listOpts := metav1.ListOptions{LabelSelector: sel.String()}

	// Delete orphaned build Jobs.
	propagation := metav1.DeletePropagationForeground
	jobs, err := r.kubeclient.BatchV1().Jobs(ctrlcommon.MCONamespace).List(ctx, listOpts)
	if err != nil {
		return fmt.Errorf("could not list jobs for deleted MOSC %q: %w", moscName, err)
	}
	for i := range jobs.Items {
		if err := r.kubeclient.BatchV1().Jobs(ctrlcommon.MCONamespace).Delete(ctx, jobs.Items[i].Name, metav1.DeleteOptions{PropagationPolicy: &propagation}); err != nil && !k8serrors.IsNotFound(err) {
			return fmt.Errorf("could not delete job %q for MOSC %q: %w", jobs.Items[i].Name, moscName, err)
		}
		klog.Infof("MOSCReconciler: deleted orphaned job %q for deleted MOSC %q", jobs.Items[i].Name, moscName)
	}

	// Delete orphaned ephemeral ConfigMaps.
	cms, err := r.kubeclient.CoreV1().ConfigMaps(ctrlcommon.MCONamespace).List(ctx, listOpts)
	if err != nil {
		return fmt.Errorf("could not list configmaps for deleted MOSC %q: %w", moscName, err)
	}
	for i := range cms.Items {
		if err := r.kubeclient.CoreV1().ConfigMaps(ctrlcommon.MCONamespace).Delete(ctx, cms.Items[i].Name, metav1.DeleteOptions{}); err != nil && !k8serrors.IsNotFound(err) {
			return fmt.Errorf("could not delete configmap %q for MOSC %q: %w", cms.Items[i].Name, moscName, err)
		}
		klog.Infof("MOSCReconciler: deleted orphaned configmap %q for deleted MOSC %q", cms.Items[i].Name, moscName)
	}

	// Delete orphaned ephemeral Secrets.
	secrets, err := r.kubeclient.CoreV1().Secrets(ctrlcommon.MCONamespace).List(ctx, listOpts)
	if err != nil {
		return fmt.Errorf("could not list secrets for deleted MOSC %q: %w", moscName, err)
	}
	for i := range secrets.Items {
		if err := r.kubeclient.CoreV1().Secrets(ctrlcommon.MCONamespace).Delete(ctx, secrets.Items[i].Name, metav1.DeleteOptions{}); err != nil && !k8serrors.IsNotFound(err) {
			return fmt.Errorf("could not delete secret %q for MOSC %q: %w", secrets.Items[i].Name, moscName, err)
		}
		klog.Infof("MOSCReconciler: deleted orphaned secret %q for deleted MOSC %q", secrets.Items[i].Name, moscName)
	}

	return nil
}

// --- helpers ---

func hasRebuildAnnotation(mosc *mcfgv1.MachineOSConfig) bool {
	if mosc.Annotations == nil {
		return false
	}
	_, ok := mosc.Annotations[constants.RebuildMachineOSConfigAnnotationKey]
	return ok
}

func isMOSBCurrentForMOSC(mosc *mcfgv1.MachineOSConfig, mosb *mcfgv1.MachineOSBuild) bool {
	if mosc.Annotations == nil {
		return false
	}
	return mosc.Annotations[constants.CurrentMachineOSBuildAnnotationKey] == mosb.Name
}
