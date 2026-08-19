package v2

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	mcfgv1 "github.com/openshift/api/machineconfiguration/v1"
	fakeclientmachineconfigv1 "github.com/openshift/client-go/machineconfiguration/clientset/versioned/fake"
	fakeclientimagev1 "github.com/openshift/client-go/image/clientset/versioned/fake"
	fakeclientroutev1 "github.com/openshift/client-go/route/clientset/versioned/fake"
	"github.com/openshift/machine-config-operator/pkg/controller/build/constants"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	batchv1 "k8s.io/api/batch/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	fakecorev1client "k8s.io/client-go/kubernetes/fake"
)

// newTestController creates a controller for testing with fake clients.
func newTestController(t *testing.T) *OSBuildController {
	t.Helper()
	mcfgclient := fakeclientmachineconfigv1.NewSimpleClientset()
	kubeclient := fakecorev1client.NewSimpleClientset()
	imageclient := fakeclientimagev1.NewSimpleClientset()
	routeclient := fakeclientroutev1.NewSimpleClientset()
	return newOSBuildController(defaultConfig(), mcfgclient, kubeclient, imageclient, routeclient, nil)
}

func TestController_CanBeConstructed(t *testing.T) {
	t.Parallel()

	ctrl := newTestController(t)

	require.NotNil(t, ctrl)
	assert.NotNil(t, ctrl.moscQueue)
	assert.NotNil(t, ctrl.mosbQueue)
	assert.NotNil(t, ctrl.mcpQueue)
	assert.NotNil(t, ctrl.listers)
	assert.NotNil(t, ctrl.informers)
	assert.NotNil(t, ctrl.eventRecorder)
	assert.NotNil(t, ctrl.shutdownHandler)
	assert.NotNil(t, ctrl.syncMOSC)
	assert.NotNil(t, ctrl.syncMOSB)
	assert.NotNil(t, ctrl.syncMCP)
}

func TestController_RunStartsAndStopsCleanly(t *testing.T) {
	t.Parallel()

	ctrl := newTestController(t)

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})

	go func() {
		ctrl.Run(ctx, 1)
		close(done)
	}()

	// Give the controller a moment to start.
	time.Sleep(50 * time.Millisecond)

	// Cancel the context to trigger shutdown.
	cancel()

	// Wait for Run to return.
	select {
	case <-done:
		// OK
	case <-time.After(15 * time.Second):
		t.Fatal("controller did not shut down within 15 seconds")
	}

	// Verify the shutdown channel was closed.
	select {
	case <-ctrl.ShutdownChan():
		// OK
	default:
		t.Fatal("shutdownChan was not closed after Run returned")
	}
}

