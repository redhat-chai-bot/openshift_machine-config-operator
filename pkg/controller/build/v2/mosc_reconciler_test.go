package v2

import (
	"context"
	"testing"

	mcfgv1 "github.com/openshift/api/machineconfiguration/v1"
	fakeclientmachineconfigv1 "github.com/openshift/client-go/machineconfiguration/clientset/versioned/fake"
	mcfglistersv1 "github.com/openshift/client-go/machineconfiguration/listers/machineconfiguration/v1"
	"github.com/openshift/machine-config-operator/pkg/apihelpers"
	"github.com/openshift/machine-config-operator/pkg/controller/build/buildrequest"
	"github.com/openshift/machine-config-operator/pkg/controller/build/constants"
	"github.com/openshift/machine-config-operator/pkg/controller/build/events"
	ctrlcommon "github.com/openshift/machine-config-operator/pkg/controller/common"
	k8serrors "k8s.io/apimachinery/pkg/api/errors"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	fakecorev1client "k8s.io/client-go/kubernetes/fake"
	corelistersv1 "k8s.io/client-go/listers/core/v1"
	"k8s.io/client-go/tools/cache"
	"k8s.io/client-go/tools/record"
)

// --- Fixture ---

type testMOSCReconcilerFixture struct {
	reconciler   *moscReconciler
	mcfgclient   *fakeclientmachineconfigv1.Clientset
	kubeclient   *fakecorev1client.Clientset
	mosbIndexer  cache.Indexer
	moscIndexer  cache.Indexer
	mcpIndexer   cache.Indexer
	mcIndexer    cache.Indexer
	secretIndexer cache.Indexer
}

func newTestMOSCReconcilerFixture(t *testing.T) *testMOSCReconcilerFixture {
	t.Helper()

	mcfgclient := fakeclientmachineconfigv1.NewSimpleClientset()
	kubeclient := fakecorev1client.NewSimpleClientset()

	mosbIndexer := cache.NewIndexer(cache.MetaNamespaceKeyFunc, cache.Indexers{})
	moscIndexer := cache.NewIndexer(cache.MetaNamespaceKeyFunc, cache.Indexers{})
	mcpIndexer := cache.NewIndexer(cache.MetaNamespaceKeyFunc, cache.Indexers{})
	mcIndexer := cache.NewIndexer(cache.MetaNamespaceKeyFunc, cache.Indexers{})
	secretIndexer := cache.NewIndexer(cache.MetaNamespaceKeyFunc, cache.Indexers{})

	l := &listers{
		machineOSBuildLister:    mcfglistersv1.NewMachineOSBuildLister(mosbIndexer),
		machineOSConfigLister:   mcfglistersv1.NewMachineOSConfigLister(moscIndexer),
		machineConfigPoolLister: mcfglistersv1.NewMachineConfigPoolLister(mcpIndexer),
		machineConfigLister:     mcfglistersv1.NewMachineConfigLister(mcIndexer),
		secretLister:            corelistersv1.NewSecretLister(secretIndexer),
	}

	statusMgr := NewMCPStatusManager(mcfgclient, l)
	seeder := NewSeedManager(mcfgclient, kubeclient, l)

	fakeRecorder := record.NewFakeRecorder(100)
	eventRecorder := events.NewOCLEventRecorder(fakeRecorder)

	r := newMOSCReconciler(mcfgclient, kubeclient, l, statusMgr, seeder, eventRecorder)

	return &testMOSCReconcilerFixture{
		reconciler:    r,
		mcfgclient:    mcfgclient,
		kubeclient:    kubeclient,
		mosbIndexer:   mosbIndexer,
		moscIndexer:   moscIndexer,
		mcpIndexer:    mcpIndexer,
		mcIndexer:     mcIndexer,
		secretIndexer: secretIndexer,
	}
}

func (f *testMOSCReconcilerFixture) addMOSC(t *testing.T, mosc *mcfgv1.MachineOSConfig) {
	t.Helper()
	f.moscIndexer.Add(mosc)
	_, err := f.mcfgclient.MachineconfigurationV1().MachineOSConfigs().Create(context.Background(), mosc, metav1.CreateOptions{})
	require.NoError(t, err)
}

