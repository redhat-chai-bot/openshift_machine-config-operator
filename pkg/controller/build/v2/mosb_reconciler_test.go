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
	ctrlcommon "github.com/openshift/machine-config-operator/pkg/controller/common"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	fakecorev1client "k8s.io/client-go/kubernetes/fake"
	"k8s.io/client-go/tools/cache"
	"k8s.io/client-go/tools/record"
)

// --- Test helpers ---

type testMOSBReconcilerFixture struct {
	reconciler *mosbReconciler
	mcfgclient *fakeclientmachineconfigv1.Clientset
	kubeclient *fakecorev1client.Clientset
	statusMgr  *MCPStatusManager
	mosbIndexer cache.Indexer
	moscIndexer cache.Indexer
	mcpIndexer  cache.Indexer
	mcIndexer   cache.Indexer
}

func newTestMOSBReconcilerFixture(t *testing.T) *testMOSBReconcilerFixture {
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

	r := newMOSBReconciler(mcfgclient, kubeclient, l, statusMgr, eventRecorder)

	return &testMOSBReconcilerFixture{
		reconciler:  r,
		mcfgclient:  mcfgclient,
		kubeclient:  kubeclient,
		statusMgr:   statusMgr,
		mosbIndexer: mosbIndexer,
		moscIndexer: moscIndexer,
		mcpIndexer:  mcpIndexer,
		mcIndexer:   mcIndexer,
	}
}

func (f *testMOSBReconcilerFixture) addMOSB(t *testing.T, mosb *mcfgv1.MachineOSBuild) {
	t.Helper()
	f.mosbIndexer.Add(mosb)
	created, err := f.mcfgclient.MachineconfigurationV1().MachineOSBuilds().Create(context.Background(), mosb, metav1.CreateOptions{})
	require.NoError(t, err)
	// Update status separately (status ignored on Create for CRDs).
	if len(mosb.Status.Conditions) > 0 || mosb.Status.DigestedImagePushSpec != "" {
		created.Status = mosb.Status
		_, err = f.mcfgclient.MachineconfigurationV1().MachineOSBuilds().UpdateStatus(context.Background(), created, metav1.UpdateOptions{})
		require.NoError(t, err)
	}
}

func (f *testMOSBReconcilerFixture) addMOSC(t *testing.T, mosc *mcfgv1.MachineOSConfig) {
	t.Helper()
	f.moscIndexer.Add(mosc)
	_, err := f.mcfgclient.MachineconfigurationV1().MachineOSConfigs().Create(context.Background(), mosc, metav1.CreateOptions{})
	require.NoError(t, err)
}

func (f *testMOSBReconcilerFixture) addMCP(t *testing.T, mcp *mcfgv1.MachineConfigPool) {
	t.Helper()
	f.mcpIndexer.Add(mcp)
	_, err := f.mcfgclient.MachineconfigurationV1().MachineConfigPools().Create(context.Background(), mcp, metav1.CreateOptions{})
	require.NoError(t, err)
}

func (f *testMOSBReconcilerFixture) addMC(t *testing.T, mc *mcfgv1.MachineConfig) {
	t.Helper()
	f.mcIndexer.Add(mc)
}

// Test object builders

func testMOSB(name, moscName, mcName string) *mcfgv1.MachineOSBuild {
	return &mcfgv1.MachineOSBuild{
		ObjectMeta: metav1.ObjectMeta{
			Name: name,
			Labels: map[string]string{
				constants.MachineOSConfigNameLabelKey: moscName,
			},
		},
		Spec: mcfgv1.MachineOSBuildSpec{
			MachineOSConfig: mcfgv1.MachineOSConfigReference{Name: moscName},
			MachineConfig:   mcfgv1.MachineConfigReference{Name: mcName},
		},
	}
}

func testMOSBWithSucceeded(name, moscName, mcName, imageSpec string) *mcfgv1.MachineOSBuild {
	mosb := testMOSB(name, moscName, mcName)
	mosb.Status = mcfgv1.MachineOSBuildStatus{
		Conditions:           apihelpers.MachineOSBuildSucceededConditions(),
		DigestedImagePushSpec: mcfgv1.ImageDigestFormat(imageSpec),
	}
	return mosb
}

