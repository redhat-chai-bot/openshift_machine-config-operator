package build

import (
	"context"
	"fmt"

	mcfgv1 "github.com/openshift/api/machineconfiguration/v1"
	"github.com/openshift/client-go/machineconfiguration/clientset/versioned"
	"github.com/openshift/machine-config-operator/pkg/controller/build/utils"
	ctrlcommon "github.com/openshift/machine-config-operator/pkg/controller/common"
	"github.com/openshift/machine-config-operator/pkg/secrets"
	corev1 "k8s.io/api/core/v1"
	k8serrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	clientset "k8s.io/client-go/kubernetes"
)

// ValidateOnClusterBuildConfig validates the existence of the MachineOSConfig and the required build inputs.
func ValidateOnClusterBuildConfig(ctx context.Context, kubeclient clientset.Interface, mcfgclient versioned.Interface, layeredMCPs []*mcfgv1.MachineConfigPool) error {
	// Validate the presence of the MachineOSConfig
	machineOSConfigs, err := mcfgclient.MachineconfigurationV1().MachineOSConfigs().List(ctx, metav1.ListOptions{})
	if err != nil {
		return err
	}

	secretGetter := func(name string) (*corev1.Secret, error) {
		return kubeclient.CoreV1().Secrets(ctrlcommon.MCONamespace).Get(ctx, name, metav1.GetOptions{})
	}

	moscForPoolExists := false
	var moscForPool *mcfgv1.MachineOSConfig
	for _, pool := range layeredMCPs {
		moscForPoolExists = false
		for _, mosc := range machineOSConfigs.Items {
			if mosc.Spec.MachineConfigPool.Name == pool.Name {
				moscForPoolExists = true
				moscForPool = &mosc
				break
			}
		}

		if !moscForPoolExists {
			return fmt.Errorf("MachineOSConfig for pool %s missing, did you create it?", pool.Name)
		}

		mcpGetter := func(_ string) (*mcfgv1.MachineConfigPool, error) {
			return pool, nil
		}

		if err := validateMachineOSConfig(mcpGetter, secretGetter, moscForPool); err != nil {
			return err
		}
	}

	return nil
}

func validateMachineOSConfig(mcpGetter func(string) (*mcfgv1.MachineConfigPool, error), secretGetter func(string) (*corev1.Secret, error), mosc *mcfgv1.MachineOSConfig) error {
	if err := utils.ValidateMachineOSConfigSpec(mosc); err != nil {
		return err
	}

	_, err := mcpGetter(mosc.Spec.MachineConfigPool.Name)
	if err != nil && k8serrors.IsNotFound(err) {
		return fmt.Errorf("no MachineConfigPool named %s exists for MachineOSConfig %s", mosc.Spec.MachineConfigPool.Name, mosc.Name)
	}

	if err != nil {
		return fmt.Errorf("could not get MachineConfigPool %s: %w", mosc.Spec.MachineConfigPool.Name, err)
	}

	secretFields := map[string]string{
		mosc.Spec.RenderedImagePushSecret.Name: "renderedImagePushSecret",
	}
	// Add base image pull secret if it has been defined in the MOSC
	if mosc.Spec.BaseImagePullSecret != nil {
		secretFields[mosc.Spec.BaseImagePullSecret.Name] = "baseImagePullSecret"
	}

	for secretName, fieldName := range secretFields {
		if err := validateSecret(secretGetter, mosc, secretName); err != nil {
			return fmt.Errorf("could not validate %s %q for MachineOSConfig %s: %w", fieldName, secretName, mosc.Name, err)
		}
	}

	return nil
}

func validateSecret(secretGetter func(string) (*corev1.Secret, error), mosc *mcfgv1.MachineOSConfig, secretName string) error {
	if secretName == "" {
		return fmt.Errorf("no secret name provided")
	}

	secret, err := secretGetter(secretName)

	if err != nil && k8serrors.IsNotFound(err) {
		return fmt.Errorf("secret %s from %s is not found. Did you use the right secret name?", secretName, mosc.Name)
	}

	if err != nil {
		return fmt.Errorf("could not get secret %s for MachineOSConfig %s: %w", secretName, mosc.Name, err)
	}

	return secrets.ValidateKubernetesImageRegistrySecret(secret)
}