func (f *testMOSCReconcilerFixture) addMOSB(t *testing.T, mosb *mcfgv1.MachineOSBuild) {
	t.Helper()
	f.mosbIndexer.Add(mosb)
	created, err := f.mcfgclient.MachineconfigurationV1().MachineOSBuilds().Create(context.Background(), mosb, metav1.CreateOptions{})
	require.NoError(t, err)
	if len(mosb.Status.Conditions) > 0 || mosb.Status.DigestedImagePushSpec != "" {
		created.Status = mosb.Status
		_, err = f.mcfgclient.MachineconfigurationV1().MachineOSBuilds().UpdateStatus(context.Background(), created, metav1.UpdateOptions{})
		require.NoError(t, err)
	}
}

func (f *testMOSCReconcilerFixture) addMCP(t *testing.T, mcp *mcfgv1.MachineConfigPool) {
	t.Helper()
	f.mcpIndexer.Add(mcp)
	_, err := f.mcfgclient.MachineconfigurationV1().MachineConfigPools().Create(context.Background(), mcp, metav1.CreateOptions{})
	require.NoError(t, err)
}

func (f *testMOSCReconcilerFixture) addMC(t *testing.T, mc *mcfgv1.MachineConfig) {
	t.Helper()
	f.mcIndexer.Add(mc)
	_, err := f.mcfgclient.MachineconfigurationV1().MachineConfigs().Create(context.Background(), mc, metav1.CreateOptions{})
	require.NoError(t, err)
}

func (f *testMOSCReconcilerFixture) addSecret(t *testing.T, name string) {
	t.Helper()
	secret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: ctrlcommon.MCONamespace,
		},
		Type: corev1.SecretTypeDockerConfigJson,
		Data: map[string][]byte{
			corev1.DockerConfigJsonKey: []byte(`{"auths":{"registry.example.com":{"auth":"dGVzdDp0ZXN0"}}}`),
		},
	}
	f.secretIndexer.Add(secret)
	_, err := f.kubeclient.CoreV1().Secrets(ctrlcommon.MCONamespace).Create(context.Background(), secret, metav1.CreateOptions{})
	require.NoError(t, err)
}

// --- MOSC not found (deletion) ---

func TestMOSCSync_NotFound_NoMOSBs_NoOp(t *testing.T) {
	t.Parallel()

	f := newTestMOSCReconcilerFixture(t)

	err := f.reconciler.Sync(context.Background(), "nonexistent-mosc")
	require.NoError(t, err)
}

func TestMOSCSync_NotFound_DeletesAssociatedMOSBs(t *testing.T) {
	t.Parallel()

	f := newTestMOSCReconcilerFixture(t)

	// Add an MOSB labeled for this MOSC.
	mosb := testMOSB("worker-build-1", "deleted-mosc", "rendered-worker-1")
	f.addMOSB(t, mosb)

	err := f.reconciler.Sync(context.Background(), "deleted-mosc")
	require.NoError(t, err)

	// The MOSB should be deleted.
	_, getErr := f.mcfgclient.MachineconfigurationV1().MachineOSBuilds().Get(
		context.Background(), "worker-build-1", metav1.GetOptions{})
	assert.True(t, k8serrors.IsNotFound(getErr))
}

// --- Seeding ---

func TestMOSCSync_SeedingComplete_CleansUpAnnotation(t *testing.T) {
	t.Parallel()

	f := newTestMOSCReconcilerFixture(t)

	mosc := &mcfgv1.MachineOSConfig{
		ObjectMeta: metav1.ObjectMeta{
			Name: "worker-os-config",
			Annotations: map[string]string{
				constants.PreBuiltImageAnnotationKey:         "registry.example.com/image@sha256:abc",
				constants.CurrentMachineOSBuildAnnotationKey: "build-1",
			},
		},
		Spec: mcfgv1.MachineOSConfigSpec{
			MachineConfigPool: mcfgv1.MachineConfigPoolReference{Name: "worker"},
		},
		Status: mcfgv1.MachineOSConfigStatus{
			CurrentImagePullSpec: "registry.example.com/image@sha256:abc",
		},
	}
	f.addMOSC(t, mosc)

	err := f.reconciler.Sync(context.Background(), "worker-os-config")
	require.NoError(t, err)

	// The pre-built image annotation should be removed.
	updated, getErr := f.mcfgclient.MachineconfigurationV1().MachineOSConfigs().Get(
		context.Background(), "worker-os-config", metav1.GetOptions{})
	require.NoError(t, getErr)
	_, hasAnnotation := updated.Annotations[constants.PreBuiltImageAnnotationKey]
	assert.False(t, hasAnnotation, "pre-built image annotation should be removed after cleanup")
}

