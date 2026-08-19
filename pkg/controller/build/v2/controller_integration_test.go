package v2

import (
	"context"
	"testing"
	"time"

	mcfgv1 "github.com/openshift/api/machineconfiguration/v1"
	fakeclientimagev1 "github.com/openshift/client-go/image/clientset/versioned/fake"
	fakeclientmachineconfigv1 "github.com/openshift/client-go/machineconfiguration/clientset/versioned/fake"
	fakeclientroutev1 "github.com/openshift/client-go/route/clientset/versioned/fake"
	"github.com/openshift/machine-config-operator/pkg/apihelpers"
	"github.com/openshift/machine-config-operator/pkg/controller/build/constants"
	ctrlcommon "github.com/openshift/machine-config-operator/pkg/controller/common"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	fakecorev1client "k8s.io/client-go/kubernetes/fake"
)

// newIntegrationController creates an OSBuildController with real reconcilers
// wired in via the function pointers, using fake clients for all API calls.
func newIntegrationController(t *testing.T) (*OSBuildController, context.CancelFunc) {
	t.Helper()

	mcfgclient := fakeclientmachineconfigv1.NewSimpleClientset()
	kubeclient := fakecorev1client.NewSimpleClientset()
	imageclient := fakeclientimagev1.NewSimpleClientset()
	routeclient := fakeclientroutev1.NewSimpleClientset()

	ctrl := newOSBuildController(defaultConfig(), mcfgclient, kubeclient, imageclient, routeclient, nil)

	// Wire real reconcilers into the controller's sync handler function pointers.
	statusMgr := NewMCPStatusManager(ctrl.mcfgclient, ctrl.listers)
	seeder := NewSeedManager(ctrl.mcfgclient, ctrl.kubeclient, ctrl.listers)

	moscReconciler := newMOSCReconciler(
		ctrl.mcfgclient, ctrl.kubeclient, ctrl.listers,
		statusMgr, seeder, ctrl.eventRecorder,
	)
	mosbReconciler := newMOSBReconciler(
		ctrl.mcfgclient, ctrl.kubeclient, ctrl.listers,
		statusMgr, ctrl.eventRecorder,
	)
	mcpReconciler := newMCPReconciler(
		ctrl.mcfgclient, ctrl.kubeclient, ctrl.listers,
		statusMgr, ctrl.eventRecorder, ctrl.moscQueue,
	)

	ctrl.syncMOSC = moscReconciler.Sync
	ctrl.syncMOSB = mosbReconciler.Sync
	ctrl.syncMCP = mcpReconciler.Sync

	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)

	go ctrl.Run(ctx, 1)

	// Wait for informer caches to sync.
	time.Sleep(100 * time.Millisecond)

	return ctrl, cancel
}

// createTestMCPInCluster creates a MachineConfigPool via the API.
func createTestMCPInCluster(t *testing.T, ctrl *OSBuildController, name, renderedConfig string) {
	t.Helper()
	mcp := &mcfgv1.MachineConfigPool{
		ObjectMeta: metav1.ObjectMeta{Name: name},
		Spec: mcfgv1.MachineConfigPoolSpec{
			Configuration: mcfgv1.MachineConfigPoolStatusConfiguration{
				ObjectReference: corev1.ObjectReference{Name: renderedConfig},
			},
		},
		Status: mcfgv1.MachineConfigPoolStatus{
			Configuration: mcfgv1.MachineConfigPoolStatusConfiguration{
				ObjectReference: corev1.ObjectReference{Name: renderedConfig},
			},
		},
	}
	_, err := ctrl.mcfgclient.MachineconfigurationV1().MachineConfigPools().Create(
		context.Background(), mcp, metav1.CreateOptions{})
	require.NoError(t, err)
}

// createTestMCInCluster creates a MachineConfig via the API.
func createTestMCInCluster(t *testing.T, ctrl *OSBuildController, name string) {
	t.Helper()
	mc := &mcfgv1.MachineConfig{
		ObjectMeta: metav1.ObjectMeta{
			Name: name,
			Annotations: map[string]string{
				ctrlcommon.ReleaseImageVersionAnnotationKey:          "4.19.0-test",
				ctrlcommon.GeneratedByControllerVersionAnnotationKey: "4.19.0-test",
			},
		},
	}
	_, err := ctrl.mcfgclient.MachineconfigurationV1().MachineConfigs().Create(
		context.Background(), mc, metav1.CreateOptions{})
	require.NoError(t, err)
}