func testMOSBWithFailed(name, moscName, mcName string) *mcfgv1.MachineOSBuild {
	mosb := testMOSB(name, moscName, mcName)
	mosb.Status = mcfgv1.MachineOSBuildStatus{
		Conditions: apihelpers.MachineOSBuildFailedConditions(),
	}
	return mosb
}

func testMOSBWithInterrupted(name, moscName, mcName string) *mcfgv1.MachineOSBuild {
	mosb := testMOSB(name, moscName, mcName)
	mosb.Status = mcfgv1.MachineOSBuildStatus{
		Conditions: apihelpers.MachineOSBuildInterruptedConditions(),
	}
	return mosb
}

func testMOSBWithBuilding(name, moscName, mcName string) *mcfgv1.MachineOSBuild {
	mosb := testMOSB(name, moscName, mcName)
	mosb.Status = mcfgv1.MachineOSBuildStatus{
		Conditions: apihelpers.MachineOSBuildRunningConditions(),
	}
	return mosb
}


func testMOSCForReconciler(name, poolName string) *mcfgv1.MachineOSConfig {
	return &mcfgv1.MachineOSConfig{
		ObjectMeta: metav1.ObjectMeta{
			Name: name,
		},
		Spec: mcfgv1.MachineOSConfigSpec{
			MachineConfigPool: mcfgv1.MachineConfigPoolReference{Name: poolName},
			RenderedImagePushSpec: mcfgv1.ImageTagFormat("registry.example.com/org/repo:latest"),
			RenderedImagePushSecret: mcfgv1.ImageSecretObjectReference{Name: "push-secret"},
		},
	}
}

func testMCPForReconciler(name, renderedConfig string) *mcfgv1.MachineConfigPool {
	return &mcfgv1.MachineConfigPool{
		ObjectMeta: metav1.ObjectMeta{Name: name},
		Spec: mcfgv1.MachineConfigPoolSpec{
			Configuration: mcfgv1.MachineConfigPoolStatusConfiguration{
				ObjectReference: corev1.ObjectReference{Name: renderedConfig},
			},
		},
	}
}

func testMCForReconciler(name string) *mcfgv1.MachineConfig {
	return &mcfgv1.MachineConfig{
		ObjectMeta: metav1.ObjectMeta{
			Name: name,
			Annotations: map[string]string{
				ctrlcommon.ReleaseImageVersionAnnotationKey:          "4.19.0-test",
				ctrlcommon.GeneratedByControllerVersionAnnotationKey: "4.19.0-test",
			},
		},
	}
}

// --- MOSB not found ---

func TestMOSBSync_NotFound_ReturnsNil(t *testing.T) {
	t.Parallel()

	f := newTestMOSBReconcilerFixture(t)

	err := f.reconciler.Sync(context.Background(), "nonexistent-mosb")
	require.NoError(t, err)
}

// --- Terminal states ---

func TestMOSBSync_Succeeded_UpdatesMOSCStatus(t *testing.T) {
	t.Parallel()

	f := newTestMOSBReconcilerFixture(t)

	mcp := testMCPForReconciler("worker", "rendered-worker-1")
	mosc := testMOSCForReconciler("worker-os-config", "worker")
	mosb := testMOSBWithSucceeded("worker-build-1", "worker-os-config", "rendered-worker-1",
		"registry.example.com/org/repo@sha256:abc123")

	f.addMCP(t, mcp)
	f.addMOSC(t, mosc)
	f.addMOSB(t, mosb)

	err := f.reconciler.Sync(context.Background(), "worker-build-1")
	require.NoError(t, err)

	// Verify MOSC status was updated.
	updatedMOSC, err := f.mcfgclient.MachineconfigurationV1().MachineOSConfigs().Get(
		context.Background(), "worker-os-config", metav1.GetOptions{})
	require.NoError(t, err)
	assert.Equal(t, mcfgv1.ImageDigestFormat("registry.example.com/org/repo@sha256:abc123"),
		updatedMOSC.Status.CurrentImagePullSpec)
}

