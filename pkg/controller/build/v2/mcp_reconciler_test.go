package v2

import (
	"context"
	"testing"

	mcfgv1 "github.com/openshift/api/machineconfiguration/v1"
	fakeclientmachineconfigv1 "github.com/openshift/client-go/machineconfiguration/clientset/versioned/fake"
	mcfglistersv1 "github.com/openshift/client-go/machineconfiguration/listers/machineconfiguration/v1"
	"github.com/openshift/machine-config-operator/pkg/apihelpers"
	"github.com/openshift/machine-config-operator/pkg/controller/build/constants"
	"github.com/openshift/machine-config-operator/pkg/controller/build/events"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	fakecorev1client "k8s.io/client-go/kubernetes/fake"
	"k8s.io/client-go/tools/cache"
	"k8s.io/client-go/tools/record"
	"k8s.io/client-go/util/workqueue"
)

// --- Fixture ---

type testMCPReconcilerFixture struct {
	reconciler  *mcpReconciler
	mcfgclient  *fakeclientmachineconfigv1.Clientset
	moscQueue   workqueue.TypedRateLimitingInterface[string]
	mosbIndexer cache.Indexer
	moscIndexer cache.Indexer
	mcpIndexer  cache.Indexer
	mcIndexer   cache.Indexer
}

func newTestMCPReconcilerFixture(t *testing.T) *testMCPReconcilerFixture {
	t.Helper()

	mcfgclient := fakeclientmachineconfigv1.NewSimpleClientset()
	kubeclient := fakecorev1client.NewSimpleClientset()

	mosbIndexer := cache.NewIndexer(cache.MetaNamespaceKeyFunc, cache.Indexers{})
	moscIndexer := cache.NewIndexer(cache.MetaNamespaceKeyFunc, cache.Indexers{})
	mcpIndexer := cache.NewIndexer(cache.MetaNamespaceKeyFunc, cache.Indexers{})
	mcIndexer := cache.NewIndexer(cache.MetaNamespaceKeyFunc, cache.Indexers{})

	l := &listers{
		machineOSBuildLister:    mcfglistersv1.NewMachineOSBuildLister(mosbIndexer),
		machineOSConfigLister:   mcfglistersv1.NewMachineOSConfigLister(moscIndexer),
		machineConfigPoolLister: mcfglistersv1.NewMachineConfigPoolLister(mcpIndexer),
		machineConfigLister:     mcfglistersv1.NewMachineConfigLister(mcIndexer),
	}

	statusMgr := NewMCPStatusManager(mcfgclient, l)

	fakeRecorder := record.NewFakeRecorder(100)
	eventRecorder := events.NewOCLEventRecorder(fakeRecorder)

	moscQueue := workqueue.NewTypedRateLimitingQueueWithConfig(
		workqueue.DefaultTypedControllerRateLimiter[string](),
		workqueue.TypedRateLimitingQueueConfig[string]{Name: "test-mosc-queue"},
	)

	r := newMCPReconciler(mcfgclient, kubeclient, l, statusMgr, eventRecorder, moscQueue)

	return &testMCPReconcilerFixture{
		reconciler:  r,
		mcfgclient:  mcfgclient,
		moscQueue:   moscQueue,
		mosbIndexer: mosbIndexer,
		moscIndexer: moscIndexer,
		mcpIndexer:  mcpIndexer,
		mcIndexer:   mcIndexer,
	}
}

func (f *testMCPReconcilerFixture) addMCP(t *testing.T, mcp *mcfgv1.MachineConfigPool) {
	t.Helper()
	f.mcpIndexer.Add(mcp)
	_, err := f.mcfgclient.MachineconfigurationV1().MachineConfigPools().Create(context.Background(), mcp, metav1.CreateOptions{})
	require.NoError(t, err)
}

func (f *testMCPReconcilerFixture) addMOSC(t *testing.T, mosc *mcfgv1.MachineOSConfig) {
	t.Helper()
	f.moscIndexer.Add(mosc)
}

func (f *testMCPReconcilerFixture) addMC(t *testing.T, mc *mcfgv1.MachineConfig) {
	t.Helper()
	f.mcIndexer.Add(mc)
}