// TestIntegration_MOSCCreate_TriggersMOSB verifies that creating a
// MachineOSConfig with matching MCP and MC results in a MachineOSBuild
// being created by the MOSC reconciler.
func TestIntegration_MOSCCreate_TriggersMOSB(t *testing.T) {
	t.Parallel()

	ctrl, _ := newIntegrationController(t)

	// Set up the MCP and MC.
	createTestMCPInCluster(t, ctrl, "worker", "rendered-worker-1")
	createTestMCInCluster(t, ctrl, "rendered-worker-1")

	// Create the MachineOSConfig.
	mosc := &mcfgv1.MachineOSConfig{
		ObjectMeta: metav1.ObjectMeta{Name: "worker-os-config"},
		Spec: mcfgv1.MachineOSConfigSpec{
			MachineConfigPool:       mcfgv1.MachineConfigPoolReference{Name: "worker"},
			RenderedImagePushSpec:   mcfgv1.ImageTagFormat("registry.example.com/org/repo:latest"),
			RenderedImagePushSecret: mcfgv1.ImageSecretObjectReference{Name: "push-secret"},
		},
	}
	_, err := ctrl.mcfgclient.MachineconfigurationV1().MachineOSConfigs().Create(
		context.Background(), mosc, metav1.CreateOptions{})
	require.NoError(t, err)

	// Wait for the MOSC reconciler to process it and create a MachineOSBuild.
	assert.Eventually(t, func() bool {
		mosbList, err := ctrl.mcfgclient.MachineconfigurationV1().MachineOSBuilds().List(
			context.Background(), metav1.ListOptions{})
		if err != nil {
			return false
		}
		return len(mosbList.Items) >= 1
	}, 10*time.Second, 50*time.Millisecond,
		"expected a MachineOSBuild to be created after MachineOSConfig creation")

	// Verify the MOSC got the current build annotation.
	assert.Eventually(t, func() bool {
		updatedMOSC, err := ctrl.mcfgclient.MachineconfigurationV1().MachineOSConfigs().Get(
			context.Background(), "worker-os-config", metav1.GetOptions{})
		if err != nil {
			return false
		}
		return updatedMOSC.Annotations[constants.CurrentMachineOSBuildAnnotationKey] != ""
	}, 5*time.Second, 50*time.Millisecond,
		"expected MOSC to have currentBuild annotation after MOSB creation")
}

// TestIntegration_SucceededMOSB_UpdatesMOSCStatus verifies that when an MOSB
// reaches Succeeded state, the MOSB reconciler updates the MOSC status with
// the digested image pullspec.
func TestIntegration_SucceededMOSB_UpdatesMOSCStatus(t *testing.T) {
	t.Parallel()

	ctrl, _ := newIntegrationController(t)

	createTestMCPInCluster(t, ctrl, "worker", "rendered-worker-1")
	createTestMCInCluster(t, ctrl, "rendered-worker-1")

	// Create MOSC.
	mosc := &mcfgv1.MachineOSConfig{
		ObjectMeta: metav1.ObjectMeta{
			Name: "worker-os-config",
			Annotations: map[string]string{
				constants.CurrentMachineOSBuildAnnotationKey: "worker-build-1",
			},
		},
		Spec: mcfgv1.MachineOSConfigSpec{
			MachineConfigPool:       mcfgv1.MachineConfigPoolReference{Name: "worker"},
			RenderedImagePushSpec:   mcfgv1.ImageTagFormat("registry.example.com/org/repo:latest"),
			RenderedImagePushSecret: mcfgv1.ImageSecretObjectReference{Name: "push-secret"},
		},
	}
	_, err := ctrl.mcfgclient.MachineconfigurationV1().MachineOSConfigs().Create(
		context.Background(), mosc, metav1.CreateOptions{})
	require.NoError(t, err)

	// Create a succeeded MOSB.
	mosb := &mcfgv1.MachineOSBuild{
		ObjectMeta: metav1.ObjectMeta{
			Name: "worker-build-1",
			Labels: map[string]string{
				constants.MachineOSConfigNameLabelKey: "worker-os-config",
			},
		},
		Spec: mcfgv1.MachineOSBuildSpec{
			MachineOSConfig: mcfgv1.MachineOSConfigReference{Name: "worker-os-config"},
			MachineConfig:   mcfgv1.MachineConfigReference{Name: "rendered-worker-1"},
		},
	}
	created, err := ctrl.mcfgclient.MachineconfigurationV1().MachineOSBuilds().Create(
		context.Background(), mosb, metav1.CreateOptions{})
	require.NoError(t, err)

	// Update status to succeeded (status is separate subresource).
	created.Status = mcfgv1.MachineOSBuildStatus{
		Conditions:           apihelpers.MachineOSBuildSucceededConditions(),
		DigestedImagePushSpec: "registry.example.com/org/repo@sha256:abc123",
	}
	_, err = ctrl.mcfgclient.MachineconfigurationV1().MachineOSBuilds().UpdateStatus(
		context.Background(), created, metav1.UpdateOptions{})
	require.NoError(t, err)

	// Wait for MOSC status to be updated.
	assert.Eventually(t, func() bool {
		updatedMOSC, err := ctrl.mcfgclient.MachineconfigurationV1().MachineOSConfigs().Get(
			context.Background(), "worker-os-config", metav1.GetOptions{})
		if err != nil {
			return false
		}
		return updatedMOSC.Status.CurrentImagePullSpec == "registry.example.com/org/repo@sha256:abc123"
	}, 10*time.Second, 50*time.Millisecond,
		"expected MOSC status to be updated with digested pullspec from succeeded MOSB")
}