func TestMOSBSync_Succeeded_ClearsMCPDegraded(t *testing.T) {
	t.Parallel()

	f := newTestMOSBReconcilerFixture(t)

	mcp := testMCPForReconciler("worker", "rendered-worker-1")
	// Start with degraded condition.
	cond := apihelpers.NewMachineConfigPoolCondition(mcfgv1.MachineConfigPoolImageBuildDegraded,
		corev1.ConditionTrue, "BuildFailed", "previous failure")
	apihelpers.SetMachineConfigPoolCondition(&mcp.Status, *cond)

	mosc := testMOSCForReconciler("worker-os-config", "worker")
	mosb := testMOSBWithSucceeded("worker-build-1", "worker-os-config", "rendered-worker-1",
		"registry.example.com/org/repo@sha256:abc123")

	f.addMCP(t, mcp)
	f.addMOSC(t, mosc)
	f.addMOSB(t, mosb)

	err := f.reconciler.Sync(context.Background(), "worker-build-1")
	require.NoError(t, err)

	// Verify MCP degraded condition was cleared.
	updatedMCP, err := f.mcfgclient.MachineconfigurationV1().MachineConfigPools().Get(
		context.Background(), "worker", metav1.GetOptions{})
	require.NoError(t, err)
	assert.True(t, apihelpers.IsMachineConfigPoolConditionFalse(
		updatedMCP.Status.Conditions, mcfgv1.MachineConfigPoolImageBuildDegraded))
}

func TestMOSBSync_Failed_SetsMCPDegraded(t *testing.T) {
	t.Parallel()

	f := newTestMOSBReconcilerFixture(t)

	mcp := testMCPForReconciler("worker", "rendered-worker-1")
	mosc := testMOSCForReconciler("worker-os-config", "worker")
	mosb := testMOSBWithFailed("worker-build-1", "worker-os-config", "rendered-worker-1")

	f.addMCP(t, mcp)
	f.addMOSC(t, mosc)
	f.addMOSB(t, mosb)

	err := f.reconciler.Sync(context.Background(), "worker-build-1")
	// SetBuildFailed returns the build error.
	require.Error(t, err)

	// Verify MCP degraded condition was set.
	updatedMCP, err := f.mcfgclient.MachineconfigurationV1().MachineConfigPools().Get(
		context.Background(), "worker", metav1.GetOptions{})
	require.NoError(t, err)
	assert.True(t, apihelpers.IsMachineConfigPoolConditionTrue(
		updatedMCP.Status.Conditions, mcfgv1.MachineConfigPoolImageBuildDegraded))
}

func TestMOSBSync_Interrupted_NoOp(t *testing.T) {
	t.Parallel()

	f := newTestMOSBReconcilerFixture(t)

	mcp := testMCPForReconciler("worker", "rendered-worker-1")
	mosc := testMOSCForReconciler("worker-os-config", "worker")
	mosb := testMOSBWithInterrupted("worker-build-1", "worker-os-config", "rendered-worker-1")

	f.addMCP(t, mcp)
	f.addMOSC(t, mosc)
	f.addMOSB(t, mosb)

	err := f.reconciler.Sync(context.Background(), "worker-build-1")
	require.NoError(t, err)
}

// --- Initial state ---

func TestMOSBSync_InitialState_MOSCNotFound_ReturnsNil(t *testing.T) {
	t.Parallel()

	f := newTestMOSBReconcilerFixture(t)

	mosb := testMOSB("worker-build-1", "nonexistent-mosc", "rendered-worker-1")
	f.addMOSB(t, mosb)

	err := f.reconciler.Sync(context.Background(), "worker-build-1")
	require.NoError(t, err)
}

func TestMOSBSync_InitialState_PreBuiltImageLabel_Skips(t *testing.T) {
	t.Parallel()

	f := newTestMOSBReconcilerFixture(t)

	mcp := testMCPForReconciler("worker", "rendered-worker-1")
	mosc := testMOSCForReconciler("worker-os-config", "worker")
	mosb := testMOSB("worker-build-1", "worker-os-config", "rendered-worker-1")
	mosb.Labels[constants.PreBuiltImageLabelKey] = constants.TrueValue

	f.addMCP(t, mcp)
	f.addMOSC(t, mosc)
	f.addMOSB(t, mosb)

	err := f.reconciler.Sync(context.Background(), "worker-build-1")
	require.NoError(t, err)
}

func TestMOSBSync_InitialState_PreBuiltImageAwaitingSeeding_Skips(t *testing.T) {
	t.Parallel()

	f := newTestMOSBReconcilerFixture(t)

	mcp := testMCPForReconciler("worker", "rendered-worker-1")
	mosc := testMOSCForReconciler("worker-os-config", "worker")
	mosc.Annotations = map[string]string{
		constants.PreBuiltImageAnnotationKey: "registry.example.com/image@sha256:abc",
	}
	mosb := testMOSB("worker-build-1", "worker-os-config", "rendered-worker-1")

	f.addMCP(t, mcp)
	f.addMOSC(t, mosc)
	f.addMOSB(t, mosb)

	err := f.reconciler.Sync(context.Background(), "worker-build-1")
	require.NoError(t, err)
}

