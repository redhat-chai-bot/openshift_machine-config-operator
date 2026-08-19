package v2

import (
	"context"
	"fmt"
	"time"

	mcfgv1 "github.com/openshift/api/machineconfiguration/v1"
	mcfgclientset "github.com/openshift/client-go/machineconfiguration/clientset/versioned"
	"github.com/openshift/machine-config-operator/pkg/controller/build/buildrequest"
	"github.com/openshift/machine-config-operator/pkg/controller/build/constants"
	"github.com/openshift/machine-config-operator/pkg/controller/build/utils"
	ctrlcommon "github.com/openshift/machine-config-operator/pkg/controller/common"
	k8serrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	clientset "k8s.io/client-go/kubernetes"
	"k8s.io/klog/v2"
)

// SeedManager handles seeding MachineOSConfigs with pre-built images.
// This allows clusters to be bootstrapped with a pre-built image specified
// via the PreBuiltImageAnnotationKey annotation on the MachineOSConfig.
type SeedManager struct {
	mcfgclient mcfgclientset.Interface
	kubeclient clientset.Interface
	*listers
}

// NewSeedManager creates a new SeedManager.
func NewSeedManager(mcfgclient mcfgclientset.Interface, kubeclient clientset.Interface, l *listers) *SeedManager {
	return &SeedManager{
		mcfgclient: mcfgclient,
		kubeclient: kubeclient,
		listers:    l,
	}
}

// ShouldSeed returns true if the MachineOSConfig should be seeded with a
// pre-built image. This is the case when the pre-built image annotation is
// present and the currentBuild annotation has not been set yet.
func (s *SeedManager) ShouldSeed(mosc *mcfgv1.MachineOSConfig) bool {
	return shouldSeedWithPreBuiltImage(mosc)
}

// NeedsCleanup returns true if seeding is complete and the pre-built image
// annotation should be removed. Seeding is considered complete when the
// currentBuild annotation is set and the MOSC status has been populated.
func (s *SeedManager) NeedsCleanup(mosc *mcfgv1.MachineOSConfig) bool {
	return needsPreBuiltImageAnnotationCleanup(mosc)
}

// Cleanup removes the pre-built image annotation from the MachineOSConfig
// after seeding is complete.
func (s *SeedManager) Cleanup(ctx context.Context, mosc *mcfgv1.MachineOSConfig) error {
	// Get a fresh copy to avoid conflicts.
	current, err := s.mcfgclient.MachineconfigurationV1().MachineOSConfigs().Get(ctx, mosc.Name, metav1.GetOptions{})
	if err != nil {
		return fmt.Errorf("could not get MachineOSConfig %q for cleanup: %w", mosc.Name, err)
	}

	delete(current.Annotations, constants.PreBuiltImageAnnotationKey)

	_, err = s.mcfgclient.MachineconfigurationV1().MachineOSConfigs().Update(ctx, current, metav1.UpdateOptions{})
	if err != nil {
		return fmt.Errorf("could not remove pre-built image annotation from MachineOSConfig %q: %w", mosc.Name, err)
	}

	klog.Infof("Removed pre-built image annotation from MachineOSConfig %q", mosc.Name)
	return nil
}

// SeedIfNeeded checks if the MachineOSConfig should be seeded and performs
// the seeding if necessary. Returns (handled, error) where handled is true
// if the seeding was performed (or skipped because it's not needed).
func (s *SeedManager) SeedIfNeeded(ctx context.Context, mosc *mcfgv1.MachineOSConfig) (bool, error) {
	if !s.ShouldSeed(mosc) {
		return false, nil
	}

	imageSpec, _ := getPreBuiltImage(mosc)
	if err := s.SeedMachineOSConfig(ctx, mosc, imageSpec); err != nil {
		return true, err
	}
	return true, nil
}

