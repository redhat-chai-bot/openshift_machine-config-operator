package utils

import (
	mcfgv1 "github.com/openshift/api/machineconfiguration/v1"
	"github.com/openshift/machine-config-operator/pkg/controller/build/internal/buildlabels"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
)

// The following functions delegate to internal/buildlabels.
// They are retained so that packages outside pkg/controller/build/
// (e.g. test/e2e-ocl-shared) can continue to call them.

func GetMachineOSBuildLabels(mosc *mcfgv1.MachineOSConfig, mcp *mcfgv1.MachineConfigPool) map[string]string {
	return buildlabels.GetMachineOSBuildLabels(mosc, mcp)
}

func MachineOSBuildSelector(mosc *mcfgv1.MachineOSConfig, mcp *mcfgv1.MachineConfigPool) labels.Selector {
	return buildlabels.MachineOSBuildSelector(mosc, mcp)
}

func MachineOSBuildForPoolSelector(mosc *mcfgv1.MachineOSConfig) labels.Selector {
	return buildlabels.MachineOSBuildForPoolSelector(mosc)
}

func OSBuildSelector() labels.Selector {
	return buildlabels.OSBuildSelector()
}

func EphemeralBuildObjectSelector() labels.Selector {
	return buildlabels.EphemeralBuildObjectSelector()
}

func EphemeralBuildObjectSelectorForSpecificBuild(mosb *mcfgv1.MachineOSBuild, mosc *mcfgv1.MachineOSConfig) (labels.Selector, error) {
	return buildlabels.EphemeralBuildObjectSelectorForSpecificBuild(mosb, mosc)
}

func CanonicalizedSecretSelector() labels.Selector {
	return buildlabels.CanonicalizedSecretSelector()
}

func IsObjectCreatedByController(obj metav1.Object) bool {
	return buildlabels.IsObjectCreatedByController(obj)
}
