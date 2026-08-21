package build

// Tests for the OSBuildController type (multi-queue shell, enqueue helpers,
// shutdown, informer wiring). The test file lives in the same package
// to access unexported types like informers, listers, and queue internals.

import (
	"context"
	"fmt"
	"sync"
	"testing"
	"time"

	mcfgv1 "github.com/openshift/api/machineconfiguration/v1"
	fakeclientmachineconfiguration "github.com/openshift/client-go/machineconfiguration/clientset/versioned/fake"
	"github.com/openshift/machine-config-operator/pkg/controller/build/imagepruner"
	"github.com/openshift/machine-config-operator/pkg/controller/build/reconcile"
	ctrlcommon "github.com/openshift/machine-config-operator/pkg/controller/common"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/wait"
	k8sfake "k8s.io/client-go/kubernetes/fake"
	"k8s.io/client-go/tools/cache"
	"k8s.io/client-go/util/workqueue"
	clockutil "k8s.io/utils/clock"
	clocktesting "k8s.io/utils/clock/testing"
)

// Compile-time interface satisfaction check.
var _ reconcile.Reconciler = &fakeReconciler{}

// fakeReconciler records calls made by the controller.
type fakeReconciler struct {
	mu    sync.Mutex
	calls map[string][]string // method -> list of keys
}

func newFakeReconciler() *fakeReconciler {
	return &fakeReconciler{calls: make(map[string][]string)}
}

func (f *fakeReconciler) ReconcileMOSC(_ context.Context, key string) error {
	f.record("ReconcileMOSC", key)
	return nil
}

func (f *fakeReconciler) ReconcileMOSB(_ context.Context, key string) error {
	f.record("ReconcileMOSB", key)
	return nil
}

func (f *fakeReconciler) ReconcilePool(_ context.Context, key string) error {
	f.record("ReconcilePool", key)
	return nil
}

func (f *fakeReconciler) ReconcileJob(_ context.Context, key string) error {
	f.record("ReconcileJob", key)
	return nil
}

func (f *fakeReconciler) record(method, key string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls[method] = append(f.calls[method], key)
}

func (f *fakeReconciler) getCalls(method string) []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]string, len(f.calls[method]))
	copy(out, f.calls[method])
	return out
}

// newTestController constructs an OSBuildController the same way
// NewController used to -- creating queues, wiring handlers, etc.
// This is used by unit tests that need direct access to controller
// internals without going through the full service-wiring path.
func newTestController(r reconcile.Reconciler, inf *informers, l *listers, cfg Config, opts ...ControllerOption) *OSBuildController {
	ctrl := &OSBuildController{
		moscQueue:    newStringQueue(moscQueueName),
		mosbQueue:    newStringQueue(mosbQueueName),
		mcpQueue:     newStringQueue(mcpQueueName),
		jobQueue:     newStringQueue(jobQueueName),
		reconciler:   r,
		informers:    inf,
		listers:      l,
		config:       cfg,
		shutdownChan: make(chan struct{}),
		clock:        clockutil.RealClock{},
	}

	for _, o := range opts {
		o(ctrl)
	}

	ctrl.shutdownDelayHandler = &shutdownDelayHandler{
		listers: l,
		clock:   ctrl.clock,
	}

	// Wire informer event handlers when informers are provided.
	if inf != nil && inf.machineOSConfigInformer != nil {
		ctrl.addInformerHandlers()
	}

	return ctrl
}

// TestNewController verifies basic construction.
func TestNewController(t *testing.T) {
	r := newFakeReconciler()

	ctrl := newTestController(r, &informers{}, &listers{}, defaultConfig())
	if ctrl == nil {
		t.Fatal("newTestController returned nil")
	}
	if ctrl.moscQueue == nil || ctrl.mosbQueue == nil || ctrl.mcpQueue == nil || ctrl.jobQueue == nil {
		t.Fatal("one or more workqueues is nil")
	}
	if ctrl.reconciler != r {
		t.Error("reconciler not set correctly")
	}
}

// TestControllerWithClock verifies clock injection.
func TestControllerWithClock(t *testing.T) {
	fakeClock := clocktesting.NewFakeClock(time.Now())
	ctrl := newTestController(newFakeReconciler(), &informers{}, &listers{}, defaultConfig(), WithClock(fakeClock))

	if ctrl.clock != fakeClock {
		t.Error("custom clock was not injected")
	}
}

