package v2

import (
	"context"
	"fmt"
	"testing"
	"time"

	mcfgv1 "github.com/openshift/api/machineconfiguration/v1"
	fakeclientmachineconfigv1 "github.com/openshift/client-go/machineconfiguration/clientset/versioned/fake"
	mcfglistersv1 "github.com/openshift/client-go/machineconfiguration/listers/machineconfiguration/v1"
	"github.com/openshift/machine-config-operator/pkg/apihelpers"
	"github.com/openshift/machine-config-operator/pkg/controller/build/constants"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/tools/cache"
)

// --- Test helpers ---

func newTestMCP(name string) *mcfgv1.MachineConfigPool {
	return &mcfgv1.MachineConfigPool{
		ObjectMeta: metav1.ObjectMeta{Name: name},
	}
}

func newTestMCPWithCondition(name string, condType mcfgv1.MachineConfigPoolConditionType, status corev1.ConditionStatus) *mcfgv1.MachineConfigPool {
	mcp := newTestMCP(name)
	cond := apihelpers.NewMachineConfigPoolCondition(condType, status, "test-reason", "test message")
	apihelpers.SetMachineConfigPoolCondition(&mcp.Status, *cond)
	return mcp
}

func newTestMCPStatusManager(t *testing.T, objects ...*mcfgv1.MachineConfigPool) (*MCPStatusManager, *fakeclientmachineconfigv1.Clientset) {
	t.Helper()

	mcfgclient := fakeclientmachineconfigv1.NewSimpleClientset()
	for _, mcp := range objects {
		_, err := mcfgclient.MachineconfigurationV1().MachineConfigPools().Create(context.Background(), mcp, metav1.CreateOptions{})
		require.NoError(t, err)
	}

	mcpIndexer := cache.NewIndexer(cache.MetaNamespaceKeyFunc, cache.Indexers{})
	mosbIndexer := cache.NewIndexer(cache.MetaNamespaceKeyFunc, cache.Indexers{})

	for _, mcp := range objects {
		mcpIndexer.Add(mcp)
	}

	l := &listers{
		machineConfigPoolLister: mcfglistersv1.NewMachineConfigPoolLister(mcpIndexer),
		machineOSBuildLister:    mcfglistersv1.NewMachineOSBuildLister(mosbIndexer),
	}

	return NewMCPStatusManager(mcfgclient, l), mcfgclient
}

func newTestMCPStatusManagerWithMOSBs(t *testing.T, mcp *mcfgv1.MachineConfigPool, mosbs []*mcfgv1.MachineOSBuild, mosc *mcfgv1.MachineOSConfig) (*MCPStatusManager, *fakeclientmachineconfigv1.Clientset) {
	t.Helper()

	mcfgclient := fakeclientmachineconfigv1.NewSimpleClientset()
	_, err := mcfgclient.MachineconfigurationV1().MachineConfigPools().Create(context.Background(), mcp, metav1.CreateOptions{})
	require.NoError(t, err)

	mcpIndexer := cache.NewIndexer(cache.MetaNamespaceKeyFunc, cache.Indexers{})
	mosbIndexer := cache.NewIndexer(cache.MetaNamespaceKeyFunc, cache.Indexers{})
	moscIndexer := cache.NewIndexer(cache.MetaNamespaceKeyFunc, cache.Indexers{})

	mcpIndexer.Add(mcp)

	if mosc != nil {
		moscIndexer.Add(mosc)
	}

	for _, mosb := range mosbs {
		mosbIndexer.Add(mosb)
	}

	l := &listers{
		machineConfigPoolLister: mcfglistersv1.NewMachineConfigPoolLister(mcpIndexer),
		machineOSBuildLister:    mcfglistersv1.NewMachineOSBuildLister(mosbIndexer),
		machineOSConfigLister:   mcfglistersv1.NewMachineOSConfigLister(moscIndexer),
	}

	return NewMCPStatusManager(mcfgclient, l), mcfgclient
}

func getMCPFromClient(t *testing.T, mcfgclient *fakeclientmachineconfigv1.Clientset, name string) *mcfgv1.MachineConfigPool {
	t.Helper()
	mcp, err := mcfgclient.MachineconfigurationV1().MachineConfigPools().Get(context.Background(), name, metav1.GetOptions{})
	require.NoError(t, err)
	return mcp
}

// --- SetBuildStarted tests ---