func mcpWithConfigs(name, statusConfig, specConfig string) *mcfgv1.MachineConfigPool {
	return &mcfgv1.MachineConfigPool{
		ObjectMeta: metav1.ObjectMeta{Name: name},
		Spec: mcfgv1.MachineConfigPoolSpec{
			Configuration: mcfgv1.MachineConfigPoolStatusConfiguration{
				ObjectReference: corev1.ObjectReference{Name: specConfig},
			},
		},
		Status: mcfgv1.MachineConfigPoolStatus{
			Configuration: mcfgv1.MachineConfigPoolStatusConfiguration{
				ObjectReference: corev1.ObjectReference{Name: statusConfig},
			},
		},
	}
}

func moscForPool(name, poolName string) *mcfgv1.MachineOSConfig {
	return &mcfgv1.MachineOSConfig{
		ObjectMeta: metav1.ObjectMeta{Name: name},
		Spec: mcfgv1.MachineOSConfigSpec{
			MachineConfigPool: mcfgv1.MachineConfigPoolReference{Name: poolName},
		},
	}
}

func moscForPoolWithAnnotation(name, poolName string) *mcfgv1.MachineOSConfig {
	mosc := moscForPool(name, poolName)
	mosc.Annotations = map[string]string{
		constants.CurrentMachineOSBuildAnnotationKey: "some-build",
	}
	return mosc
}

// --- MCPNotFound ---

func TestMCPSync_MCPNotFound_NoOp(t *testing.T) {
	t.Parallel()

	f := newTestMCPReconcilerFixture(t)

	err := f.reconciler.Sync(context.Background(), "nonexistent")
	require.NoError(t, err)
	assert.Equal(t, 0, f.moscQueue.Len())
}

// --- No MOSC for pool ---

func TestMCPSync_NoMOSC_NoOp(t *testing.T) {
	t.Parallel()

	f := newTestMCPReconcilerFixture(t)

	mcp := mcpWithConfigs("worker", "rendered-worker-1", "rendered-worker-1")
	f.addMCP(t, mcp)

	err := f.reconciler.Sync(context.Background(), "worker")
	require.NoError(t, err)
	assert.Equal(t, 0, f.moscQueue.Len())
}

// --- Install time ---

func TestMCPSync_EmptyStatusConfigName_NoOp(t *testing.T) {
	t.Parallel()

	f := newTestMCPReconcilerFixture(t)

	mcp := mcpWithConfigs("worker", "", "rendered-worker-1")
	f.addMCP(t, mcp)
	f.addMOSC(t, moscForPool("worker-os-config", "worker"))

	err := f.reconciler.Sync(context.Background(), "worker")
	require.NoError(t, err)
	assert.Equal(t, 0, f.moscQueue.Len())
}

func TestMCPSync_EmptySpecConfigName_NoOp(t *testing.T) {
	t.Parallel()

	f := newTestMCPReconcilerFixture(t)

	mcp := mcpWithConfigs("worker", "rendered-worker-1", "")
	f.addMCP(t, mcp)
	f.addMOSC(t, moscForPool("worker-os-config", "worker"))

	err := f.reconciler.Sync(context.Background(), "worker")
	require.NoError(t, err)
	assert.Equal(t, 0, f.moscQueue.Len())
}

// --- No config change ---

func TestMCPSync_NoConfigChange_WithAnnotation_NoOp(t *testing.T) {
	t.Parallel()

	f := newTestMCPReconcilerFixture(t)

	mcp := mcpWithConfigs("worker", "rendered-worker-1", "rendered-worker-1")
	f.addMCP(t, mcp)
	f.addMOSC(t, moscForPoolWithAnnotation("worker-os-config", "worker"))

	err := f.reconciler.Sync(context.Background(), "worker")
	require.NoError(t, err)
	assert.Equal(t, 0, f.moscQueue.Len(), "should not enqueue when config unchanged and annotation present")
}

