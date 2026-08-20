package services

import (
	"context"
	"fmt"
	"time"

	mcfgv1 "github.com/openshift/api/machineconfiguration/v1"
	mcfgclientset "github.com/openshift/client-go/machineconfiguration/clientset/versioned"
	mcfglistersv1 "github.com/openshift/client-go/machineconfiguration/listers/machineconfiguration/v1"
	"github.com/openshift/machine-config-operator/pkg/controller/build/buildrequest"
	"github.com/openshift/machine-config-operator/pkg/controller/build/constants"
	"github.com/openshift/machine-config-operator/pkg/controller/build/utils"
	ctrlcommon "github.com/openshift/machine-config-operator/pkg/controller/common"
	k8serrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	clientset "k8s.io/client-go/kubernetes"
	"k8s.io/klog/v2"
)

// Seeder handles the pre-built-image seeding flow for MachineOSConfigs.
type Seeder interface {
	// ShouldSeed reports whether the MachineOSConfig should be seeded with a
	// pre-built image. Returns true when the annotation is present and the
	// current-build marker is absent.
	ShouldSeed(mosc *mcfgv1.MachineOSConfig) bool

	// GetPreBuiltImage returns the pre-built image pullspec from the MOSC's
	// annotations, and a boolean indicating whether the annotation is present
	// and non-empty.
	GetPreBuiltImage(mosc *mcfgv1.MachineOSConfig) (string, bool)

	// NeedsAnnotationCleanup reports whether seeding is complete and the
	// pre-built-image annotation can be removed.
	NeedsAnnotationCleanup(mosc *mcfgv1.MachineOSConfig) bool

	// Seed performs the full seeding workflow: verifies the push secret,
	// creates a synthetic MachineOSBuild with success status, and updates
	// the MachineOSConfig with the build annotation and status.
	Seed(ctx context.Context, mosc *mcfgv1.MachineOSConfig, imageSpec string) error
}

// seeder is the production implementation of Seeder.
type seeder struct {
	mcfgclient mcfgclientset.Interface
	kubeclient clientset.Interface
	mcpLister  mcfglistersv1.MachineConfigPoolLister
	mcLister   mcfglistersv1.MachineConfigLister
}

// NewSeeder creates a Seeder backed by the given clients and listers.
func NewSeeder(
	mcfgclient mcfgclientset.Interface,
	kubeclient clientset.Interface,
	mcpLister mcfglistersv1.MachineConfigPoolLister,
	mcLister mcfglistersv1.MachineConfigLister,
) Seeder {
	return &seeder{
		mcfgclient: mcfgclient,
		kubeclient: kubeclient,
		mcpLister:  mcpLister,
		mcLister:   mcLister,
	}
}

func (s *seeder) GetPreBuiltImage(mosc *mcfgv1.MachineOSConfig) (string, bool) {
	if mosc.Annotations == nil {
		return "", false
	}
	image, exists := mosc.Annotations[constants.PreBuiltImageAnnotationKey]
	return image, exists && image != ""
}

func (s *seeder) ShouldSeed(mosc *mcfgv1.MachineOSConfig) bool {
	_, hasImage := s.GetPreBuiltImage(mosc)
	return hasImage && !hasCurrentBuildAnnotation(mosc)
}

func (s *seeder) NeedsAnnotationCleanup(mosc *mcfgv1.MachineOSConfig) bool {
	_, hasImage := s.GetPreBuiltImage(mosc)
	return hasCurrentBuildAnnotation(mosc) &&
		mosc.Status.CurrentImagePullSpec != "" &&
		hasImage
}