// TestEnqueueMOSC verifies that enqueueMOSC extracts the correct key.
func TestEnqueueMOSC(t *testing.T) {
	ctrl := newTestController(newFakeReconciler(), &informers{}, &listers{}, defaultConfig())

	mosc := &mcfgv1.MachineOSConfig{
		ObjectMeta: metav1.ObjectMeta{Name: "test-mosc"},
	}
	ctrl.enqueueMOSC(mosc)

	key, quit := ctrl.moscQueue.Get()
	if quit {
		t.Fatal("queue shut down unexpectedly")
	}
	defer ctrl.moscQueue.Done(key)

	if key != "test-mosc" {
		t.Errorf("expected key %q, got %q", "test-mosc", key)
	}
}

// TestEnqueueMOSB verifies that enqueueMOSB extracts the correct key.
func TestEnqueueMOSB(t *testing.T) {
	ctrl := newTestController(newFakeReconciler(), &informers{}, &listers{}, defaultConfig())

	mosb := &mcfgv1.MachineOSBuild{
		ObjectMeta: metav1.ObjectMeta{Name: "test-mosb"},
	}
	ctrl.enqueueMOSB(mosb)

	key, quit := ctrl.mosbQueue.Get()
	if quit {
		t.Fatal("queue shut down unexpectedly")
	}
	defer ctrl.mosbQueue.Done(key)

	if key != "test-mosb" {
		t.Errorf("expected key %q, got %q", "test-mosb", key)
	}
}

// TestEnqueueMCP verifies that enqueueMCP extracts the correct key.
func TestEnqueueMCP(t *testing.T) {
	ctrl := newTestController(newFakeReconciler(), &informers{}, &listers{}, defaultConfig())

	mcp := &mcfgv1.MachineConfigPool{
		ObjectMeta: metav1.ObjectMeta{Name: "worker"},
	}
	ctrl.enqueueMCP(mcp)

	key, quit := ctrl.mcpQueue.Get()
	if quit {
		t.Fatal("queue shut down unexpectedly")
	}
	defer ctrl.mcpQueue.Done(key)

	if key != "worker" {
		t.Errorf("expected key %q, got %q", "worker", key)
	}
}

// TestEnqueueJob verifies that enqueueJob extracts the namespace/name key.
func TestEnqueueJob(t *testing.T) {
	ctrl := newTestController(newFakeReconciler(), &informers{}, &listers{}, defaultConfig())

	job := &batchv1.Job{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "build-job-1",
			Namespace: "openshift-machine-config-operator",
		},
	}
	ctrl.enqueueJob(job)

	key, quit := ctrl.jobQueue.Get()
	if quit {
		t.Fatal("queue shut down unexpectedly")
	}
	defer ctrl.jobQueue.Done(key)

	expected := "openshift-machine-config-operator/build-job-1"
	if key != expected {
		t.Errorf("expected key %q, got %q", expected, key)
	}
}

// TestHandleDeleteWithTombstone verifies tombstone handling in the MOSC delete
// handler. DeletionHandlingMetaNamespaceKeyFunc extracts the Key from the
// DeletedFinalStateUnknown wrapper (which the cache layer populated), so the
// enqueued key is the tombstone's Key field.
func TestHandleDeleteWithTombstone(t *testing.T) {
	ctrl := newTestController(newFakeReconciler(), &informers{}, &listers{}, defaultConfig())

	mosc := &mcfgv1.MachineOSConfig{
		ObjectMeta: metav1.ObjectMeta{Name: "deleted-mosc"},
	}
	tombstone := cache.DeletedFinalStateUnknown{
		Key: "deleted-mosc",
		Obj: mosc,
	}

	ctrl.handleDeleteMOSC(tombstone)

	key, quit := ctrl.moscQueue.Get()
	if quit {
		t.Fatal("queue shut down unexpectedly")
	}
	defer ctrl.moscQueue.Done(key)

	if key != "deleted-mosc" {
		t.Errorf("expected key %q from tombstone, got %q", "deleted-mosc", key)
	}
}

