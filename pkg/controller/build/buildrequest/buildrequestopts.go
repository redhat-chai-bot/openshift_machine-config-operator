package buildrequest

import (
	"fmt"
	goruntime "runtime"

	configv1 "github.com/openshift/api/config/v1"
	mcfgv1 "github.com/openshift/api/machineconfiguration/v1"
	mcfglistersv1 "github.com/openshift/client-go/machineconfiguration/listers/machineconfiguration/v1"
	"github.com/openshift/machine-config-operator/pkg/controller/build/constants"
	"github.com/openshift/machine-config-operator/pkg/controller/build/utils"
	ctrlcommon "github.com/openshift/machine-config-operator/pkg/controller/common"
	"github.com/openshift/machine-config-operator/pkg/helpers"
	"github.com/openshift/machine-config-operator/pkg/secrets"
	corev1 "k8s.io/api/core/v1"
	k8serrors "k8s.io/apimachinery/pkg/api/errors"
	corelistersv1 "k8s.io/client-go/listers/core/v1"
	"k8s.io/klog/v2"
)

// Holds all of the options used to produce a BuildRequest.
type BuildRequestOpts struct { //nolint:revive // This name is fine.
	MachineOSConfig *mcfgv1.MachineOSConfig
	MachineOSBuild  *mcfgv1.MachineOSBuild
	MachineConfig   *mcfgv1.MachineConfig
	Images          *ctrlcommon.Images

	BaseImagePullSecret  *corev1.Secret
	FinalImagePushSecret *corev1.Secret

	// Has user defined base image pull secret
	hasUserDefinedBaseImagePullSecret bool
	// Has /etc/pki/entitlement
	HasEtcPkiEntitlementKeys bool
	// Has /etc/yum.repos.d configs
	HasEtcYumReposDConfigs bool
	// Has /etc/pki/rpm-gpg configs
	HasEtcPkiRpmGpgKeys bool

	// Proxy Configurations
	Proxy *configv1.ProxyStatus
	// Additional trust bundles for proxy (user defined)
	AdditionalTrustBundle []byte
}

// Gets the packages for the kernel from the MachineConfig, if available.
func (b BuildRequestOpts) getKernelPackages() (string, map[string][]string, error) {

	newKtype := helpers.CanonicalizeKernelType(b.MachineConfig.Spec.KernelType)
	if newKtype == ctrlcommon.KernelTypeDefault {
		return "", nil, nil
	}

	// 64K memory pages kernel is only supported for aarch64
	if newKtype == ctrlcommon.KernelType64kPages && goruntime.GOARCH != ctrlcommon.GoArchARM64 {
		return "", nil, fmt.Errorf("64k-pages is only supported for aarch64 architecture")
	}

	return ctrlcommon.GetPackagesForSupportedKernelType(newKtype)
}

// Gets the packages for the extensions from the MachineConfig, if available.
func (b BuildRequestOpts) getExtensionsPackages() ([]string, error) {
	if len(b.MachineConfig.Spec.Extensions) == 0 {
		return nil, nil
	}

	return ctrlcommon.GetPackagesForSupportedExtensions(b.MachineConfig.Spec.Extensions)
}

// Listers holds the informer-backed listers required to populate
// BuildRequestOpts from the local cache instead of making direct API
// server calls.
type Listers struct {
	SecretLister           corelistersv1.SecretLister
	ConfigMapLister        corelistersv1.ConfigMapLister
	MachineConfigLister    mcfglistersv1.MachineConfigLister
	ControllerConfigLister mcfglistersv1.ControllerConfigLister
}

// Gets all of the image build request opts from informer-backed listers.
func newBuildRequestOptsFromAPI(l *Listers, mosb *mcfgv1.MachineOSBuild, mosc *mcfgv1.MachineOSConfig) (*BuildRequestOpts, error) {
	og := optsGetter{
		listers: l,
	}

	opts, err := og.getOpts(mosb, mosc)
	if err != nil {
		return nil, fmt.Errorf("could not get buildrequestopts from API: %w", err)
	}

	if opts.MachineOSConfig == nil {
		return nil, fmt.Errorf("expected MachineOSConfig to not be nil")
	}

	if opts.MachineOSBuild == nil {
		return nil, fmt.Errorf("expected MachineSOBuild to not be nil")
	}

	if opts.MachineConfig == nil {
		return nil, fmt.Errorf("expected MachineConfig to not be nil")
	}

	if opts.Images == nil {
		return nil, fmt.Errorf("expected images to not be nil")
	}

	if opts.BaseImagePullSecret == nil {
		return nil, fmt.Errorf("expected base image pull secret to not be nil")
	}

	if opts.FinalImagePushSecret == nil {
		return nil, fmt.Errorf("expected final image push secret to not be nil")
	}

	return opts, nil
}