func TestSetBuildStarted_ClearsDegradedCondition(t *testing.T) {
	t.Parallel()

	// MCP starts with ImageBuildDegraded=True (previous failure).
	mcp := newTestMCPWithCondition("worker", mcfgv1.MachineConfigPoolImageBuildDegraded, corev1.ConditionTrue)
	mgr, mcfgclient := newTestMCPStatusManager(t, mcp)

	err := mgr.SetBuildStarted(context.Background(), mcp)
	require.NoError(t, err)

	updated := getMCPFromClient(t, mcfgclient, "worker")
	assert.True(t, apihelpers.IsMachineConfigPoolConditionFalse(updated.Status.Conditions, mcfgv1.MachineConfigPoolImageBuildDegraded))

	cond := apihelpers.GetMachineConfigPoolCondition(updated.Status, mcfgv1.MachineConfigPoolImageBuildDegraded)
	require.NotNil(t, cond)
	assert.Equal(t, string(mcfgv1.MachineConfigPoolBuilding), cond.Reason)
}

func TestSetBuildStarted_AlreadyClear_NoOp(t *testing.T) {
	t.Parallel()

	// MCP already has ImageBuildDegraded=False with Building reason.
	mcp := newTestMCPWithCondition("worker", mcfgv1.MachineConfigPoolImageBuildDegraded, corev1.ConditionFalse)
	cond := apihelpers.GetMachineConfigPoolCondition(mcp.Status, mcfgv1.MachineConfigPoolImageBuildDegraded)
	cond.Reason = string(mcfgv1.MachineConfigPoolBuilding)

	mgr, _ := newTestMCPStatusManager(t, mcp)

	err := mgr.SetBuildStarted(context.Background(), mcp)
	require.NoError(t, err)
	// Should complete without error (no-op path).
}

// --- SetBuildSucceeded tests ---

func TestSetBuildSucceeded_ClearsDegradedCondition(t *testing.T) {
	t.Parallel()

	mcp := newTestMCPWithCondition("worker", mcfgv1.MachineConfigPoolImageBuildDegraded, corev1.ConditionTrue)
	mgr, mcfgclient := newTestMCPStatusManager(t, mcp)

	err := mgr.SetBuildSucceeded(context.Background(), mcp)
	require.NoError(t, err)

	updated := getMCPFromClient(t, mcfgclient, "worker")
	assert.True(t, apihelpers.IsMachineConfigPoolConditionFalse(updated.Status.Conditions, mcfgv1.MachineConfigPoolImageBuildDegraded))

	cond := apihelpers.GetMachineConfigPoolCondition(updated.Status, mcfgv1.MachineConfigPoolImageBuildDegraded)
	require.NotNil(t, cond)
	assert.Equal(t, string(mcfgv1.MachineConfigPoolBuildSuccess), cond.Reason)
}

// --- SetBuildFailed tests ---

func TestSetBuildFailed_SetsDegradedCondition(t *testing.T) {
	t.Parallel()

	mcp := newTestMCP("worker")
	mgr, mcfgclient := newTestMCPStatusManager(t, mcp)

	buildErr := fmt.Errorf("image push failed: timeout")
	err := mgr.SetBuildFailed(context.Background(), mcp, buildErr, "worker-build-abc")

	// Should return the original build error.
	require.Error(t, err)
	assert.Contains(t, err.Error(), "image push failed: timeout")

	updated := getMCPFromClient(t, mcfgclient, "worker")
	assert.True(t, apihelpers.IsMachineConfigPoolConditionTrue(updated.Status.Conditions, mcfgv1.MachineConfigPoolImageBuildDegraded))
}

func TestSetBuildFailed_IncludesErrorMessageAndMOSBName(t *testing.T) {
	t.Parallel()

	mcp := newTestMCP("worker")
	mgr, mcfgclient := newTestMCPStatusManager(t, mcp)

	buildErr := fmt.Errorf("containerfile syntax error")
	_ = mgr.SetBuildFailed(context.Background(), mcp, buildErr, "worker-build-xyz")

	updated := getMCPFromClient(t, mcfgclient, "worker")
	cond := apihelpers.GetMachineConfigPoolCondition(updated.Status, mcfgv1.MachineConfigPoolImageBuildDegraded)
	require.NotNil(t, cond)
	assert.Contains(t, cond.Message, "worker-build-xyz")
	assert.Contains(t, cond.Message, "containerfile syntax error")
	assert.Equal(t, string(mcfgv1.MachineConfigPoolBuildFailed), cond.Reason)
}

func TestSetBuildFailed_ReturnsBuildError(t *testing.T) {
	t.Parallel()

	mcp := newTestMCP("worker")
	mgr, _ := newTestMCPStatusManager(t, mcp)

	buildErr := fmt.Errorf("specific build failure")
	err := mgr.SetBuildFailed(context.Background(), mcp, buildErr, "build-1")

	require.Error(t, err)
	assert.Equal(t, "specific build failure", err.Error())
}