// TestHandleDeleteMCPWithTombstone verifies the MCP delete handler (new in WS2).
func TestHandleDeleteMCPWithTombstone(t *testing.T) {
	ctrl := newTestController(newFakeReconciler(), &informers{}, &listers{}, defaultConfig())

	mcp := &mcfgv1.MachineConfigPool{
		ObjectMeta: metav1.ObjectMeta{Name: "worker"},
	}
	tombstone := cache.DeletedFinalStateUnknown{
		Key: "worker",
		Obj: mcp,
	}

	ctrl.handleDeleteMCP(tombstone)

	key, quit := ctrl.mcpQueue.Get()
	if quit {
		t.Fatal("queue shut down unexpectedly")
	}
	defer ctrl.mcpQueue.Done(key)

	if key != "worker" {
		t.Errorf("expected key %q, got %q", "worker", key)
	}
}

// TestHandleDeleteMOSBWithTombstone verifies the MOSB delete handler processes
// tombstones correctly, extracting the key from the DeletedFinalStateUnknown.
func TestHandleDeleteMOSBWithTombstone(t *testing.T) {
	ctrl := newTestController(newFakeReconciler(), &informers{}, &listers{}, defaultConfig())

	mosb := &mcfgv1.MachineOSBuild{
		ObjectMeta: metav1.ObjectMeta{Name: "deleted-mosb"},
	}
	tombstone := cache.DeletedFinalStateUnknown{
		Key: "deleted-mosb",
		Obj: mosb,
	}

	ctrl.handleDeleteMOSB(tombstone)

	key, quit := ctrl.mosbQueue.Get()
	if quit {
		t.Fatal("queue shut down unexpectedly")
	}
	defer ctrl.mosbQueue.Done(key)

	if key != "deleted-mosb" {
		t.Errorf("expected key %q from tombstone, got %q", "deleted-mosb", key)
	}
}

// TestHandleDeleteJobWithTombstone verifies the Job delete handler processes
// tombstones correctly, extracting the namespace/name key.
func TestHandleDeleteJobWithTombstone(t *testing.T) {
	ctrl := newTestController(newFakeReconciler(), &informers{}, &listers{}, defaultConfig())

	job := &batchv1.Job{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "deleted-job",
			Namespace: "openshift-machine-config-operator",
		},
	}
	tombstone := cache.DeletedFinalStateUnknown{
		Key: "openshift-machine-config-operator/deleted-job",
		Obj: job,
	}

	ctrl.handleDeleteJob(tombstone)

	key, quit := ctrl.jobQueue.Get()
	if quit {
		t.Fatal("queue shut down unexpectedly")
	}
	defer ctrl.jobQueue.Done(key)

	expected := "openshift-machine-config-operator/deleted-job"
	if key != expected {
		t.Errorf("expected key %q from tombstone, got %q", expected, key)
	}
}

// TestProcessQueueCallsReconciler verifies the queue->reconciler dispatch path.
func TestProcessQueueCallsReconciler(t *testing.T) {
	r := newFakeReconciler()
	ctrl := newTestController(r, &informers{}, &listers{}, defaultConfig())

	// Enqueue a key and process it.
	ctrl.moscQueue.Add("my-mosc")
	ctrl.processQueue(context.Background(), ctrl.moscQueue, ctrl.reconciler.ReconcileMOSC)

	calls := r.getCalls("ReconcileMOSC")
	if len(calls) != 1 || calls[0] != "my-mosc" {
		t.Errorf("expected ReconcileMOSC called with [my-mosc], got %v", calls)
	}
}

