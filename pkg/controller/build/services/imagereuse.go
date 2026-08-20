package services

import (
	"context"
	"fmt"

	"github.com/containers/image/v5/types"
	mcfgv1 "github.com/openshift/api/machineconfiguration/v1"
	mcfglistersv1 "github.com/openshift/client-go/machineconfiguration/listers/machineconfiguration/v1"
	"github.com/openshift/machine-config-operator/pkg/controller/build/constants"
	"github.com/openshift/machine-config-operator/pkg/controller/build/imagepruner"
	ctrlcommon "github.com/openshift/machine-config-operator/pkg/controller/common"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	clientset "k8s.io/client-go/kubernetes"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/klog/v2"
)

// ImageReuseResult holds the outcome of a reuse check.
type ImageReuseResult struct {
	// CanReuse indicates the existing build can be reused as-is.
	CanReuse bool
	// NeedsRebuild indicates the existing image is gone and a rebuild is required.
	NeedsRebuild bool
}

// ImageReuseChecker evaluates whether a previously built image can be reused
// instead of starting a new build.
type ImageReuseChecker interface {
	// InspectImage checks whether the given pullspec still exists in the registry.
	// Returns the inspect info if the image exists, or an error if it cannot be reached.
	InspectImage(ctx context.Context, pullspec string, mosb *mcfgv1.MachineOSBuild) (*types.ImageInspectInfo, error)

	// EvaluateReuse determines whether an existing MachineOSBuild can be reused
	// for the given MachineOSConfig. It examines the build state, checks image
	// existence, and returns the result.
	EvaluateReuse(ctx context.Context, mosc *mcfgv1.MachineOSConfig, existingMosb *mcfgv1.MachineOSBuild) (ImageReuseResult, error)
}

// imageReuseChecker is the production implementation of ImageReuseChecker.
type imageReuseChecker struct {
	imagePruner      imagepruner.ImagePruner
	kubeclient       clientset.Interface
	ccLister         mcfglistersv1.ControllerConfigLister
}

// NewImageReuseChecker constructs an ImageReuseChecker.
func NewImageReuseChecker(
	pruner imagepruner.ImagePruner,
	kubeclient clientset.Interface,
	ccLister mcfglistersv1.ControllerConfigLister,
) ImageReuseChecker {
	return &imageReuseChecker{
		imagePruner: pruner,
		kubeclient:  kubeclient,
		ccLister:    ccLister,
	}
}

func (c *imageReuseChecker) InspectImage(ctx context.Context, pullspec string, mosb *mcfgv1.MachineOSBuild) (*types.ImageInspectInfo, error) {
	secret, cc, err := c.getObjectsForImagePruner(ctx, mosb)
	if err != nil {
		return nil, err
	}

	info, _, err := c.imagePruner.InspectImage(ctx, pullspec, secret, cc)
	return info, err
}

func (c *imageReuseChecker) EvaluateReuse(ctx context.Context, mosc *mcfgv1.MachineOSConfig, existingMosb *mcfgv1.MachineOSBuild) (ImageReuseResult, error) {
	existingState := ctrlcommon.NewMachineOSBuildState(existingMosb)
	result := ImageReuseResult{}

	// If the existing build succeeded and has a pullspec, verify the image still exists.
	if existingState.IsBuildSuccess() && existingMosb.Status.DigestedImagePushSpec != "" {
		image := string(existingMosb.Spec.RenderedImagePushSpec)
		klog.Infof("ImageReuseChecker: existing MachineOSBuild %q found, checking image %q", existingMosb.Name, image)

		inspect, err := c.InspectImage(ctx, image, existingMosb)
		if inspect != nil && err == nil {
			klog.Infof("ImageReuseChecker: image %q exists, reuse possible for MachineOSConfig %q", image, mosc.Name)
			result.CanReuse = true
			return result, nil
		}

		klog.Infof("ImageReuseChecker: image %q no longer exists, rebuild needed. Error: %v", image, err)
		result.NeedsRebuild = true
		return result, nil
	}

	// If the existing build is in a transient state, it can be reused.
	if existingState.IsInTransientState() {
		klog.Infof("ImageReuseChecker: MachineOSBuild %q in transient state, reuse possible for MachineOSConfig %q", existingMosb.Name, mosc.Name)
		result.CanReuse = true
		return result, nil
	}

	return result, nil
}

// getObjectsForImagePruner retrieves the push secret and ControllerConfig
// needed to inspect or delete a container image.
func (c *imageReuseChecker) getObjectsForImagePruner(ctx context.Context, mosb *mcfgv1.MachineOSBuild) (*corev1.Secret, *mcfgv1.ControllerConfig, error) {
	secretName := ""
	if mosb.Annotations != nil {
		secretName = mosb.Annotations[constants.RenderedImagePushSecretAnnotationKey]
	}

	if secretName == "" {
		return nil, nil, fmt.Errorf("MachineOSBuild %s missing annotation %s", mosb.Name, constants.RenderedImagePushSecretAnnotationKey)
	}

	secret, err := c.kubeclient.CoreV1().Secrets(ctrlcommon.MCONamespace).Get(ctx, secretName, metav1.GetOptions{})
	if err != nil {
		return nil, nil, fmt.Errorf("could not get rendered push secret %s: %w", secretName, err)
	}

	controllerConfigs, err := c.ccLister.List(labels.Everything())
	if err != nil {
		return nil, nil, fmt.Errorf("could not list ControllerConfigs: %w", err)
	}
	if len(controllerConfigs) == 0 {
		return nil, nil, fmt.Errorf("no ControllerConfigs found")
	}

	return secret.DeepCopy(), controllerConfigs[0].DeepCopy(), nil
}