func TestController_MOSCEvent_EnqueuesIntoMOSCQueue(t *testing.T) {
	t.Parallel()

	ctrl := newTestController(t)

	// Track what gets synced via the MOSC sync handler.
	var syncedKey atomic.Value
	ctrl.syncMOSC = func(_ context.Context, key string) error {
		syncedKey.Store(key)
		return nil
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	go ctrl.Run(ctx, 1)
	time.Sleep(50 * time.Millisecond) // let informers sync

	// Simulate a MachineOSConfig creation.
	mosc := &mcfgv1.MachineOSConfig{
		ObjectMeta: metav1.ObjectMeta{Name: "worker-os-config"},
		Spec: mcfgv1.MachineOSConfigSpec{
			MachineConfigPool: mcfgv1.MachineConfigPoolReference{Name: "worker"},
		},
	}
	_, err := ctrl.mcfgclient.MachineconfigurationV1().MachineOSConfigs().Create(ctx, mosc, metav1.CreateOptions{})
	require.NoError(t, err)

	// Wait for the sync handler to be called.
	assert.Eventually(t, func() bool {
		v := syncedKey.Load()
		return v != nil && v.(string) == "worker-os-config"
	}, 5*time.Second, 10*time.Millisecond, "expected MOSC sync handler to be called with 'worker-os-config'")
}

func TestController_MOSBEvent_EnqueuesIntoMOSBQueue(t *testing.T) {
	t.Parallel()

	ctrl := newTestController(t)

	var syncedKey atomic.Value
	ctrl.syncMOSB = func(_ context.Context, key string) error {
		syncedKey.Store(key)
		return nil
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	go ctrl.Run(ctx, 1)
	time.Sleep(50 * time.Millisecond)

	mosb := &mcfgv1.MachineOSBuild{
		ObjectMeta: metav1.ObjectMeta{Name: "worker-build-abc123"},
	}
	_, err := ctrl.mcfgclient.MachineconfigurationV1().MachineOSBuilds().Create(ctx, mosb, metav1.CreateOptions{})
	require.NoError(t, err)

	assert.Eventually(t, func() bool {
		v := syncedKey.Load()
		return v != nil && v.(string) == "worker-build-abc123"
	}, 5*time.Second, 10*time.Millisecond, "expected MOSB sync handler to be called with 'worker-build-abc123'")
}

func TestController_JobEvent_ExtractsMOSBNameAndEnqueuesIntoMOSBQueue(t *testing.T) {
	t.Parallel()

	ctrl := newTestController(t)

	var syncedKey atomic.Value
	ctrl.syncMOSB = func(_ context.Context, key string) error {
		syncedKey.Store(key)
		return nil
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	go ctrl.Run(ctx, 1)
	time.Sleep(50 * time.Millisecond)

	// Create a Job with the MachineOSBuild name label.
	job := &batchv1.Job{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "build-job-xyz",
			Namespace: "openshift-machine-config-operator",
			Labels: map[string]string{
				constants.MachineOSBuildNameLabelKey: "worker-build-abc123",
				// Also add ephemeral labels so the informer's label selector picks it up.
				constants.EphemeralBuildObjectLabelKey:    "",
				constants.OnClusterLayeringLabelKey:       "",
				constants.RenderedMachineConfigLabelKey:   "",
				constants.TargetMachineConfigPoolLabelKey: "",
			},
		},
	}
	_, err := ctrl.kubeclient.BatchV1().Jobs("openshift-machine-config-operator").Create(ctx, job, metav1.CreateOptions{})
	require.NoError(t, err)

	assert.Eventually(t, func() bool {
		v := syncedKey.Load()
		return v != nil && v.(string) == "worker-build-abc123"
	}, 5*time.Second, 10*time.Millisecond, "expected MOSB sync handler to be called with 'worker-build-abc123' from Job event")
}

func TestController_MCPEvent_EnqueuesIntoMCPQueue(t *testing.T) {
	t.Parallel()

	ctrl := newTestController(t)

	var syncedKey atomic.Value
	ctrl.syncMCP = func(_ context.Context, key string) error {
		syncedKey.Store(key)
		return nil
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	go ctrl.Run(ctx, 1)
	time.Sleep(50 * time.Millisecond)

	mcp := &mcfgv1.MachineConfigPool{
		ObjectMeta: metav1.ObjectMeta{Name: "worker"},
	}
	_, err := ctrl.mcfgclient.MachineconfigurationV1().MachineConfigPools().Create(ctx, mcp, metav1.CreateOptions{})
	require.NoError(t, err)

	assert.Eventually(t, func() bool {
		v := syncedKey.Load()
		return v != nil && v.(string) == "worker"
	}, 5*time.Second, 10*time.Millisecond, "expected MCP sync handler to be called with 'worker'")
}

func TestController_JobWithoutMOSBLabel_DoesNotEnqueue(t *testing.T) {
	t.Parallel()

	ctrl := newTestController(t)

	syncCalled := atomic.Bool{}
	ctrl.syncMOSB = func(_ context.Context, _ string) error {
		syncCalled.Store(true)
		return nil
	}

	// Directly test the handler — don't need the full controller loop.
	job := &batchv1.Job{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "unrelated-job",
			Namespace: "openshift-machine-config-operator",
		},
	}
	ctrl.enqueueJobAsMOSB(job)

	// The queue should be empty.
	assert.Equal(t, 0, ctrl.mosbQueue.Len(), "expected mosbQueue to be empty for job without MOSB label")
}
