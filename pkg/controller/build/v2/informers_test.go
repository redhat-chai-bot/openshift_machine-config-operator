package v2

import (
	"testing"

	fakeclientmachineconfigv1 "github.com/openshift/client-go/machineconfiguration/clientset/versioned/fake"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	fakecorev1client "k8s.io/client-go/kubernetes/fake"
)

func TestNewInformers_CreatesAllInformers(t *testing.T) {
	t.Parallel()

	mcfgclient := fakeclientmachineconfigv1.NewSimpleClientset()
	kubeclient := fakecorev1client.NewSimpleClientset()

	inf := newInformers(mcfgclient, kubeclient)

	require.NotNil(t, inf)
	assert.NotNil(t, inf.controllerConfigInformer)
	assert.NotNil(t, inf.machineConfigPoolInformer)
	assert.NotNil(t, inf.machineConfigInformer)
	assert.NotNil(t, inf.jobInformer)
	assert.NotNil(t, inf.machineOSBuildInformer)
	assert.NotNil(t, inf.machineOSConfigInformer)
	assert.NotNil(t, inf.nodeInformer)
	assert.NotNil(t, inf.configmapInformer)
	assert.NotNil(t, inf.secretInformer)
	assert.Len(t, inf.toStart, 3, "expected 3 informer factories to start")
	assert.Len(t, inf.hasSynced, 9, "expected 9 has-synced funcs")
}

func TestListers_ReturnExpectedObjects(t *testing.T) {
	t.Parallel()

	mcfgclient := fakeclientmachineconfigv1.NewSimpleClientset()
	kubeclient := fakecorev1client.NewSimpleClientset()

	inf := newInformers(mcfgclient, kubeclient)
	l := inf.listers()

	require.NotNil(t, l)
	assert.NotNil(t, l.machineOSBuildLister)
	assert.NotNil(t, l.machineOSConfigLister)
	assert.NotNil(t, l.machineConfigPoolLister)
	assert.NotNil(t, l.machineConfigLister)
	assert.NotNil(t, l.jobLister)
	assert.NotNil(t, l.controllerConfigLister)
	assert.NotNil(t, l.nodeLister)
	assert.NotNil(t, l.configmapLister)
	assert.NotNil(t, l.secretLister)

	// utilListers should also work
	ul := l.utilListers()
	require.NotNil(t, ul)
	assert.NotNil(t, ul.MachineOSBuildLister)
	assert.NotNil(t, ul.MachineOSConfigLister)
	assert.NotNil(t, ul.MachineConfigPoolLister)
	assert.NotNil(t, ul.NodeLister)
}
