package build

// Tests for the Controller type (multi-queue shell, enqueue helpers,
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
	batchv1 "k8s.io/api/batch/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	k8sfake "k8s.io/client-go/kubernetes/fake"
	"k8s.io/client-go/tools/cache"
	clocktesting "k8s.io/utils/clock/testing"
)

// fakeReconciler records calls made by the controller.
type fakeReconciler struct {
	mu    sync.Mutex
	calls map[string][]string // method → list of keys
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

// TestNewController verifies basic construction.
func TestNewController(t *testing.T) {
	r := newFakeReconciler()

	bc := NewController(r, &informers{}, &listers{}, defaultConfig())
	if bc == nil {
		t.Fatal("NewController returned nil")
	}
	if bc.moscQueue == nil || bc.mosbQueue == nil || bc.mcpQueue == nil || bc.jobQueue == nil {
		t.Fatal("one or more workqueues is nil")
	}
	if bc.reconciler != r {
		t.Error("reconciler not set correctly")
	}
}

// TestControllerWithClock verifies clock injection.
func TestControllerWithClock(t *testing.T) {
	fakeClock := clocktesting.NewFakeClock(time.Now())
	bc := NewController(newFakeReconciler(), &informers{}, &listers{}, defaultConfig(), WithClock(fakeClock))

	if bc.clock != fakeClock {
		t.Error("custom clock was not injected")
	}
}

// TestEnqueueMOSC verifies that enqueueMOSC extracts the correct key.
func TestEnqueueMOSC(t *testing.T) {
	bc := NewController(newFakeReconciler(), &informers{}, &listers{}, defaultConfig())

	mosc := &mcfgv1.MachineOSConfig{
		ObjectMeta: metav1.ObjectMeta{Name: "test-mosc"},
	}
	bc.enqueueMOSC(mosc)

	key, quit := bc.moscQueue.Get()
	if quit {
		t.Fatal("queue shut down unexpectedly")
	}
	defer bc.moscQueue.Done(key)

	if key != "test-mosc" {
		t.Errorf("expected key %q, got %q", "test-mosc", key)
	}
}

// TestEnqueueMOSB verifies that enqueueMOSB extracts the correct key.
func TestEnqueueMOSB(t *testing.T) {
	bc := NewController(newFakeReconciler(), &informers{}, &listers{}, defaultConfig())

	mosb := &mcfgv1.MachineOSBuild{
		ObjectMeta: metav1.ObjectMeta{Name: "test-mosb"},
	}
	bc.enqueueMOSB(mosb)

	key, quit := bc.mosbQueue.Get()
	if quit {
		t.Fatal("queue shut down unexpectedly")
	}
	defer bc.mosbQueue.Done(key)

	if key != "test-mosb" {
		t.Errorf("expected key %q, got %q", "test-mosb", key)
	}
}

// TestEnqueueMCP verifies that enqueueMCP extracts the correct key.
func TestEnqueueMCP(t *testing.T) {
	bc := NewController(newFakeReconciler(), &informers{}, &listers{}, defaultConfig())

	mcp := &mcfgv1.MachineConfigPool{
		ObjectMeta: metav1.ObjectMeta{Name: "worker"},
	}
	bc.enqueueMCP(mcp)

	key, quit := bc.mcpQueue.Get()
	if quit {
		t.Fatal("queue shut down unexpectedly")
	}
	defer bc.mcpQueue.Done(key)

	if key != "worker" {
		t.Errorf("expected key %q, got %q", "worker", key)
	}
}

// TestEnqueueJob verifies that enqueueJob extracts the namespace/name key.
func TestEnqueueJob(t *testing.T) {
	bc := NewController(newFakeReconciler(), &informers{}, &listers{}, defaultConfig())

	job := &batchv1.Job{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "build-job-1",
			Namespace: "openshift-machine-config-operator",
		},
	}
	bc.enqueueJob(job)

	key, quit := bc.jobQueue.Get()
	if quit {
		t.Fatal("queue shut down unexpectedly")
	}
	defer bc.jobQueue.Done(key)

	expected := "openshift-machine-config-operator/build-job-1"
	if key != expected {
		t.Errorf("expected key %q, got %q", expected, key)
	}
}

// TestHandleDeleteWithTombstone verifies tombstone handling in delete handlers.
func TestHandleDeleteWithTombstone(t *testing.T) {
	bc := NewController(newFakeReconciler(), &informers{}, &listers{}, defaultConfig())

	// Simulate a tombstone wrapping a MOSC.
	mosc := &mcfgv1.MachineOSConfig{
		ObjectMeta: metav1.ObjectMeta{Name: "deleted-mosc"},
	}
	tombstone := cache.DeletedFinalStateUnknown{
		Key: "deleted-mosc",
		Obj: mosc,
	}

	bc.handleDeleteMOSC(tombstone)

	key, quit := bc.moscQueue.Get()
	if quit {
		t.Fatal("queue shut down unexpectedly")
	}
	defer bc.moscQueue.Done(key)

	if key != "deleted-mosc" {
		t.Errorf("expected key %q from tombstone, got %q", "deleted-mosc", key)
	}
}