func TestMOSBSync_InitialState_StaleMC_Skips(t *testing.T) {
	t.Parallel()

	f := newTestMOSBReconcilerFixture(t)

	// MCP points to rendered-worker-2 but MOSB targets rendered-worker-1.
	mcp := testMCPForReconciler("worker", "rendered-worker-2")
	mosc := testMOSCForReconciler("worker-os-config", "worker")
	mosb := testMOSB("worker-build-1", "worker-os-config", "rendered-worker-1")

	f.addMCP(t, mcp)
	f.addMOSC(t, mosc)
	f.addMOSB(t, mosb)

	err := f.reconciler.Sync(context.Background(), "worker-build-1")
	require.NoError(t, err)
}

func TestMOSBSync_InitialState_MCPDegraded_PreventsStart(t *testing.T) {
	t.Parallel()

	f := newTestMOSBReconcilerFixture(t)

	mcp := testMCPForReconciler("worker", "rendered-worker-1")
	// Set NodeDegraded to prevent build.
	cond := apihelpers.NewMachineConfigPoolCondition(mcfgv1.MachineConfigPoolNodeDegraded,
		corev1.ConditionTrue, "NodeFailed", "node is degraded")
	apihelpers.SetMachineConfigPoolCondition(&mcp.Status, *cond)

	mosc := testMOSCForReconciler("worker-os-config", "worker")
	mosb := testMOSB("worker-build-1", "worker-os-config", "rendered-worker-1")

	f.addMCP(t, mcp)
	f.addMOSC(t, mosc)
	f.addMOSB(t, mosb)

	err := f.reconciler.Sync(context.Background(), "worker-build-1")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "degraded")
}

// --- Transient states ---

func TestMOSBSync_Building_JobNotFound_MarksInterrupted(t *testing.T) {
	t.Parallel()

	f := newTestMOSBReconcilerFixture(t)

	mcp := testMCPForReconciler("worker", "rendered-worker-1")
	mosc := testMOSCForReconciler("worker-os-config", "worker")
	mosb := testMOSBWithBuilding("worker-build-1", "worker-os-config", "rendered-worker-1")

	f.addMCP(t, mcp)
	f.addMOSC(t, mosc)
	f.addMOSB(t, mosb)

	// No Job exists in kubeclient — the builder Exists() should return false.
	err := f.reconciler.Sync(context.Background(), "worker-build-1")
	require.NoError(t, err)

	// Verify MOSB was marked as interrupted.
	updatedMOSB, err := f.mcfgclient.MachineconfigurationV1().MachineOSBuilds().Get(
		context.Background(), "worker-build-1", metav1.GetOptions{})
	require.NoError(t, err)
	assert.True(t, apihelpers.IsMachineOSBuildConditionTrue(
		updatedMOSB.Status.Conditions, mcfgv1.MachineOSBuildInterrupted))
}

// --- Status transition tests (unit tests for isMOSBStatusUpdateNeeded) ---

func TestStatusTransition_Initial_ToTransient_Allowed(t *testing.T) {
	t.Parallel()
	old := mcfgv1.MachineOSBuildStatus{Conditions: apihelpers.MachineOSBuildInitialConditions()}
	cur := mcfgv1.MachineOSBuildStatus{Conditions: apihelpers.MachineOSBuildPendingConditions()}
	needed, _ := isMOSBStatusUpdateNeeded(old, cur)
	assert.True(t, needed)
}

func TestStatusTransition_Prepared_ToBuilding_Allowed(t *testing.T) {
	t.Parallel()
	old := mcfgv1.MachineOSBuildStatus{Conditions: apihelpers.MachineOSBuildPendingConditions()}
	cur := mcfgv1.MachineOSBuildStatus{Conditions: apihelpers.MachineOSBuildRunningConditions()}
	needed, _ := isMOSBStatusUpdateNeeded(old, cur)
	assert.True(t, needed)
}