// --- ShouldPreventBuild tests ---

func TestShouldPreventBuild_TrueForNodeDegraded(t *testing.T) {
	t.Parallel()

	mcp := newTestMCPWithCondition("worker", mcfgv1.MachineConfigPoolNodeDegraded, corev1.ConditionTrue)
	mgr, _ := newTestMCPStatusManager(t, mcp)
	assert.True(t, mgr.ShouldPreventBuild(mcp))
}

func TestShouldPreventBuild_TrueForRenderDegraded(t *testing.T) {
	t.Parallel()

	mcp := newTestMCPWithCondition("worker", mcfgv1.MachineConfigPoolRenderDegraded, corev1.ConditionTrue)
	mgr, _ := newTestMCPStatusManager(t, mcp)
	assert.True(t, mgr.ShouldPreventBuild(mcp))
}

func TestShouldPreventBuild_TrueForPinnedImageSetsDegraded(t *testing.T) {
	t.Parallel()

	mcp := newTestMCPWithCondition("worker", mcfgv1.MachineConfigPoolPinnedImageSetsDegraded, corev1.ConditionTrue)
	mgr, _ := newTestMCPStatusManager(t, mcp)
	assert.True(t, mgr.ShouldPreventBuild(mcp))
}

func TestShouldPreventBuild_TrueForSynchronizerDegraded(t *testing.T) {
	t.Parallel()

	mcp := newTestMCPWithCondition("worker", mcfgv1.MachineConfigPoolSynchronizerDegraded, corev1.ConditionTrue)
	mgr, _ := newTestMCPStatusManager(t, mcp)
	assert.True(t, mgr.ShouldPreventBuild(mcp))
}

func TestShouldPreventBuild_FalseForBuildDegradedOnly(t *testing.T) {
	t.Parallel()

	// Pool degraded ONLY due to ImageBuildDegraded — should still be able to retry.
	mcp := newTestMCPWithCondition("worker", mcfgv1.MachineConfigPoolImageBuildDegraded, corev1.ConditionTrue)
	mgr, _ := newTestMCPStatusManager(t, mcp)
	assert.False(t, mgr.ShouldPreventBuild(mcp))
}

func TestShouldPreventBuild_FalseForHealthyPool(t *testing.T) {
	t.Parallel()

	mcp := newTestMCP("worker")
	mgr, _ := newTestMCPStatusManager(t, mcp)
	assert.False(t, mgr.ShouldPreventBuild(mcp))
}

// --- UpdateFromActiveBuild tests ---

func TestUpdateFromActiveBuild_FailedBuild_SetsDegraded(t *testing.T) {
	t.Parallel()

	mcp := newTestMCP("worker")
	mosc := &mcfgv1.MachineOSConfig{
		ObjectMeta: metav1.ObjectMeta{
			Name: "worker-os-config",
			Annotations: map[string]string{
				constants.CurrentMachineOSBuildAnnotationKey: "worker-build-1",
			},
		},
	}

	failedMOSB := &mcfgv1.MachineOSBuild{
		ObjectMeta: metav1.ObjectMeta{
			Name: "worker-build-1",
			Labels: map[string]string{
				constants.MachineOSConfigNameLabelKey: "worker-os-config",
			},
		},
		Status: mcfgv1.MachineOSBuildStatus{
			Conditions: apihelpers.MachineOSBuildFailedConditions(),
		},
	}

	mgr, mcfgclient := newTestMCPStatusManagerWithMOSBs(t, mcp, []*mcfgv1.MachineOSBuild{failedMOSB}, mosc)

	err := mgr.UpdateFromActiveBuild(context.Background(), mcp, mosc)
	// SetBuildFailed returns the build error.
	require.Error(t, err)

	updated := getMCPFromClient(t, mcfgclient, "worker")
	assert.True(t, apihelpers.IsMachineConfigPoolConditionTrue(updated.Status.Conditions, mcfgv1.MachineConfigPoolImageBuildDegraded))
}