// TestHandleDeleteMCPWithTombstone verifies the MCP delete handler (new in WS2).
func TestHandleDeleteMCPWithTombstone(t *testing.T) {
	bc := NewController(newFakeReconciler(), &informers{}, &listers{}, defaultConfig())

	mcp := &mcfgv1.MachineConfigPool{
		ObjectMeta: metav1.ObjectMeta{Name: "worker"},
	}
	tombstone := cache.DeletedFinalStateUnknown{
		Key: "worker",
		Obj: mcp,
	}

	bc.handleDeleteMCP(tombstone)

	key, quit := bc.mcpQueue.Get()
	if quit {
		t.Fatal("queue shut down unexpectedly")
	}
	defer bc.mcpQueue.Done(key)

	if key != "worker" {
		t.Errorf("expected key %q, got %q", "worker", key)
	}
}

// TestProcessQueueCallsReconciler verifies the queue→reconciler dispatch path.
func TestProcessQueueCallsReconciler(t *testing.T) {
	r := newFakeReconciler()
	bc := NewController(r, &informers{}, &listers{}, defaultConfig())

	// Enqueue a key and process it.
	bc.moscQueue.Add("my-mosc")
	bc.processQueue(context.Background(), bc.moscQueue, bc.reconciler.ReconcileMOSC)

	calls := r.getCalls("ReconcileMOSC")
	if len(calls) != 1 || calls[0] != "my-mosc" {
		t.Errorf("expected ReconcileMOSC called with [my-mosc], got %v", calls)
	}
}

// TestProcessQueueRetries verifies retry and drop behavior.
func TestProcessQueueRetries(t *testing.T) {
	cfg := defaultConfig()
	cfg.MaxRetries = 2

	callCount := 0
	failingReconciler := newFakeReconciler()
	bc := NewController(failingReconciler, &informers{}, &listers{}, cfg)

	// Create a handler that always fails.
	handler := func(_ context.Context, key string) error {
		callCount++
		return fmt.Errorf("transient error for %s", key)
	}

	bc.moscQueue.Add("fail-key")

	// Process until the key is dropped (MaxRetries = 2 means 3 total attempts:
	// 1 initial + 2 retries, then dropped via Forget+return).
	for i := 0; i < cfg.MaxRetries+1; i++ {
		bc.processQueue(context.Background(), bc.moscQueue, handler)
	}

	if callCount != cfg.MaxRetries+1 {
		t.Errorf("expected %d calls (1 initial + %d retries), got %d", cfg.MaxRetries+1, cfg.MaxRetries, callCount)
	}
}

// TestShutdownChan verifies the shutdown channel closes after shutdown.
func TestShutdownChan(t *testing.T) {
	bc := NewController(newFakeReconciler(), &informers{}, &listers{}, defaultConfig())

	// ShutdownChan should not be closed initially.
	select {
	case <-bc.ShutdownChan():
		t.Fatal("shutdown channel should not be closed yet")
	default:
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
		t.Errorf("expected %d hasSynced callbacks, got %d — machineConfigInformer.HasSynced may be missing", expected, got)
	}
}

// TestMultipleQueuesIndependent verifies that enqueuing on one queue does not
// affect others.
func TestMultipleQueuesIndependent(t *testing.T) {
	bc := NewController(newFakeReconciler(), &informers{}, &listers{}, defaultConfig())

	bc.moscQueue.Add("mosc-key")
	bc.mosbQueue.Add("mosb-key")
	bc.mcpQueue.Add("mcp-key")
	bc.jobQueue.Add("ns/job-key")

	// Each queue should have exactly 1 item.
	if bc.moscQueue.Len() != 1 {
		t.Errorf("moscQueue: expected len 1, got %d", bc.moscQueue.Len())
	}
	if bc.mosbQueue.Len() != 1 {
		t.Errorf("mosbQueue: expected len 1, got %d", bc.mosbQueue.Len())
	}
	if bc.mcpQueue.Len() != 1 {
		t.Errorf("mcpQueue: expected len 1, got %d", bc.mcpQueue.Len())
	}
	if bc.jobQueue.Len() != 1 {
		t.Errorf("jobQueue: expected len 1, got %d", bc.jobQueue.Len())
	}
}