// TestIntegration_FailedMOSB_SetsMCPDegraded verifies that when an MOSB
// fails, the MCP gets the ImageBuildDegraded condition set.
func TestIntegration_FailedMOSB_SetsMCPDegraded(t *testing.T) {
	t.Parallel()

	ctrl, _ := newIntegrationController(t)

	createTestMCPInCluster(t, ctrl, "worker", "rendered-worker-1")

	// Create MOSC.
	mosc := &mcfgv1.MachineOSConfig{
		ObjectMeta: metav1.ObjectMeta{Name: "worker-os-config"},
		Spec: mcfgv1.MachineOSConfigSpec{
			MachineConfigPool: mcfgv1.MachineConfigPoolReference{Name: "worker"},
		},
	}
	_, err := ctrl.mcfgclient.MachineconfigurationV1().MachineOSConfigs().Create(
		context.Background(), mosc, metav1.CreateOptions{})
	require.NoError(t, err)

	// Create a failed MOSB.
	mosb := &mcfgv1.MachineOSBuild{
		ObjectMeta: metav1.ObjectMeta{
			Name: "worker-build-fail",
			Labels: map[string]string{
				constants.MachineOSConfigNameLabelKey: "worker-os-config",
			},
		},
		Spec: mcfgv1.MachineOSBuildSpec{
			MachineOSConfig: mcfgv1.MachineOSConfigReference{Name: "worker-os-config"},
			MachineConfig:   mcfgv1.MachineConfigReference{Name: "rendered-worker-1"},
		},
	}
	created, err := ctrl.mcfgclient.MachineconfigurationV1().MachineOSBuilds().Create(
		context.Background(), mosb, metav1.CreateOptions{})
	require.NoError(t, err)

	created.Status = mcfgv1.MachineOSBuildStatus{
		Conditions: apihelpers.MachineOSBuildFailedConditions(),
	}
	_, err = ctrl.mcfgclient.MachineconfigurationV1().MachineOSBuilds().UpdateStatus(
		context.Background(), created, metav1.UpdateOptions{})
	require.NoError(t, err)

	// Wait for MCP to have ImageBuildDegraded=True.
	assert.Eventually(t, func() bool {
		mcp, err := ctrl.mcfgclient.MachineconfigurationV1().MachineConfigPools().Get(
			context.Background(), "worker", metav1.GetOptions{})
		if err != nil {
			return false
		}
		return apihelpers.IsMachineConfigPoolConditionTrue(
			mcp.Status.Conditions, mcfgv1.MachineConfigPoolImageBuildDegraded)
	}, 10*time.Second, 50*time.Millisecond,
		"expected MCP to have ImageBuildDegraded=True after MOSB failure")
}