func (s *seeder) Seed(ctx context.Context, mosc *mcfgv1.MachineOSConfig, imageSpec string) error {
	// Step 1: Verify push secret exists.
	if mosc.Spec.RenderedImagePushSecret.Name == "" {
		return fmt.Errorf("MachineOSConfig %q has no rendered image push secret", mosc.Name)
	}
	if err := s.ensureSecretExists(ctx, mosc.Spec.RenderedImagePushSecret.Name); err != nil {
		return fmt.Errorf("could not ensure push secret %s exists: %w", mosc.Spec.RenderedImagePushSecret.Name, err)
	}

	// Step 2: Generate expected MachineOSBuild name from MCP/MC hash.
	mcp, err := s.mcpLister.Get(mosc.Spec.MachineConfigPool.Name)
	if err != nil {
		return fmt.Errorf("could not get MachineConfigPool %q: %w", mosc.Spec.MachineConfigPool.Name, err)
	}
	mc, err := s.mcLister.Get(mcp.Spec.Configuration.Name)
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

	// Step 3: Create synthetic MachineOSBuild with pre-populated success status.
	syntheticMOSB, err := s.createSyntheticMachineOSBuild(ctx, mosc, mcp, templateMOSB.Name, imageSpec)
	if err != nil {
		return fmt.Errorf("could not create synthetic MachineOSBuild for MachineOSConfig %q: %w", mosc.Name, err)
	}

	// Step 4: Update MachineOSConfig annotations and status.
	if err := s.updateMachineOSConfigForSeeding(ctx, mosc, syntheticMOSB, imageSpec); err != nil {
		return fmt.Errorf("could not update MachineOSConfig %q for seeding: %w", mosc.Name, err)
	}

	klog.Infof("Seeder: successfully seeded MachineOSConfig %q with pre-built image %q", mosc.Name, imageSpec)
	return nil
}