// --- Rebuild annotation ---

func TestMOSCSync_RebuildAnnotation_NoCurrentBuild_NoOp(t *testing.T) {
	t.Parallel()

	f := newTestMOSCReconcilerFixture(t)

	mosc := testMOSCForReconciler("worker-os-config", "worker")
	mosc.Annotations = map[string]string{
		constants.RebuildMachineOSConfigAnnotationKey: "",
	}
	f.addMOSC(t, mosc)

	err := f.reconciler.Sync(context.Background(), "worker-os-config")
	require.NoError(t, err)
}

func TestMOSCSync_RebuildAnnotation_DeletesCurrentMOSB(t *testing.T) {
	t.Parallel()

	f := newTestMOSCReconcilerFixture(t)

	mosc := testMOSCForReconciler("worker-os-config", "worker")
	mosc.Annotations = map[string]string{
		constants.RebuildMachineOSConfigAnnotationKey: "",
		constants.CurrentMachineOSBuildAnnotationKey:  "worker-build-1",
	}
	f.addMOSC(t, mosc)

	mosb := testMOSBWithSucceeded("worker-build-1", "worker-os-config", "rendered-worker-1",
		"registry.example.com/org/repo@sha256:old")
	f.addMOSB(t, mosb)

	err := f.reconciler.Sync(context.Background(), "worker-os-config")
	require.NoError(t, err)

	// The MOSB should be deleted.
	_, getErr := f.mcfgclient.MachineconfigurationV1().MachineOSBuilds().Get(
		context.Background(), "worker-build-1", metav1.GetOptions{})
	assert.True(t, k8serrors.IsNotFound(getErr))
}

// --- Normal lifecycle ---

func TestMOSCSync_NewMOSC_CreatesMOSB(t *testing.T) {
	t.Parallel()

	f := newTestMOSCReconcilerFixture(t)

	mosc := testMOSCForReconciler("worker-os-config", "worker")
	mcp := testMCPForReconciler("worker", "rendered-worker-1")
	mc := testMCForReconciler("rendered-worker-1")

	f.addMOSC(t, mosc)
	f.addMCP(t, mcp)
	f.addMC(t, mc)

	err := f.reconciler.Sync(context.Background(), "worker-os-config")
	require.NoError(t, err)

	// A new MOSB should have been created.
	mosbList, listErr := f.mcfgclient.MachineconfigurationV1().MachineOSBuilds().List(
		context.Background(), metav1.ListOptions{})
	require.NoError(t, listErr)
	assert.GreaterOrEqual(t, len(mosbList.Items), 1, "expected at least one MachineOSBuild to be created")
}

func TestMOSCSync_NewMOSC_MCPDegraded_ReturnsError(t *testing.T) {
	t.Parallel()

	f := newTestMOSCReconcilerFixture(t)

	mosc := testMOSCForReconciler("worker-os-config", "worker")
	mcp := testMCPForReconciler("worker", "rendered-worker-1")
	// Set NodeDegraded.
	cond := apihelpers.NewMachineConfigPoolCondition(mcfgv1.MachineConfigPoolNodeDegraded,
		corev1.ConditionTrue, "NodeFailed", "node is degraded")
	apihelpers.SetMachineConfigPoolCondition(&mcp.Status, *cond)
	mc := testMCForReconciler("rendered-worker-1")

	f.addMOSC(t, mosc)
	f.addMCP(t, mcp)
	f.addMC(t, mc)

	err := f.reconciler.Sync(context.Background(), "worker-os-config")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "degraded")
}