func TestStatusTransition_Building_ToPrepared_Rejected(t *testing.T) {
	t.Parallel()
	old := mcfgv1.MachineOSBuildStatus{Conditions: apihelpers.MachineOSBuildRunningConditions()}
	cur := mcfgv1.MachineOSBuildStatus{Conditions: apihelpers.MachineOSBuildPendingConditions()}
	needed, _ := isMOSBStatusUpdateNeeded(old, cur)
	assert.False(t, needed)
}

func TestStatusTransition_Transient_ToTerminal_Allowed(t *testing.T) {
	t.Parallel()
	old := mcfgv1.MachineOSBuildStatus{Conditions: apihelpers.MachineOSBuildRunningConditions()}
	cur := mcfgv1.MachineOSBuildStatus{Conditions: apihelpers.MachineOSBuildSucceededConditions()}
	needed, _ := isMOSBStatusUpdateNeeded(old, cur)
	assert.True(t, needed)
}

func TestStatusTransition_Terminal_ToTerminal_Rejected(t *testing.T) {
	t.Parallel()
	old := mcfgv1.MachineOSBuildStatus{Conditions: apihelpers.MachineOSBuildSucceededConditions()}
	cur := mcfgv1.MachineOSBuildStatus{Conditions: apihelpers.MachineOSBuildFailedConditions()}
	needed, _ := isMOSBStatusUpdateNeeded(old, cur)
	assert.False(t, needed)
}

func TestStatusTransition_Terminal_ToTransient_Rejected(t *testing.T) {
	t.Parallel()
	old := mcfgv1.MachineOSBuildStatus{Conditions: apihelpers.MachineOSBuildFailedConditions()}
	cur := mcfgv1.MachineOSBuildStatus{Conditions: apihelpers.MachineOSBuildRunningConditions()}
	needed, _ := isMOSBStatusUpdateNeeded(old, cur)
	assert.False(t, needed)
}

// --- ensureMOSCStatus ---

func TestEnsureMOSCStatus_UpdatesPullspec(t *testing.T) {
	t.Parallel()

	f := newTestMOSBReconcilerFixture(t)

	mosc := testMOSCForReconciler("worker-os-config", "worker")
	f.addMOSC(t, mosc)

	mosb := testMOSBWithSucceeded("worker-build-1", "worker-os-config", "rendered-worker-1",
		"registry.example.com/org/repo@sha256:new")

	err := f.reconciler.ensureMOSCStatus(context.Background(), mosc, mosb)
	require.NoError(t, err)

	updated, err := f.mcfgclient.MachineconfigurationV1().MachineOSConfigs().Get(
		context.Background(), "worker-os-config", metav1.GetOptions{})
	require.NoError(t, err)
	assert.Equal(t, mcfgv1.ImageDigestFormat("registry.example.com/org/repo@sha256:new"),
		updated.Status.CurrentImagePullSpec)
}

func TestEnsureMOSCStatus_AlreadyUpToDate_NoOp(t *testing.T) {
	t.Parallel()

	f := newTestMOSBReconcilerFixture(t)

	mosc := testMOSCForReconciler("worker-os-config", "worker")
	mosc.Status.CurrentImagePullSpec = "registry.example.com/org/repo@sha256:same"
	f.addMOSC(t, mosc)

	mosb := testMOSBWithSucceeded("worker-build-1", "worker-os-config", "rendered-worker-1",
		"registry.example.com/org/repo@sha256:same")

	err := f.reconciler.ensureMOSCStatus(context.Background(), mosc, mosb)
	require.NoError(t, err)
}

// --- ensureMOSCAnnotation ---

func TestEnsureMOSCAnnotation_SetsAnnotation(t *testing.T) {
	t.Parallel()

	f := newTestMOSBReconcilerFixture(t)

	mosc := testMOSCForReconciler("worker-os-config", "worker")
	f.addMOSC(t, mosc)

	mosb := testMOSB("worker-build-1", "worker-os-config", "rendered-worker-1")

	err := f.reconciler.ensureMOSCAnnotation(context.Background(), mosc, mosb)
	require.NoError(t, err)

	updated, err := f.mcfgclient.MachineconfigurationV1().MachineOSConfigs().Get(
		context.Background(), "worker-os-config", metav1.GetOptions{})
	require.NoError(t, err)
	assert.Equal(t, "worker-build-1", updated.Annotations[constants.CurrentMachineOSBuildAnnotationKey])
}