func (s *seeder) createSyntheticMachineOSBuild(ctx context.Context, mosc *mcfgv1.MachineOSConfig, mcp *mcfgv1.MachineConfigPool, buildName, imageSpec string) (*mcfgv1.MachineOSBuild, error) {
	buildLabels := utils.GetMachineOSBuildLabels(mosc, mcp)
	buildLabels[constants.PreBuiltImageLabelKey] = constants.TrueValue

	buildAnnotations := map[string]string{
		constants.RenderedImagePushSecretAnnotationKey: mosc.Spec.RenderedImagePushSecret.Name,
	}

	now := metav1.Now()
	buildEnd := metav1.NewTime(now.Add(1 * time.Second))
	oref := metav1.NewControllerRef(mosc, mcfgv1.SchemeGroupVersion.WithKind("MachineOSConfig"))

	mosb := &mcfgv1.MachineOSBuild{
		TypeMeta: metav1.TypeMeta{
			Kind:       "MachineOSBuild",
			APIVersion: "machineconfiguration.openshift.io/v1",
		},
		ObjectMeta: metav1.ObjectMeta{
			Name:            buildName,
			Labels:          buildLabels,
			Finalizers:      []string{metav1.FinalizerDeleteDependents},
			Annotations:     buildAnnotations,
			OwnerReferences: []metav1.OwnerReference{*oref},
		},
		Spec: mcfgv1.MachineOSBuildSpec{
			RenderedImagePushSpec: mosc.Spec.RenderedImagePushSpec,
			MachineConfig:        mcfgv1.MachineConfigReference{Name: mcp.Spec.Configuration.Name},
			MachineOSConfig:      mcfgv1.MachineOSConfigReference{Name: mosc.Name},
		},
		Status: mcfgv1.MachineOSBuildStatus{
			BuildStart:            &now,
			BuildEnd:              &buildEnd,
			DigestedImagePushSpec: mcfgv1.ImageDigestFormat(imageSpec),
			Conditions: []metav1.Condition{
				{Type: string(mcfgv1.MachineOSBuildSucceeded), Status: metav1.ConditionTrue, LastTransitionTime: now, Reason: constants.ReasonPreBuiltImageSeeded, Message: fmt.Sprintf("Pre-built image %q successfully seeded", imageSpec)},
				{Type: string(mcfgv1.MachineOSBuildPrepared), Status: metav1.ConditionFalse, LastTransitionTime: now, Reason: constants.ReasonPreBuiltImageSeeded, Message: "Skipped: using pre-built image"},
				{Type: string(mcfgv1.MachineOSBuilding), Status: metav1.ConditionFalse, LastTransitionTime: now, Reason: constants.ReasonPreBuiltImageSeeded, Message: "Skipped: using pre-built image"},
				{Type: string(mcfgv1.MachineOSBuildFailed), Status: metav1.ConditionFalse, LastTransitionTime: now, Reason: constants.ReasonPreBuiltImageSeeded, Message: "Skipped: using pre-built image"},
				{Type: string(mcfgv1.MachineOSBuildInterrupted), Status: metav1.ConditionFalse, LastTransitionTime: now, Reason: constants.ReasonPreBuiltImageSeeded, Message: "Skipped: using pre-built image"},
			},
		},
	}

	// Check if build already exists (race protection).
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
		klog.Infof("Seeder: created synthetic MachineOSBuild %q for pre-built image %q", buildName, imageSpec)
	} else {
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

func (s *seeder) updateMachineOSConfigForSeeding(ctx context.Context, mosc *mcfgv1.MachineOSConfig, mosb *mcfgv1.MachineOSBuild, imageSpec string) error {
	metav1.SetMetaDataAnnotation(&mosc.ObjectMeta, constants.CurrentMachineOSBuildAnnotationKey, mosb.Name)

	updatedMOSC, err := s.mcfgclient.MachineconfigurationV1().MachineOSConfigs().Update(ctx, mosc, metav1.UpdateOptions{})
	if err != nil {
		return fmt.Errorf("could not update MachineOSConfig %q annotations: %w", mosc.Name, err)
	}

	updatedMOSC.Status.CurrentImagePullSpec = mcfgv1.ImageDigestFormat(imageSpec)
	updatedMOSC.Status.ObservedGeneration = updatedMOSC.GetGeneration()
	updatedMOSC.Status.MachineOSBuild = &mcfgv1.ObjectReference{
		Name:     mosb.Name,
		Group:    mcfgv1.SchemeGroupVersion.Group,
		Resource: "machineosbuilds",
	}

	seededCondition := metav1.Condition{
		Type:               constants.MachineOSConfigSeeded,
		Status:             metav1.ConditionTrue,
		LastTransitionTime: metav1.NewTime(time.Now()),
		Reason:             constants.ReasonPreBuiltImageSeeded,
		Message:            fmt.Sprintf("MachineOSConfig seeded with pre-built image %q", imageSpec),
	}
	updatedMOSC.Status.Conditions = append(updatedMOSC.Status.Conditions, seededCondition)

	_, err = s.mcfgclient.MachineconfigurationV1().MachineOSConfigs().UpdateStatus(ctx, updatedMOSC, metav1.UpdateOptions{})
	if err != nil {
		return fmt.Errorf("could not update MachineOSConfig %q status: %w", mosc.Name, err)
	}

	klog.Infof("Seeder: updated MachineOSConfig %q status with pre-built image %q", mosc.Name, imageSpec)
	return nil
}

func (s *seeder) ensureSecretExists(ctx context.Context, secretName string) error {
	_, err := s.kubeclient.CoreV1().Secrets(ctrlcommon.MCONamespace).Get(ctx, secretName, metav1.GetOptions{})
	if err == nil {
		return nil
	}
	if k8serrors.IsNotFound(err) {
		return fmt.Errorf("required secret %s not found in %s namespace", secretName, ctrlcommon.MCONamespace)
	}
	return fmt.Errorf("failed to check if secret %s exists: %w", secretName, err)
}

// hasCurrentBuildAnnotation checks if the MOSC has the current-build annotation set.
func hasCurrentBuildAnnotation(mosc *mcfgv1.MachineOSConfig) bool {
	if mosc.Annotations == nil {
		return false
	}
	_, ok := mosc.Annotations[constants.CurrentMachineOSBuildAnnotationKey]
	return ok
}