func TestMCPSync_NoConfigChange_NoAnnotation_EnqueuesMOSC(t *testing.T) {
	t.Parallel()

	f := newTestMCPReconcilerFixture(t)

	mcp := mcpWithConfigs("worker", "rendered-worker-1", "rendered-worker-1")
	f.addMCP(t, mcp)
	f.addMOSC(t, moscForPool("worker-os-config", "worker"))

	err := f.reconciler.Sync(context.Background(), "worker")
	require.NoError(t, err)
	assert.Equal(t, 1, f.moscQueue.Len(), "should enqueue MOSC when no current build annotation")
}

// --- Config change ---

func TestMCPSync_ConfigChanged_EnqueuesMOSC(t *testing.T) {
	t.Parallel()

	f := newTestMCPReconcilerFixture(t)

	mcp := mcpWithConfigs("worker", "rendered-worker-1", "rendered-worker-2")
	f.addMCP(t, mcp)
	f.addMOSC(t, moscForPoolWithAnnotation("worker-os-config", "worker"))
	f.addMC(t, testMCForReconciler("rendered-worker-1"))
	f.addMC(t, testMCForReconciler("rendered-worker-2"))

	err := f.reconciler.Sync(context.Background(), "worker")
	require.NoError(t, err)
	assert.Equal(t, 1, f.moscQueue.Len(), "should enqueue MOSC on config change")
}

// --- Pre-built seeding pending ---

func TestMCPSync_PreBuiltSeedingPending_Skips(t *testing.T) {
	t.Parallel()

	f := newTestMCPReconcilerFixture(t)

	mcp := mcpWithConfigs("worker", "rendered-worker-1", "rendered-worker-2")
	f.addMCP(t, mcp)

	mosc := moscForPool("worker-os-config", "worker")
	mosc.Annotations = map[string]string{
		constants.PreBuiltImageAnnotationKey: "registry.example.com/image@sha256:abc",
	}
	f.addMOSC(t, mosc)

	err := f.reconciler.Sync(context.Background(), "worker")
	require.NoError(t, err)
	assert.Equal(t, 0, f.moscQueue.Len(), "should not enqueue when pre-built seeding pending")
}

// --- SetMCPBuildability ---

func TestSetMCPBuildability_ExactlyOneMOSC_Buildable(t *testing.T) {
	t.Parallel()

	f := newTestMCPReconcilerFixture(t)

	mcp := mcpWithConfigs("worker", "rendered-worker-1", "rendered-worker-1")
	f.addMCP(t, mcp)
	f.addMOSC(t, moscForPool("worker-os-config", "worker"))

	err := f.reconciler.SetMCPBuildability(context.Background(), mcp)
	require.NoError(t, err)

	updated, getErr := f.mcfgclient.MachineconfigurationV1().MachineConfigPools().Get(
		context.Background(), "worker", metav1.GetOptions{})
	require.NoError(t, getErr)
	assert.True(t, apihelpers.IsMachineConfigPoolConditionFalse(
		updated.Status.Conditions, mcfgv1.MachineConfigPoolImageBuildDegraded),
		"should set ImageBuildDegraded=False when exactly 1 MOSC")
}

func TestSetMCPBuildability_ZeroMOSCs_NotBuildable(t *testing.T) {
	t.Parallel()

	f := newTestMCPReconcilerFixture(t)

	mcp := mcpWithConfigs("worker", "rendered-worker-1", "rendered-worker-1")
	f.addMCP(t, mcp)
	// No MOSC added.

	err := f.reconciler.SetMCPBuildability(context.Background(), mcp)
	require.NoError(t, err)

	updated, getErr := f.mcfgclient.MachineconfigurationV1().MachineConfigPools().Get(
		context.Background(), "worker", metav1.GetOptions{})
	require.NoError(t, getErr)
	assert.True(t, apihelpers.IsMachineConfigPoolConditionTrue(
		updated.Status.Conditions, mcfgv1.MachineConfigPoolImageBuildDegraded),
		"should set ImageBuildDegraded=True when 0 MOSCs")

	cond := apihelpers.GetMachineConfigPoolCondition(updated.Status, mcfgv1.MachineConfigPoolImageBuildDegraded)
	require.NotNil(t, cond)
	assert.Equal(t, "NoMOSC", cond.Reason)
}