func TestMOSCSync_ExistingSuccessfulMOSB_UpdatesMOSCStatus(t *testing.T) {
	t.Parallel()

	f := newTestMOSCReconcilerFixture(t)

	mosc := testMOSCForReconciler("worker-os-config", "worker")
	mcp := testMCPForReconciler("worker", "rendered-worker-1")
	mc := testMCForReconciler("rendered-worker-1")

	f.addMOSC(t, mosc)
	f.addMCP(t, mcp)
	f.addMC(t, mc)

	// Create the expected MOSB name (we'll add a successful MOSB with whatever name buildrequest produces).
	// Instead, create an MOSB and add it to the lister, then run sync.
	// We need to match the name that buildrequest.NewMachineOSBuild would produce.
	// For simplicity, create the MOSB via buildrequest ourselves.
	expectedMOSB, err := buildrequest.NewMachineOSBuild(buildrequest.MachineOSBuildOpts{
		MachineConfig: mc, MachineConfigPool: mcp, MachineOSConfig: mosc,
	})
	require.NoError(t, err)

	// Make it succeeded.
	expectedMOSB.Status = mcfgv1.MachineOSBuildStatus{
		Conditions:           apihelpers.MachineOSBuildSucceededConditions(),
		DigestedImagePushSpec: "registry.example.com/org/repo@sha256:abc123",
	}
	f.addMOSB(t, expectedMOSB)

	err = f.reconciler.Sync(context.Background(), "worker-os-config")
	require.NoError(t, err)

	// Verify MOSC status updated.
	updated, getErr := f.mcfgclient.MachineconfigurationV1().MachineOSConfigs().Get(
		context.Background(), "worker-os-config", metav1.GetOptions{})
	require.NoError(t, getErr)
	assert.Equal(t, mcfgv1.ImageDigestFormat("registry.example.com/org/repo@sha256:abc123"),
		updated.Status.CurrentImagePullSpec)
}

func TestMOSCSync_ExistingTransientMOSB_EnsuresAnnotation(t *testing.T) {
	t.Parallel()

	f := newTestMOSCReconcilerFixture(t)

	mosc := testMOSCForReconciler("worker-os-config", "worker")
	mcp := testMCPForReconciler("worker", "rendered-worker-1")
	mc := testMCForReconciler("rendered-worker-1")

	f.addMOSC(t, mosc)
	f.addMCP(t, mcp)
	f.addMC(t, mc)

	expectedMOSB, err := buildrequest.NewMachineOSBuild(buildrequest.MachineOSBuildOpts{
		MachineConfig: mc, MachineConfigPool: mcp, MachineOSConfig: mosc,
	})
	require.NoError(t, err)

	// Make it building.
	expectedMOSB.Status = mcfgv1.MachineOSBuildStatus{
		Conditions: apihelpers.MachineOSBuildRunningConditions(),
	}
	f.addMOSB(t, expectedMOSB)

	err = f.reconciler.Sync(context.Background(), "worker-os-config")
	require.NoError(t, err)

	// Verify the MOSC annotation was set.
	updated, getErr := f.mcfgclient.MachineconfigurationV1().MachineOSConfigs().Get(
		context.Background(), "worker-os-config", metav1.GetOptions{})
	require.NoError(t, getErr)
	assert.Equal(t, expectedMOSB.Name, updated.Annotations[constants.CurrentMachineOSBuildAnnotationKey])
}

func TestMOSCSync_ExistingFailedMOSB_NoOp(t *testing.T) {
	t.Parallel()

	f := newTestMOSCReconcilerFixture(t)

	mosc := testMOSCForReconciler("worker-os-config", "worker")
	mcp := testMCPForReconciler("worker", "rendered-worker-1")
	mc := testMCForReconciler("rendered-worker-1")

	f.addMOSC(t, mosc)
	f.addMCP(t, mcp)
	f.addMC(t, mc)

	expectedMOSB, err := buildrequest.NewMachineOSBuild(buildrequest.MachineOSBuildOpts{
		MachineConfig: mc, MachineConfigPool: mcp, MachineOSConfig: mosc,
	})
	require.NoError(t, err)

	expectedMOSB.Status = mcfgv1.MachineOSBuildStatus{
		Conditions: apihelpers.MachineOSBuildFailedConditions(),
	}
	f.addMOSB(t, expectedMOSB)

	err = f.reconciler.Sync(context.Background(), "worker-os-config")
	require.NoError(t, err) // No error — the MOSB reconciler handles failures
}

// --- Deletion cleanup ---