// SeedMachineOSConfig performs the full seeding workflow:
// 1. Validates the push secret exists
// 2. Generates the expected MachineOSBuild name using buildrequest
// 3. Creates a synthetic MachineOSBuild with success status
// 4. Updates the MachineOSConfig with the build annotation and status
func (s *SeedManager) SeedMachineOSConfig(ctx context.Context, mosc *mcfgv1.MachineOSConfig, imageSpec string) error {
	// Step 1: Validate push secret exists.
	if mosc.Spec.RenderedImagePushSecret.Name == "" {
		return fmt.Errorf("MachineOSConfig %q has no rendered image push secret", mosc.Name)
	}
	if err := s.ensureSecretExists(ctx, mosc.Spec.RenderedImagePushSecret.Name); err != nil {
		return fmt.Errorf("could not ensure push secret %s exists: %w", mosc.Spec.RenderedImagePushSecret.Name, err)
	}

	// Step 2: Generate the expected MachineOSBuild using buildrequest.
	mcp, err := s.machineConfigPoolLister.Get(mosc.Spec.MachineConfigPool.Name)
	if err != nil {
		return fmt.Errorf("could not get MachineConfigPool %q: %w", mosc.Spec.MachineConfigPool.Name, err)
	}

	mc, err := s.machineConfigLister.Get(mcp.Spec.Configuration.Name)
	if err != nil {
		return fmt.Errorf("could not get MachineConfig %q: %w", mcp.Spec.Configuration.Name, err)
	}

	templateMOSB, err := buildrequest.NewMachineOSBuild(buildrequest.MachineOSBuildOpts{
		MachineConfig:     mc,
		MachineConfigPool: mcp,
		MachineOSConfig:   mosc,
	})
	if err != nil {
		return fmt.Errorf("could not generate MachineOSBuild template for MachineOSConfig %q: %w", mosc.Name, err)
	}

	// Step 3: Create synthetic MachineOSBuild.
	syntheticMOSB, err := s.SeedMOSB(ctx, mosc, mcp, templateMOSB.Name, imageSpec)
	if err != nil {
		return fmt.Errorf("could not create synthetic MachineOSBuild for MachineOSConfig %q: %w", mosc.Name, err)
	}

	// Step 4: Update MachineOSConfig annotations and status.
	if err := s.updateMOSCForSeeding(ctx, mosc, syntheticMOSB, imageSpec); err != nil {
		return fmt.Errorf("could not update MachineOSConfig %q for seeding: %w", mosc.Name, err)
	}

	klog.Infof("Successfully seeded MachineOSConfig %q with pre-built image %q", mosc.Name, imageSpec)
	return nil
}

// SeedMOSB creates a synthetic MachineOSBuild marked as succeeded with the
// given pre-built image pullspec.
func (s *SeedManager) SeedMOSB(ctx context.Context, mosc *mcfgv1.MachineOSConfig, mcp *mcfgv1.MachineConfigPool, buildName, imageSpec string) (*mcfgv1.MachineOSBuild, error) {
	buildLabels := utils.GetMachineOSBuildLabels(mosc, mcp)
	buildLabels[constants.PreBuiltImageLabelKey] = constants.TrueValue

	now := metav1.Now()
	buildEnd := metav1.NewTime(now.Add(1 * time.Second))

	oref := metav1.NewControllerRef(mosc, mcfgv1.SchemeGroupVersion.WithKind("MachineOSConfig"))

	mosb := &mcfgv1.MachineOSBuild{
		TypeMeta: metav1.TypeMeta{
			Kind:       "MachineOSBuild",
			APIVersion: "machineconfiguration.openshift.io/v1",
		},
		ObjectMeta: metav1.ObjectMeta{
			Name:   buildName,
			Labels: buildLabels,
			Finalizers: []string{
				metav1.FinalizerDeleteDependents,
			},
			Annotations: map[string]string{
				constants.RenderedImagePushSecretAnnotationKey: mosc.Spec.RenderedImagePushSecret.Name,
			},
			OwnerReferences: []metav1.OwnerReference{*oref},
		},
		Spec: mcfgv1.MachineOSBuildSpec{
			RenderedImagePushSpec: mosc.Spec.RenderedImagePushSpec,
			MachineConfig: mcfgv1.MachineConfigReference{
				Name: mcp.Spec.Configuration.Name,
			},
			MachineOSConfig: mcfgv1.MachineOSConfigReference{
				Name: mosc.Name,
			},
		},
		Status: mcfgv1.MachineOSBuildStatus{
			BuildStart:            &now,
			BuildEnd:              &buildEnd,
			DigestedImagePushSpec: mcfgv1.ImageDigestFormat(imageSpec),
			Conditions:            syntheticSuccessConditions(now, imageSpec),
		},
	}

	// Check if it already exists (race between seeding and normal workflow).
	existingMOSB, err := s.mcfgclient.MachineconfigurationV1().MachineOSBuilds().Get(ctx, buildName, metav1.GetOptions{})
	if err != nil && !k8serrors.IsNotFound(err) {
		return nil, fmt.Errorf("could not check if MachineOSBuild %q exists: %w", buildName, err)
	}

	var createdMOSB *mcfgv1.MachineOSBuild
	if k8serrors.IsNotFound(err) {
		createdMOSB, err = s.mcfgclient.MachineconfigurationV1().MachineOSBuilds().Create(ctx, mosb, metav1.CreateOptions{})
		if err != nil {
			return nil, fmt.Errorf("could not create synthetic MachineOSBuild %q: %w", buildName, err)
		}
		klog.Infof("Created synthetic MachineOSBuild %q for pre-built image %q", buildName, imageSpec)
	} else {
		// Already exists — ensure it has the pre-built-image label.
		if existingMOSB.Labels == nil {
			existingMOSB.Labels = make(map[string]string)
		}
		if existingMOSB.Labels[constants.PreBuiltImageLabelKey] != constants.TrueValue {
			existingMOSB.Labels[constants.PreBuiltImageLabelKey] = constants.TrueValue
			existingMOSB, err = s.mcfgclient.MachineconfigurationV1().MachineOSBuilds().Update(ctx, existingMOSB, metav1.UpdateOptions{})
			if err != nil {
				return nil, fmt.Errorf("could not update labels on existing MachineOSBuild %q: %w", buildName, err)
			}
		}
		createdMOSB = existingMOSB
	}

	// Update status separately (status is ignored on Create).
	createdMOSB.Status = mosb.Status
	updatedMOSB, err := s.mcfgclient.MachineconfigurationV1().MachineOSBuilds().UpdateStatus(ctx, createdMOSB, metav1.UpdateOptions{})
	if err != nil {
		return nil, fmt.Errorf("could not update status on synthetic MachineOSBuild %q: %w", buildName, err)
	}

	return updatedMOSB, nil
}