// Holds all of the private methods used to populate the BuildRequestOpts
// fields from informer-backed listers.
type optsGetter struct {
	listers *Listers
}

func (o *optsGetter) validateMachineOSConfig(mosc *mcfgv1.MachineOSConfig) error {
	return utils.ValidateMachineOSConfigSpec(mosc)
}

// Validates that the required fields on a MachineOSBuild are set before beginning the build.
func (o *optsGetter) validateMachineOSBuild(mosb *mcfgv1.MachineOSBuild) error {
	if mosb == nil {
		return fmt.Errorf("expected MachineOSBuild not to be nil")
	}

	if mosb.Spec.MachineConfig.Name == "" {
		return fmt.Errorf("machineConfig.name empty for MachineOSBuild %s", mosb.Name)
	}

	return nil
}

// Gets the BuildRequestOpts using informer-backed listers.
func (o *optsGetter) getOpts(mosb *mcfgv1.MachineOSBuild, mosc *mcfgv1.MachineOSConfig) (*BuildRequestOpts, error) {
	if err := o.validateMachineOSConfig(mosc); err != nil {
		return nil, fmt.Errorf("could not validate MachineOSConfig: %w", err)
	}

	if err := o.validateMachineOSBuild(mosb); err != nil {
		return nil, fmt.Errorf("could not validate MachineOSBuild: %w", err)
	}

	opts, err := o.resolveEntitlements(mosc)
	if err != nil {
		return nil, fmt.Errorf("unable to resolve entitlements for MachineOSBuild %s: %w", mosb.Name, err)
	}

	imagesCM, err := o.listers.ConfigMapLister.ConfigMaps(ctrlcommon.MCONamespace).Get(ctrlcommon.MachineConfigOperatorImagesConfigMapName)
	if err != nil {
		return nil, fmt.Errorf("could not get images.json config: %w", err)
	}

	imagesConfig, err := ctrlcommon.ParseImagesFromConfigMap(imagesCM)
	if err != nil {
		return nil, fmt.Errorf("could not get images.json config: %w", err)
	}

	var baseImagePullSecretName string
	// Check if a base image pull secret was provided
	opts.hasUserDefinedBaseImagePullSecret = mosc.Spec.BaseImagePullSecret != nil
	if opts.hasUserDefinedBaseImagePullSecret {
		baseImagePullSecretName = mosc.Spec.BaseImagePullSecret.Name
	} else {
		// If not provided, fall back to the global pull secret copy in the MCO namespace
		klog.Infof("BaseImagePullSecret not defined for MachineOSConfig %s, falling back to global pull secret", mosc.Name)
		baseImagePullSecretName = ctrlcommon.GlobalPullSecretCopyName
	}

	baseImagePullSecret, err := o.getValidatedSecret(baseImagePullSecretName)
	if err != nil {
		return nil, fmt.Errorf("could not get base image pull secret %s: %w", baseImagePullSecretName, err)
	}

	finalImagePushSecret, err := o.getValidatedSecret(mosc.Spec.RenderedImagePushSecret.Name)
	if err != nil {
		return nil, fmt.Errorf("could not get final image push secret %s: %w", mosc.Spec.RenderedImagePushSecret.Name, err)
	}

	mc, err := o.listers.MachineConfigLister.Get(mosb.Spec.MachineConfig.Name)
	if err != nil {
		return nil, fmt.Errorf("could not retrieve machineconfig %s: %w", mosb.Spec.MachineConfig.Name, err)
	}

	cc, err := o.listers.ControllerConfigLister.Get(ctrlcommon.ControllerConfigName)
	if err != nil {
		return nil, fmt.Errorf("could not retrieve controllerconfig %s: %w", ctrlcommon.ControllerConfigName, err)
	}

	opts.Images = imagesConfig
	opts.MachineConfig = mc
	opts.BaseImagePullSecret = baseImagePullSecret
	opts.FinalImagePushSecret = finalImagePushSecret
	opts.MachineOSConfig = mosc.DeepCopy()
	opts.MachineOSBuild = mosb.DeepCopy()
	opts.Proxy = cc.Spec.Proxy
	opts.AdditionalTrustBundle = cc.Spec.AdditionalTrustBundle

	return opts, nil
}