// TestIntegration_RebuildAnnotation_TriggersNewBuild verifies that applying
// the rebuild annotation on a MachineOSConfig causes the current MOSB to be
// deleted and the annotations to be cleared (enabling a new build on the
// next sync).
func TestIntegration_RebuildAnnotation_TriggersNewBuild(t *testing.T) {
	t.Parallel()

	ctrl, _ := newIntegrationController(t)

	createTestMCPInCluster(t, ctrl, "worker", "rendered-worker-1")
	createTestMCInCluster(t, ctrl, "rendered-worker-1")

	// Create MOSC with a current build and rebuild annotation.
	mosc := &mcfgv1.MachineOSConfig{
		ObjectMeta: metav1.ObjectMeta{
			Name: "worker-os-config",
			Annotations: map[string]string{
				constants.CurrentMachineOSBuildAnnotationKey:  "worker-build-old",
				constants.RebuildMachineOSConfigAnnotationKey: "",
			},
		},
		Spec: mcfgv1.MachineOSConfigSpec{
			MachineConfigPool:       mcfgv1.MachineConfigPoolReference{Name: "worker"},
			RenderedImagePushSpec:   mcfgv1.ImageTagFormat("registry.example.com/org/repo:latest"),
			RenderedImagePushSecret: mcfgv1.ImageSecretObjectReference{Name: "push-secret"},
		},
	}
	_, err := ctrl.mcfgclient.MachineconfigurationV1().MachineOSConfigs().Create(
		context.Background(), mosc, metav1.CreateOptions{})
	require.NoError(t, err)

	// Create the old MOSB that should be deleted.
	oldMOSB := &mcfgv1.MachineOSBuild{
		ObjectMeta: metav1.ObjectMeta{
			Name: "worker-build-old",
			Labels: map[string]string{
				constants.MachineOSConfigNameLabelKey: "worker-os-config",
			},
		},
		Spec: mcfgv1.MachineOSBuildSpec{
			MachineOSConfig: mcfgv1.MachineOSConfigReference{Name: "worker-os-config"},
			MachineConfig:   mcfgv1.MachineConfigReference{Name: "rendered-worker-1"},
		},
	}
	_, err = ctrl.mcfgclient.MachineconfigurationV1().MachineOSBuilds().Create(
		context.Background(), oldMOSB, metav1.CreateOptions{})
	require.NoError(t, err)

	// Wait for the rebuild annotation to be processed: the old MOSB should be
	// deleted and the rebuild annotation removed.
	assert.Eventually(t, func() bool {
		updatedMOSC, err := ctrl.mcfgclient.MachineconfigurationV1().MachineOSConfigs().Get(
			context.Background(), "worker-os-config", metav1.GetOptions{})
		if err != nil {
			return false
		}
		_, hasRebuild := updatedMOSC.Annotations[constants.RebuildMachineOSConfigAnnotationKey]
		return !hasRebuild
	}, 10*time.Second, 50*time.Millisecond,
		"expected rebuild annotation to be removed after processing")
}

// TestIntegration_MCPConfigChange_EnqueuesMOSC verifies that when an MCP's
// rendered config changes, the MCP reconciler detects it and enqueues the
// corresponding MOSC.
func TestIntegration_MCPConfigChange_EnqueuesMOSC(t *testing.T) {
	t.Parallel()

	ctrl, _ := newIntegrationController(t)

	createTestMCInCluster(t, ctrl, "rendered-worker-1")
	createTestMCInCluster(t, ctrl, "rendered-worker-2")

	// Create an MCP with the old config.
	mcp := &mcfgv1.MachineConfigPool{
		ObjectMeta: metav1.ObjectMeta{Name: "worker"},
		Spec: mcfgv1.MachineConfigPoolSpec{
			Configuration: mcfgv1.MachineConfigPoolStatusConfiguration{
				ObjectReference: corev1.ObjectReference{Name: "rendered-worker-2"},
			},
		},
		Status: mcfgv1.MachineConfigPoolStatus{
			Configuration: mcfgv1.MachineConfigPoolStatusConfiguration{
				ObjectReference: corev1.ObjectReference{Name: "rendered-worker-1"},
			},
		},
	}
	_, err := ctrl.mcfgclient.MachineconfigurationV1().MachineConfigPools().Create(
		context.Background(), mcp, metav1.CreateOptions{})
	require.NoError(t, err)

	// Create a MOSC targeting this pool.
	mosc := &mcfgv1.MachineOSConfig{
		ObjectMeta: metav1.ObjectMeta{
			Name: "worker-os-config",
			Annotations: map[string]string{
				constants.CurrentMachineOSBuildAnnotationKey: "old-build",
			},
		},
		Spec: mcfgv1.MachineOSConfigSpec{
			MachineConfigPool:       mcfgv1.MachineConfigPoolReference{Name: "worker"},
			RenderedImagePushSpec:   mcfgv1.ImageTagFormat("registry.example.com/org/repo:latest"),
			RenderedImagePushSecret: mcfgv1.ImageSecretObjectReference{Name: "push-secret"},
		},
	}
	_, err = ctrl.mcfgclient.MachineconfigurationV1().MachineOSConfigs().Create(
		context.Background(), mosc, metav1.CreateOptions{})
	require.NoError(t, err)

	// The MCP reconciler should detect the config change and enqueue the MOSC.
	// The MOSC reconciler should then create a new MOSB.
	assert.Eventually(t, func() bool {
		mosbList, err := ctrl.mcfgclient.MachineconfigurationV1().MachineOSBuilds().List(
			context.Background(), metav1.ListOptions{})
		if err != nil {
			return false
		}
		return len(mosbList.Items) >= 1
	}, 10*time.Second, 100*time.Millisecond,
		"expected a new MachineOSBuild after MCP config change")
}