func TestMOSCSync_DeletesStaleNonCurrentMOSBs(t *testing.T) {
	t.Parallel()

	f := newTestMOSCReconcilerFixture(t)

	mosc := testMOSCForReconciler("worker-os-config", "worker")
	mcp := testMCPForReconciler("worker", "rendered-worker-1")
	mc := testMCForReconciler("rendered-worker-1")

	f.addMOSC(t, mosc)
	f.addMCP(t, mcp)
	f.addMC(t, mc)

	// Create the expected (current) MOSB as succeeded.
	expectedMOSB, err := buildrequest.NewMachineOSBuild(buildrequest.MachineOSBuildOpts{
		MachineConfig: mc, MachineConfigPool: mcp, MachineOSConfig: mosc,
	})
	require.NoError(t, err)
	expectedMOSB.Status = mcfgv1.MachineOSBuildStatus{
		Conditions:           apihelpers.MachineOSBuildSucceededConditions(),
		DigestedImagePushSpec: "registry.example.com/org/repo@sha256:abc123",
	}
	f.addMOSB(t, expectedMOSB)

	// Add a stale non-successful MOSB for the same MOSC.
	staleMOSB := testMOSBWithFailed("stale-build", "worker-os-config", "rendered-worker-old")
	f.addMOSB(t, staleMOSB)

	err = f.reconciler.Sync(context.Background(), "worker-os-config")
	require.NoError(t, err)

	// The stale MOSB should be deleted.
	_, getErr := f.mcfgclient.MachineconfigurationV1().MachineOSBuilds().Get(
		context.Background(), "stale-build", metav1.GetOptions{})
	assert.True(t, k8serrors.IsNotFound(getErr), "stale non-current MOSB should be deleted")
}

// --- Status updates ---

func TestMOSCSync_SetsMOSCCurrentBuildAnnotation(t *testing.T) {
	t.Parallel()

	f := newTestMOSCReconcilerFixture(t)

	mosc := testMOSCForReconciler("worker-os-config", "worker")
	mcp := testMCPForReconciler("worker", "rendered-worker-1")
	mc := testMCForReconciler("rendered-worker-1")

	f.addMOSC(t, mosc)
	f.addMCP(t, mcp)
	f.addMC(t, mc)

	err := f.reconciler.Sync(context.Background(), "worker-os-config")
	require.NoError(t, err)

	// The MOSC should have the current build annotation set.
	updated, getErr := f.mcfgclient.MachineconfigurationV1().MachineOSConfigs().Get(
		context.Background(), "worker-os-config", metav1.GetOptions{})
	require.NoError(t, getErr)
	assert.NotEmpty(t, updated.Annotations[constants.CurrentMachineOSBuildAnnotationKey])
}

// --- handleDeletion ---

func TestMOSCSync_NotFound_SkipsMOSBsOwnedByDifferentMOSC(t *testing.T) {
	t.Parallel()

	f := newTestMOSCReconcilerFixture(t)

	// Add an MOSB labeled for a DIFFERENT MOSC.
	mosb := testMOSB("other-build-1", "other-mosc", "rendered-worker-1")
	f.addMOSB(t, mosb)

	err := f.reconciler.Sync(context.Background(), "deleted-mosc")
	require.NoError(t, err)

	// The other MOSB should NOT be deleted.
	_, getErr := f.mcfgclient.MachineconfigurationV1().MachineOSBuilds().Get(
		context.Background(), "other-build-1", metav1.GetOptions{})
	require.NoError(t, getErr, "MOSB owned by different MOSC should not be deleted")
}

// --- Validate ---

func TestMOSCSync_MCPNotFound_ReturnsError(t *testing.T) {
	t.Parallel()

	f := newTestMOSCReconcilerFixture(t)

	mosc := testMOSCForReconciler("worker-os-config", "nonexistent-pool")
	f.addMOSC(t, mosc)

	err := f.reconciler.Sync(context.Background(), "worker-os-config")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "nonexistent-pool")
}

// --- handleDeletion edge: list by label selector ---

func TestHandleDeletion_DeletesByLabelSelector(t *testing.T) {
	t.Parallel()

	f := newTestMOSCReconcilerFixture(t)

	// Add two MOSBs: one for the deleted MOSC, one for a different MOSC.
	mosb1 := testMOSB("build-for-deleted", "deleted-mosc", "rendered-worker-1")
	mosb2 := testMOSB("build-for-other", "other-mosc", "rendered-worker-1")
	f.addMOSB(t, mosb1)
	f.addMOSB(t, mosb2)

	err := f.reconciler.handleDeletion(context.Background(), "deleted-mosc")
	require.NoError(t, err)

	// mosb1 should be deleted, mosb2 should remain.
	_, err1 := f.mcfgclient.MachineconfigurationV1().MachineOSBuilds().Get(
		context.Background(), "build-for-deleted", metav1.GetOptions{})
	assert.True(t, k8serrors.IsNotFound(err1))

	_, err2 := f.mcfgclient.MachineconfigurationV1().MachineOSBuilds().Get(
		context.Background(), "build-for-other", metav1.GetOptions{})
	require.NoError(t, err2)
}

// Ensure unused import is referenced
var _ = labels.Everything
