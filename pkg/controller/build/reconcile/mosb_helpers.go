package reconcile

import (
	"context"
	"fmt"

	mcfgv1 "github.com/openshift/api/machineconfiguration/v1"
	mcfgclientset "github.com/openshift/client-go/machineconfiguration/clientset/versioned"
	mcfglistersv1 "github.com/openshift/client-go/machineconfiguration/listers/machineconfiguration/v1"
	"github.com/openshift/machine-config-operator/pkg/controller/build/buildrequest"
	k8serrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/klog/v2"
)

// buildDesiredMOSBFromListers constructs a desired MachineOSBuild for the
// given MachineOSConfig by looking up the current MCP and rendered MC via
// listers. This is the shared implementation used by both MOSCReconciler
// and PoolReconciler to avoid duplicating the MOSB construction logic.
func buildDesiredMOSBFromListers(
	mosc *mcfgv1.MachineOSConfig,
	mcpLister mcfglistersv1.MachineConfigPoolLister,
	mcLister mcfglistersv1.MachineConfigLister,
) (*mcfgv1.MachineOSBuild, error) {
	mcp, err := mcpLister.Get(mosc.Spec.MachineConfigPool.Name)
	if err != nil {
		return nil, fmt.Errorf("could not get MCP %q: %w", mosc.Spec.MachineConfigPool.Name, err)
	}
	mc, err := mcLister.Get(mcp.Spec.Configuration.Name)
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

// createMachineOSBuildForMOSC creates a new MachineOSBuild owned by the given
// MachineOSConfig. It tolerates AlreadyExists errors. This is the shared
// implementation used by both MOSCReconciler and PoolReconciler.
func createMachineOSBuildForMOSC(
	ctx context.Context,
	mcfgclient mcfgclientset.Interface,
	mosb *mcfgv1.MachineOSBuild,
	mosc *mcfgv1.MachineOSConfig,
) error {
	oref := metav1.NewControllerRef(mosc, mcfgv1.SchemeGroupVersion.WithKind("MachineOSConfig"))
	mosb.SetOwnerReferences([]metav1.OwnerReference{*oref})

	_, err := mcfgclient.MachineconfigurationV1().MachineOSBuilds().Create(ctx, mosb, metav1.CreateOptions{})
	if err != nil {
		if k8serrors.IsAlreadyExists(err) {
			klog.Infof("MachineOSBuild %q already exists, skipping creation", mosb.Name)
			return nil
		}
		return fmt.Errorf("could not create MachineOSBuild %q: %w", mosb.Name, err)
	}

	klog.Infof("Created MachineOSBuild %q for MachineOSConfig %q", mosb.Name, mosc.Name)
	return nil
}