func TestUpdateFromActiveBuild_SuccessfulBuild_ClearsDegraded(t *testing.T) {
	t.Parallel()

	mcp := newTestMCPWithCondition("worker", mcfgv1.MachineConfigPoolImageBuildDegraded, corev1.ConditionTrue)
	mosc := &mcfgv1.MachineOSConfig{
		ObjectMeta: metav1.ObjectMeta{
			Name: "worker-os-config",
			Annotations: map[string]string{
				constants.CurrentMachineOSBuildAnnotationKey: "worker-build-1",
			},
		},
	}

	succeededMOSB := &mcfgv1.MachineOSBuild{
		ObjectMeta: metav1.ObjectMeta{
			Name: "worker-build-1",
			Labels: map[string]string{
				constants.MachineOSConfigNameLabelKey: "worker-os-config",
			},
		},
		Status: mcfgv1.MachineOSBuildStatus{
			Conditions: apihelpers.MachineOSBuildSucceededConditions(),
		},
	}

	mgr, mcfgclient := newTestMCPStatusManagerWithMOSBs(t, mcp, []*mcfgv1.MachineOSBuild{succeededMOSB}, mosc)

	err := mgr.UpdateFromActiveBuild(context.Background(), mcp, mosc)
	require.NoError(t, err)

	updated := getMCPFromClient(t, mcfgclient, "worker")
	assert.True(t, apihelpers.IsMachineConfigPoolConditionFalse(updated.Status.Conditions, mcfgv1.MachineConfigPoolImageBuildDegraded))
}

func TestUpdateFromActiveBuild_InProgressBuild_ClearsDegraded(t *testing.T) {
	t.Parallel()

	mcp := newTestMCPWithCondition("worker", mcfgv1.MachineConfigPoolImageBuildDegraded, corev1.ConditionTrue)
	mosc := &mcfgv1.MachineOSConfig{
		ObjectMeta: metav1.ObjectMeta{
			Name: "worker-os-config",
		},
	}

	buildingMOSB := &mcfgv1.MachineOSBuild{
		ObjectMeta: metav1.ObjectMeta{
			Name: "worker-build-1",
			Labels: map[string]string{
				constants.MachineOSConfigNameLabelKey: "worker-os-config",
			},
		},
		Status: mcfgv1.MachineOSBuildStatus{
			Conditions: apihelpers.MachineOSBuildRunningConditions(),
		},
	}

	mgr, mcfgclient := newTestMCPStatusManagerWithMOSBs(t, mcp, []*mcfgv1.MachineOSBuild{buildingMOSB}, mosc)

	err := mgr.UpdateFromActiveBuild(context.Background(), mcp, mosc)
	require.NoError(t, err)

	updated := getMCPFromClient(t, mcfgclient, "worker")
	assert.True(t, apihelpers.IsMachineConfigPoolConditionFalse(updated.Status.Conditions, mcfgv1.MachineConfigPoolImageBuildDegraded))
}

func TestUpdateFromActiveBuild_NoBuilds_ClearsDegraded(t *testing.T) {
	t.Parallel()

	mcp := newTestMCPWithCondition("worker", mcfgv1.MachineConfigPoolImageBuildDegraded, corev1.ConditionTrue)
	mosc := &mcfgv1.MachineOSConfig{
		ObjectMeta: metav1.ObjectMeta{
			Name: "worker-os-config",
		},
	}

	// No MOSBs at all.
	mgr, mcfgclient := newTestMCPStatusManagerWithMOSBs(t, mcp, nil, mosc)

	err := mgr.UpdateFromActiveBuild(context.Background(), mcp, mosc)
	require.NoError(t, err)

	updated := getMCPFromClient(t, mcfgclient, "worker")
	assert.True(t, apihelpers.IsMachineConfigPoolConditionFalse(updated.Status.Conditions, mcfgv1.MachineConfigPoolImageBuildDegraded))
}

// --- MarkMCPBuildable / MarkMCPNotBuildable tests ---

func TestMarkMCPBuildable_SetsConditionFalse(t *testing.T) {
	t.Parallel()

	mcp := newTestMCPWithCondition("worker", mcfgv1.MachineConfigPoolImageBuildDegraded, corev1.ConditionTrue)
	mgr, mcfgclient := newTestMCPStatusManager(t, mcp)

	err := mgr.MarkMCPBuildable(context.Background(), mcp, "TestReason", "Test message")
	require.NoError(t, err)

	updated := getMCPFromClient(t, mcfgclient, "worker")
	assert.True(t, apihelpers.IsMachineConfigPoolConditionFalse(updated.Status.Conditions, mcfgv1.MachineConfigPoolImageBuildDegraded))
}