// Gets an image pull secret from the lister and validates that it is usable.
func (o *optsGetter) getValidatedSecret(name string) (*corev1.Secret, error) {
	secret, err := o.listers.SecretLister.Secrets(ctrlcommon.MCONamespace).Get(name)
	if err != nil {
		return nil, fmt.Errorf("could not fetch secret %s: %w", name, err)
	}

	if err := secrets.ValidateKubernetesImageRegistrySecret(secret); err != nil {
		return nil, fmt.Errorf("could not validate secret %s: %w", name, err)
	}

	return secret, nil
}

// Determines whether the build makes use of entitlements based upon the
// presence (or lack thereof) of specific configmaps and secrets.
func (o *optsGetter) resolveEntitlements(mosc *mcfgv1.MachineOSConfig) (*BuildRequestOpts, error) {
	opts := &BuildRequestOpts{}

	etcPkiEntitlements, err := o.getOptionalSecret(constants.EtcPkiEntitlementSecretName + "-" + mosc.Spec.MachineConfigPool.Name)
	if err != nil {
		return nil, fmt.Errorf("could not determine status of optional Secret %q: %w", constants.EtcPkiEntitlementSecretName, err)
	}

	opts.HasEtcPkiEntitlementKeys = etcPkiEntitlements != nil

	etcPkiRpmGpgKeys, err := o.getOptionalSecret(constants.EtcPkiRpmGpgSecretName)
	if err != nil {
		return nil, fmt.Errorf("could not determine status of optional Secret %q: %w", constants.EtcPkiRpmGpgSecretName, err)
	}

	opts.HasEtcPkiRpmGpgKeys = etcPkiRpmGpgKeys != nil

	etcYumReposDConfigs, err := o.getOptionalConfigMap(constants.EtcYumReposDConfigMapName)
	if err != nil {
		return nil, fmt.Errorf("could not determine status of optional ConfigMap %q: %w", constants.EtcYumReposDConfigMapName, err)
	}

	opts.HasEtcYumReposDConfigs = etcYumReposDConfigs != nil

	return opts, nil
}

// Fetches an optional secret from the lister to inject into the build.
// Returns a nil error if the secret is not found.
func (o *optsGetter) getOptionalSecret(secretName string) (*corev1.Secret, error) {
	optionalSecret, err := o.listers.SecretLister.Secrets(ctrlcommon.MCONamespace).Get(secretName)
	if err == nil {
		klog.Infof("Optional build secret %q found, will include in build", secretName)
		return optionalSecret, nil
	}

	if k8serrors.IsNotFound(err) {
		klog.Infof("Could not find optional secret %q, will not include in build", secretName)
		return nil, nil
	}

	return nil, fmt.Errorf("could not retrieve optional secret: %s: %w", secretName, err)
}

// Fetches an optional ConfigMap from the lister to inject into the build.
// Returns a nil error if the ConfigMap is not found.
func (o *optsGetter) getOptionalConfigMap(configmapName string) (*corev1.ConfigMap, error) {
	optionalConfigMap, err := o.listers.ConfigMapLister.ConfigMaps(ctrlcommon.MCONamespace).Get(configmapName)
	if err == nil {
		klog.Infof("Optional build ConfigMap %q found, will include in build", configmapName)
		return optionalConfigMap, nil
	}

	if k8serrors.IsNotFound(err) {
		klog.Infof("Could not find ConfigMap %q, will not include in build", configmapName)
		return nil, nil
	}

	return nil, fmt.Errorf("could not retrieve optional ConfigMap: %s: %w", configmapName, err)
}