// updateMOSCForSeeding updates the MachineOSConfig with the current build
// annotation and status from the synthetic MachineOSBuild.
func (s *SeedManager) updateMOSCForSeeding(ctx context.Context, mosc *mcfgv1.MachineOSConfig, mosb *mcfgv1.MachineOSBuild, imageSpec string) error {
	// Set the current build annotation to mark seeding as complete.
	metav1.SetMetaDataAnnotation(&mosc.ObjectMeta, constants.CurrentMachineOSBuildAnnotationKey, mosb.Name)

	updatedMOSC, err := s.mcfgclient.MachineconfigurationV1().MachineOSConfigs().Update(ctx, mosc, metav1.UpdateOptions{})
	if err != nil {
		return fmt.Errorf("could not update MachineOSConfig %q annotations: %w", mosc.Name, err)
	}

	// Update status with image pullspec.
	updatedMOSC.Status.CurrentImagePullSpec = mcfgv1.ImageDigestFormat(imageSpec)
	updatedMOSC.Status.ObservedGeneration = updatedMOSC.GetGeneration()
	updatedMOSC.Status.MachineOSBuild = &mcfgv1.ObjectReference{
		Name:     mosb.Name,
		Group:    mcfgv1.SchemeGroupVersion.Group,
		Resource: "machineosbuilds",
	}

	updatedMOSC.Status.Conditions = append(updatedMOSC.Status.Conditions, metav1.Condition{
		Type:               constants.MachineOSConfigSeeded,
		Status:             metav1.ConditionTrue,
		LastTransitionTime: metav1.NewTime(time.Now()),
		Reason:             constants.ReasonPreBuiltImageSeeded,
		Message:            fmt.Sprintf("MachineOSConfig seeded with pre-built image %q", imageSpec),
	})

	_, err = s.mcfgclient.MachineconfigurationV1().MachineOSConfigs().UpdateStatus(ctx, updatedMOSC, metav1.UpdateOptions{})
	if err != nil {
		return fmt.Errorf("could not update MachineOSConfig %q status: %w", mosc.Name, err)
	}

	return nil
}

// ensureSecretExists verifies the secret exists in the MCO namespace.
func (s *SeedManager) ensureSecretExists(ctx context.Context, secretName string) error {
	_, err := s.kubeclient.CoreV1().Secrets(ctrlcommon.MCONamespace).Get(ctx, secretName, metav1.GetOptions{})
	if err == nil {
		return nil
	}

	if k8serrors.IsNotFound(err) {
		return fmt.Errorf("required secret %s not found in %s namespace", secretName, ctrlcommon.MCONamespace)
	}

	return fmt.Errorf("failed to check if secret %s exists: %w", secretName, err)
}

const preBuiltImageSkipMessage = "Skipped: using pre-built image"

// syntheticSuccessConditions returns the conditions for a synthetic
// MachineOSBuild that represents a pre-built image.
func syntheticSuccessConditions(now metav1.Time, imageSpec string) []metav1.Condition {
	return []metav1.Condition{
		{
			Type:               string(mcfgv1.MachineOSBuildSucceeded),
			Status:             metav1.ConditionTrue,
			LastTransitionTime: now,
			Reason:             constants.ReasonPreBuiltImageSeeded,
			Message:            fmt.Sprintf("Pre-built image %q successfully seeded", imageSpec),
		},
		{
			Type:               string(mcfgv1.MachineOSBuildPrepared),
			Status:             metav1.ConditionFalse,
			LastTransitionTime: now,
			Reason:             constants.ReasonPreBuiltImageSeeded,
			Message:            preBuiltImageSkipMessage,
		},
		{
			Type:               string(mcfgv1.MachineOSBuilding),
			Status:             metav1.ConditionFalse,
			LastTransitionTime: now,
			Reason:             constants.ReasonPreBuiltImageSeeded,
			Message:            preBuiltImageSkipMessage,
		},
		{
			Type:               string(mcfgv1.MachineOSBuildFailed),
			Status:             metav1.ConditionFalse,
			LastTransitionTime: now,
			Reason:             constants.ReasonPreBuiltImageSeeded,
			Message:            preBuiltImageSkipMessage,
		},
		{
			Type:               string(mcfgv1.MachineOSBuildInterrupted),
			Status:             metav1.ConditionFalse,
			LastTransitionTime: now,
			Reason:             constants.ReasonPreBuiltImageSeeded,
			Message:            preBuiltImageSkipMessage,
		},
	}
}