func TestMarkMCPNotBuildable_SetsConditionTrue(t *testing.T) {
	t.Parallel()

	mcp := newTestMCP("worker")
	mgr, mcfgclient := newTestMCPStatusManager(t, mcp)

	err := mgr.MarkMCPNotBuildable(context.Background(), mcp, "FailReason", "Something broke")
	require.NoError(t, err)

	updated := getMCPFromClient(t, mcfgclient, "worker")
	assert.True(t, apihelpers.IsMachineConfigPoolConditionTrue(updated.Status.Conditions, mcfgv1.MachineConfigPoolImageBuildDegraded))

	cond := apihelpers.GetMachineConfigPoolCondition(updated.Status, mcfgv1.MachineConfigPoolImageBuildDegraded)
	require.NotNil(t, cond)
	assert.Equal(t, "FailReason", cond.Reason)
	assert.Equal(t, "Something broke", cond.Message)
}

// --- getCurrentBuild tests ---

func TestGetCurrentBuild_PrefersAnnotationMatch(t *testing.T) {
	t.Parallel()

	mosc := &mcfgv1.MachineOSConfig{
		ObjectMeta: metav1.ObjectMeta{
			Name: "worker-os-config",
			Annotations: map[string]string{
				constants.CurrentMachineOSBuildAnnotationKey: "build-2",
			},
		},
	}

	mosbs := []*mcfgv1.MachineOSBuild{
		{
			ObjectMeta: metav1.ObjectMeta{
				Name:              "build-1",
				CreationTimestamp:  metav1.NewTime(time.Now().Add(-2 * time.Hour)),
			},
			Status: mcfgv1.MachineOSBuildStatus{Conditions: apihelpers.MachineOSBuildRunningConditions()},
		},
		{
			ObjectMeta: metav1.ObjectMeta{
				Name:              "build-2",
				CreationTimestamp:  metav1.NewTime(time.Now().Add(-1 * time.Hour)),
			},
			Status: mcfgv1.MachineOSBuildStatus{Conditions: apihelpers.MachineOSBuildSucceededConditions()},
		},
	}

	result := getCurrentBuild(mosc, mosbs)
	require.NotNil(t, result)
	assert.Equal(t, "build-2", result.Name)
}

func TestGetCurrentBuild_FallsBackToTransient(t *testing.T) {
	t.Parallel()

	mosc := &mcfgv1.MachineOSConfig{
		ObjectMeta: metav1.ObjectMeta{Name: "worker-os-config"},
	}

	mosbs := []*mcfgv1.MachineOSBuild{
		{
			ObjectMeta: metav1.ObjectMeta{
				Name:              "build-old",
				CreationTimestamp:  metav1.NewTime(time.Now().Add(-2 * time.Hour)),
			},
			Status: mcfgv1.MachineOSBuildStatus{Conditions: apihelpers.MachineOSBuildSucceededConditions()},
		},
		{
			ObjectMeta: metav1.ObjectMeta{
				Name:              "build-active",
				CreationTimestamp:  metav1.NewTime(time.Now().Add(-1 * time.Hour)),
			},
			Status: mcfgv1.MachineOSBuildStatus{Conditions: apihelpers.MachineOSBuildRunningConditions()},
		},
	}

	result := getCurrentBuild(mosc, mosbs)
	require.NotNil(t, result)
	assert.Equal(t, "build-active", result.Name)
}

func TestGetCurrentBuild_FallsBackToMostRecent(t *testing.T) {
	t.Parallel()

	mosc := &mcfgv1.MachineOSConfig{
		ObjectMeta: metav1.ObjectMeta{Name: "worker-os-config"},
	}

	mosbs := []*mcfgv1.MachineOSBuild{
		{
			ObjectMeta: metav1.ObjectMeta{
				Name:              "build-old",
				CreationTimestamp:  metav1.NewTime(time.Now().Add(-2 * time.Hour)),
			},
			Status: mcfgv1.MachineOSBuildStatus{Conditions: apihelpers.MachineOSBuildSucceededConditions()},
		},
		{
			ObjectMeta: metav1.ObjectMeta{
				Name:              "build-newer",
				CreationTimestamp:  metav1.NewTime(time.Now().Add(-30 * time.Minute)),
			},
			Status: mcfgv1.MachineOSBuildStatus{Conditions: apihelpers.MachineOSBuildFailedConditions()},
		},
	}

	result := getCurrentBuild(mosc, mosbs)
	require.NotNil(t, result)
	assert.Equal(t, "build-newer", result.Name)
}

func TestGetCurrentBuild_ReturnsNilForEmptyList(t *testing.T) {
	t.Parallel()

	mosc := &mcfgv1.MachineOSConfig{
		ObjectMeta: metav1.ObjectMeta{Name: "worker-os-config"},
	}

	result := getCurrentBuild(mosc, nil)
	assert.Nil(t, result)
}
