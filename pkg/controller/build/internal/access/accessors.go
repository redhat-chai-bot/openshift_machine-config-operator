// Package access provides a unified data-access container for the build
// controller's clients and informer-backed listers. Consolidating these
// into a single struct eliminates the need for multiple Listers types
// (buildrequest.Listers, utils.Listers) and makes dependency wiring
// explicit.
package access

import (
	mcfgclientset "github.com/openshift/client-go/machineconfiguration/clientset/versioned"
	mcfglistersv1 "github.com/openshift/client-go/machineconfiguration/listers/machineconfiguration/v1"
	batchlisterv1 "k8s.io/client-go/listers/batch/v1"
	clientset "k8s.io/client-go/kubernetes"
	corelistersv1 "k8s.io/client-go/listers/core/v1"
)

// Accessors bundles the Kubernetes and MCO API clients together with
// the informer-backed listers used throughout the build controller.
type Accessors struct {
	// Clients
	Kubeclient clientset.Interface
	Mcfgclient mcfgclientset.Interface

	// Core listers
	SecretLister    corelistersv1.SecretLister
	ConfigMapLister corelistersv1.ConfigMapLister
	NodeLister      corelistersv1.NodeLister

	// MCO listers
	MachineOSBuildLister    mcfglistersv1.MachineOSBuildLister
	MachineOSConfigLister   mcfglistersv1.MachineOSConfigLister
	MachineConfigPoolLister mcfglistersv1.MachineConfigPoolLister
	MachineConfigLister     mcfglistersv1.MachineConfigLister
	ControllerConfigLister  mcfglistersv1.ControllerConfigLister

	// Batch listers
	JobLister batchlisterv1.JobLister
}