func TestSetMCPBuildability_MultipleMOSCs_NotBuildable(t *testing.T) {
	t.Parallel()

	f := newTestMCPReconcilerFixture(t)

	mcp := mcpWithConfigs("worker", "rendered-worker-1", "rendered-worker-1")
	f.addMCP(t, mcp)
	f.addMOSC(t, moscForPool("worker-os-config-1", "worker"))
	f.addMOSC(t, moscForPool("worker-os-config-2", "worker"))

	err := f.reconciler.SetMCPBuildability(context.Background(), mcp)
	require.NoError(t, err)

	updated, getErr := f.mcfgclient.MachineconfigurationV1().MachineConfigPools().Get(
		context.Background(), "worker", metav1.GetOptions{})
	require.NoError(t, getErr)
	assert.True(t, apihelpers.IsMachineConfigPoolConditionTrue(
		updated.Status.Conditions, mcfgv1.MachineConfigPoolImageBuildDegraded),
		"should set ImageBuildDegraded=True when >1 MOSCs")

	cond := apihelpers.GetMachineConfigPoolCondition(updated.Status, mcfgv1.MachineConfigPoolImageBuildDegraded)
	require.NotNil(t, cond)
	assert.Equal(t, "MultipleMOSC", cond.Reason)
	assert.Contains(t, cond.Message, "2")
}

// --- MarkMCPBuildable / MarkMCPNotBuildable ---

func TestMCPReconciler_MarkMCPBuildable_SetsConditionFalse(t *testing.T) {
	t.Parallel()

	f := newTestMCPReconcilerFixture(t)

	mcp := mcpWithConfigs("worker", "rendered-worker-1", "rendered-worker-1")
	// Start with degraded.
	cond := apihelpers.NewMachineConfigPoolCondition(mcfgv1.MachineConfigPoolImageBuildDegraded,
		corev1.ConditionTrue, "PreviousFailure", "old error")
	apihelpers.SetMachineConfigPoolCondition(&mcp.Status, *cond)
	f.addMCP(t, mcp)

	err := f.reconciler.MarkMCPBuildable(context.Background(), mcp, "Recovered", "all good")
	require.NoError(t, err)

	updated, getErr := f.mcfgclient.MachineconfigurationV1().MachineConfigPools().Get(
		context.Background(), "worker", metav1.GetOptions{})
	require.NoError(t, getErr)
	assert.True(t, apihelpers.IsMachineConfigPoolConditionFalse(
		updated.Status.Conditions, mcfgv1.MachineConfigPoolImageBuildDegraded))
}

func TestMCPReconciler_MarkMCPNotBuildable_SetsConditionTrue(t *testing.T) {
	t.Parallel()

	f := newTestMCPReconcilerFixture(t)

	mcp := mcpWithConfigs("worker", "rendered-worker-1", "rendered-worker-1")
	f.addMCP(t, mcp)

	err := f.reconciler.MarkMCPNotBuildable(context.Background(), mcp, "TestFail", "broken")
	require.NoError(t, err)

	updated, getErr := f.mcfgclient.MachineconfigurationV1().MachineConfigPools().Get(
		context.Background(), "worker", metav1.GetOptions{})
	require.NoError(t, getErr)
	assert.True(t, apihelpers.IsMachineConfigPoolConditionTrue(
		updated.Status.Conditions, mcfgv1.MachineConfigPoolImageBuildDegraded))
}

// --- getMOSCCountForPool ---

func TestGetMOSCCountForPool(t *testing.T) {
	t.Parallel()

	f := newTestMCPReconcilerFixture(t)

	f.addMOSC(t, moscForPool("mosc-worker-1", "worker"))
	f.addMOSC(t, moscForPool("mosc-worker-2", "worker"))
	f.addMOSC(t, moscForPool("mosc-infra-1", "infra"))

	assert.Equal(t, 2, f.reconciler.getMOSCCountForPool("worker"))
	assert.Equal(t, 1, f.reconciler.getMOSCCountForPool("infra"))
	assert.Equal(t, 0, f.reconciler.getMOSCCountForPool("nonexistent"))
}