// TestProcessQueueRetries verifies retry and drop behavior.
// Uses a zero-delay rate limiter so retries are deterministic and
// do not depend on wall-clock timing.
func TestProcessQueueRetries(t *testing.T) {
	cfg := defaultConfig()
	cfg.MaxRetries = 2

	callCount := 0
	failingReconciler := newFakeReconciler()
	ctrl := newTestController(failingReconciler, &informers{}, &listers{}, cfg)

	// Replace the default moscQueue with one using a zero-delay rate limiter
	// so retried items are immediately available for re-processing.
	ctrl.moscQueue.ShutDown()
	rl := workqueue.NewTypedItemExponentialFailureRateLimiter[string](0, 0)
	ctrl.moscQueue = workqueue.NewTypedRateLimitingQueueWithConfig[string](rl,
		workqueue.TypedRateLimitingQueueConfig[string]{Name: "test-zero-delay"})

	// Create a handler that always fails.
	handler := func(_ context.Context, key string) error {
		callCount++
		return fmt.Errorf("transient error for %s", key)
	}

	ctrl.moscQueue.Add("fail-key")

	// Process until the key is dropped (MaxRetries = 2 means 3 total attempts:
	// 1 initial + 2 retries, then dropped via Forget+return).
	for i := 0; i < cfg.MaxRetries+1; i++ {
		ctrl.processQueue(context.Background(), ctrl.moscQueue, handler)
	}

	if callCount != cfg.MaxRetries+1 {
		t.Errorf("expected %d calls (1 initial + %d retries), got %d", cfg.MaxRetries+1, cfg.MaxRetries, callCount)
	}
}

// TestShutdownChan verifies the shutdown channel is not closed initially and
// IS closed after shutdownController completes. We call shutdownController
// directly rather than going through Run to avoid requiring real informers
// and listers.
func TestShutdownChan(t *testing.T) {
	cfg := defaultConfig()
	cfg.MaxShutdownDelay = 100 * time.Millisecond
	cfg.ShutdownPollInterval = 10 * time.Millisecond

	// Provide real (empty) fake clients so the shutdown delay handler's
	// listers can list objects without panicking.
	mcfgclient := fakeclientmachineconfiguration.NewSimpleClientset()
	kubeclient := k8sfake.NewSimpleClientset()
	inf := newInformers(mcfgclient, kubeclient)

	ctrl := newTestController(newFakeReconciler(), inf, inf.listers(), cfg)

	// ShutdownChan should not be closed initially.
	select {
	case <-ctrl.ShutdownChan():
		t.Fatal("shutdown channel should not be closed yet")
	default:
	}

	// Call shutdownController directly. This shuts down all queues and
	// closes the shutdown channel.
	ctrl.shutdownController()

	// ShutdownChan should now be closed.
	select {
	case <-ctrl.ShutdownChan():
		// expected
	default:
		t.Error("shutdown channel should be closed after shutdownController")
	}
}

// TestNewInformersHasSyncedIncludesMachineConfig verifies that the
// machineConfigInformer's HasSynced callback is included in the hasSynced
// slice. Without it, the controller may reconcile before the MC lister is
// populated, causing spurious "not found" errors.
func TestNewInformersHasSyncedIncludesMachineConfig(t *testing.T) {
	// newInformers requires non-nil clients. We use fakes to construct the
	// informers and inspect the resulting hasSynced slice length. The old code
	// had 8 entries (missing machineConfigInformer); after the fix there should
	// be 9.
	mcfgclient := fakeclientmachineconfiguration.NewSimpleClientset()
	kubeclient := k8sfake.NewSimpleClientset()

	inf := newInformers(mcfgclient, kubeclient)

	// We expect 9 hasSynced callbacks:
	// controllerConfig, machineConfigPool, machineConfig, job,
	// machineOSBuild, machineOSConfig, node, configmap, secret
	expected := 9
	if got := len(inf.hasSynced); got != expected {
		t.Errorf("expected %d hasSynced callbacks, got %d -- machineConfigInformer.HasSynced may be missing", expected, got)
	}
}

// TestMultipleQueuesIndependent verifies that enqueuing on one queue does not
// affect others.
func TestMultipleQueuesIndependent(t *testing.T) {
	ctrl := newTestController(newFakeReconciler(), &informers{}, &listers{}, defaultConfig())

	ctrl.moscQueue.Add("mosc-key")
	ctrl.mosbQueue.Add("mosb-key")
	ctrl.mcpQueue.Add("mcp-key")
	ctrl.jobQueue.Add("ns/job-key")

	// Each queue should have exactly 1 item.
	if ctrl.moscQueue.Len() != 1 {
		t.Errorf("moscQueue: expected len 1, got %d", ctrl.moscQueue.Len())
	}
	if ctrl.mosbQueue.Len() != 1 {
		t.Errorf("mosbQueue: expected len 1, got %d", ctrl.mosbQueue.Len())
	}
	if ctrl.mcpQueue.Len() != 1 {
		t.Errorf("mcpQueue: expected len 1, got %d", ctrl.mcpQueue.Len())
	}
	if ctrl.jobQueue.Len() != 1 {
		t.Errorf("jobQueue: expected len 1, got %d", ctrl.jobQueue.Len())
	}
}

// TestOSBuildController_EndToEnd_WorkqueueDispatch exercises the full
// OSBuildController -> compositeReconciler -> workqueue pipeline with real informers
// and fake clients, verifying that creating a MOSC via the fake client
// triggers reconciliation through the actual workqueue dispatch path.
func TestOSBuildController_EndToEnd_WorkqueueDispatch(t *testing.T) {
	// Pre-populate fake clients with a MCP and MC.
	mcp := &mcfgv1.MachineConfigPool{
		ObjectMeta: metav1.ObjectMeta{Name: "worker"},
		Spec: mcfgv1.MachineConfigPoolSpec{
			Configuration: mcfgv1.MachineConfigPoolStatusConfiguration{
				ObjectReference: corev1.ObjectReference{Name: "rendered-worker-1"},
			},
		},
	}
	mc := &mcfgv1.MachineConfig{
		ObjectMeta: metav1.ObjectMeta{
			Name: "rendered-worker-1",
			Annotations: map[string]string{
				ctrlcommon.ReleaseImageVersionAnnotationKey:          "4.19.0",
				ctrlcommon.GeneratedByControllerVersionAnnotationKey: "4.19.0",
			},
		},
	}
	cc := &mcfgv1.ControllerConfig{
		ObjectMeta: metav1.ObjectMeta{Name: "machine-config-controller"},
	}

	mcfgclient := fakeclientmachineconfiguration.NewSimpleClientset(mcp, mc, cc)
	kubeclient := k8sfake.NewSimpleClientset()

	cfg := Config{
		MaxRetries:           3,
		UpdateDelay:          time.Millisecond * 10,
		MaxShutdownDelay:     time.Second * 2,
		ShutdownPollInterval: time.Millisecond * 50,
	}

	ctrl := newOSBuildController(cfg, mcfgclient, kubeclient, imagepruner.NewImagePruner())

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Start the controller in background.
	go ctrl.Run(ctx, 1)

	// Wait for caches to sync (with timeout).
	syncCtx, syncCancel := context.WithTimeout(ctx, 5*time.Second)
	defer syncCancel()
	if !cache.WaitForCacheSync(syncCtx.Done(), ctrl.hasSyncedFuncs()...) {
		t.Fatal("caches failed to sync")
	}

	// Create a MOSC via the fake client -- this should trigger informer -> queue -> reconcile.
	mosc := &mcfgv1.MachineOSConfig{
		ObjectMeta: metav1.ObjectMeta{Name: "worker"},
		Spec: mcfgv1.MachineOSConfigSpec{
			MachineConfigPool:     mcfgv1.MachineConfigPoolReference{Name: "worker"},
			RenderedImagePushSpec: "registry.example.com/ocp:latest",
		},
	}
	_, err := mcfgclient.MachineconfigurationV1().MachineOSConfigs().Create(ctx, mosc, metav1.CreateOptions{})
	if err != nil {
		t.Fatalf("could not create MOSC: %v", err)
	}

	// Poll for a MOSB to be created (the reconciler should create one).
	pollCtx, pollCancel := context.WithTimeout(ctx, 5*time.Second)
	defer pollCancel()

	err = wait.PollUntilContextTimeout(pollCtx, 10*time.Millisecond, 5*time.Second, true, func(ctx context.Context) (bool, error) {
		mosbList, listErr := mcfgclient.MachineconfigurationV1().MachineOSBuilds().List(ctx, metav1.ListOptions{})
		if listErr != nil {
			return false, nil
		}
		if len(mosbList.Items) > 0 {
			t.Logf("MOSB created via workqueue dispatch: %s", mosbList.Items[0].Name)
			return true, nil
		}
		return false, nil
	})
	if err != nil {
		t.Errorf("expected MOSB to be created via workqueue dispatch: %v", err)
	}

	// Shutdown.
	cancel()
	select {
	case <-ctrl.ShutdownChan():
		t.Log("controller shut down cleanly")
	case <-time.After(5 * time.Second):
		t.Error("controller did not shut down within 5s")
	}
}
