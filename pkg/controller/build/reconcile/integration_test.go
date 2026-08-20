package reconcile

import (
	"context"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"

	mcfgv1 "github.com/openshift/api/machineconfiguration/v1"
	fakemcfgclient "github.com/openshift/client-go/machineconfiguration/clientset/versioned/fake"
	"github.com/openshift/machine-config-operator/pkg/controller/build/constants"
	ctrlcommon "github.com/openshift/machine-config-operator/pkg/controller/common"
	"github.com/openshift/machine-config-operator/pkg/controller/build/services"
	"github.com/openshift/machine-config-operator/pkg/controller/build/utils"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/runtime"
	fakekube "k8s.io/client-go/kubernetes/fake"
	batchlisterv1 "k8s.io/client-go/listers/batch/v1"
)

// ---------------------------------------------------------------------------
// Integration-test helpers
// ---------------------------------------------------------------------------

const (
	testPool           = "worker"
	testMOSCName       = "worker"
	testRenderedConfig = "rendered-worker-1"
	testRenderedPush   = "registry.example.com/ocp:latest"
	testVersion        = "4.19.0"
)

// testMC creates a MachineConfig with required annotations.
func testMC() *mcfgv1.MachineConfig {
	return &mcfgv1.MachineConfig{
		ObjectMeta: metav1.ObjectMeta{
			Name: testRenderedConfig,
			Annotations: map[string]string{
				ctrlcommon.ReleaseImageVersionAnnotationKey:           testVersion,
				ctrlcommon.GeneratedByControllerVersionAnnotationKey: testVersion,
			},
		},
	}
}

// testMCP creates a MachineConfigPool referencing the rendered config.
func testMCP() *mcfgv1.MachineConfigPool {
	return &mcfgv1.MachineConfigPool{
		ObjectMeta: metav1.ObjectMeta{
			Name:   testPool,
			Labels: map[string]string{"pools.operator.machineconfiguration.openshift.io/" + testPool: ""},
		},
		Spec: mcfgv1.MachineConfigPoolSpec{
			Configuration: mcfgv1.MachineConfigPoolStatusConfiguration{
				ObjectReference: corev1.ObjectReference{Name: testRenderedConfig},
			},
		},
	}
}

// testMOSC creates a MachineOSConfig for the test pool.
func testMOSC() *mcfgv1.MachineOSConfig {
	return &mcfgv1.MachineOSConfig{
		ObjectMeta: metav1.ObjectMeta{Name: testMOSCName},
		Spec: mcfgv1.MachineOSConfigSpec{
			MachineConfigPool:    mcfgv1.MachineConfigPoolReference{Name: testPool},
			RenderedImagePushSpec: testRenderedPush,
		},
	}
}

// ---------------------------------------------------------------------------
// mutableLister — a thread-safe lister whose items can be updated during a test
// ---------------------------------------------------------------------------

type mutableMOSBLister struct {
	mu    sync.RWMutex
	items []*mcfgv1.MachineOSBuild
}

func (m *mutableMOSBLister) add(mosb *mcfgv1.MachineOSBuild) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.items = append(m.items, mosb)
}

func (m *mutableMOSBLister) replace(name string, mosb *mcfgv1.MachineOSBuild) {
	m.mu.Lock()
	defer m.mu.Unlock()
	for i, b := range m.items {
		if b.Name == name {
			m.items[i] = mosb
			return
		}
	}
	m.items = append(m.items, mosb)
}

func (m *mutableMOSBLister) List(sel labels.Selector) ([]*mcfgv1.MachineOSBuild, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	var ret []*mcfgv1.MachineOSBuild
	for _, v := range m.items {
		if sel.Matches(labels.Set(v.Labels)) {
			ret = append(ret, v)
		}
	}
	return ret, nil
}

func (m *mutableMOSBLister) Get(name string) (*mcfgv1.MachineOSBuild, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	for _, v := range m.items {
		if v.Name == name {
			return v, nil
		}
	}
	return nil, notFound("MachineOSBuild", name)
}

// ---------------------------------------------------------------------------
// trackingEventRecorder — counts events for assertions
// ---------------------------------------------------------------------------

type trackingEventRecorder struct {
	services.EventRecorder // embed noop
	mu                     sync.Mutex
	events                 map[string]int
}

func newTrackingEventRecorder() *trackingEventRecorder {
	return &trackingEventRecorder{
		EventRecorder: services.NewNoopEventRecorder(),
		events:        make(map[string]int),
	}
}

func (t *trackingEventRecorder) RecordConfigReconciled(_ *mcfgv1.MachineOSConfig) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.events["ConfigReconciled"]++
}

func (t *trackingEventRecorder) RecordBuildFailed(_ *mcfgv1.MachineOSBuild) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.events["BuildFailed"]++
}

func (t *trackingEventRecorder) RecordBuildCompleted(_ *mcfgv1.MachineOSBuild, _ string) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.events["BuildCompleted"]++
}

func (t *trackingEventRecorder) RecordBuildDegraded(_ *mcfgv1.MachineOSConfig) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.events["BuildDegraded"]++
}

func (t *trackingEventRecorder) RecordBuildRecovered(_ *mcfgv1.MachineOSConfig) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.events["BuildRecovered"]++
}

func (t *trackingEventRecorder) count(name string) int {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.events[name]
}

// ---------------------------------------------------------------------------
// Integration Tests
// ---------------------------------------------------------------------------

// TestIntegration_MOSCCreateTriggersMOSBCreation verifies the cross-controller
// chain: reconciling a MOSC that has no builds creates a new MOSB via the
// fake mcfg client.
func TestIntegration_MOSCCreateTriggersMOSBCreation(t *testing.T) {
	mosc := testMOSC()
	mcp := testMCP()
	mc := testMC()

	mcfgclient := fakemcfgclient.NewSimpleClientset()
	events := newTrackingEventRecorder()

	moscLister := &fakeMOSCListerForSelector{items: []*mcfgv1.MachineOSConfig{mosc}}
	mosbLister := &fakeMOSBListerForSelector{items: nil} // no builds yet
	mcpLister := &fakeMCPListerForSelector{items: []*mcfgv1.MachineConfigPool{mcp}}
	mcLister := &fakeMCListerForSelector{items: []*mcfgv1.MachineConfig{mc}}

	moscReconciler := NewMOSCReconciler(
		mcfgclient, nil, moscLister, mosbLister, mcpLister, mcLister,
		events, services.NewNoopMetricsRecorder(),
		&fakeSeeder{}, &fakeReuseChecker{},
	)

	ctx := context.Background()
	if err := moscReconciler.ReconcileMOSC(ctx, testMOSCName); err != nil {
		t.Fatalf("ReconcileMOSC failed: %v", err)
	}

	// Verify MOSB was created via fake client.
	mosbList, err := mcfgclient.MachineconfigurationV1().MachineOSBuilds().List(ctx, metav1.ListOptions{})
	if err != nil {
		t.Fatalf("list MOSBs failed: %v", err)
	}
	if len(mosbList.Items) != 1 {
		t.Fatalf("expected 1 MOSB created, got %d", len(mosbList.Items))
	}
	if events.count("ConfigReconciled") != 1 {
		t.Errorf("expected 1 ConfigReconciled event, got %d", events.count("ConfigReconciled"))
	}

	t.Logf("MOSB created: %s", mosbList.Items[0].Name)
}

// TestIntegration_MOSBTerminalFailureTriggersDegraded verifies: when a MOSB
// is in failed state, the MOSB reconciler records the failure event and
// invokes the degraded handler.
func TestIntegration_MOSBTerminalFailureTriggersDegraded(t *testing.T) {
	mosc := testMOSC()
	mcp := testMCP()

	failedMOSB := &mcfgv1.MachineOSBuild{
		ObjectMeta: metav1.ObjectMeta{
			Name: "failed-mosb",
			Labels: map[string]string{
				constants.TargetMachineConfigPoolLabelKey: testPool,
				constants.MachineOSConfigNameLabelKey:     testMOSCName,
			},
		},
		Status: mcfgv1.MachineOSBuildStatus{
			Conditions: []metav1.Condition{
				{Type: string(mcfgv1.MachineOSBuildFailed), Status: metav1.ConditionTrue, Message: "build error"},
			},
		},
	}

	events := newTrackingEventRecorder()
	dh := &fakeDegradedHandler{}

	mosbLister := &fakeMOSBListerForSelector{items: []*mcfgv1.MachineOSBuild{failedMOSB}}
	moscLister := &fakeMOSCListerForSelector{items: []*mcfgv1.MachineOSConfig{mosc}}
	mcpLister := &fakeMCPListerForSelector{items: []*mcfgv1.MachineConfigPool{mcp}}

	mosbReconciler := NewMOSBReconciler(
		nil, nil, mosbLister, moscLister, mcpLister, nil,
		events, services.NewNoopMetricsRecorder(), dh,
		&utils.Listers{
			MachineOSBuildLister:    mosbLister,
			MachineOSConfigLister:   moscLister,
			MachineConfigPoolLister: mcpLister,
		},
	)

	if err := mosbReconciler.ReconcileMOSB(context.Background(), "failed-mosb"); err != nil {
		t.Fatalf("ReconcileMOSB failed: %v", err)
	}

	if events.count("BuildFailed") != 1 {
		t.Errorf("expected 1 BuildFailed event, got %d", events.count("BuildFailed"))
	}
	if events.count("BuildDegraded") != 1 {
		t.Errorf("expected 1 BuildDegraded event, got %d", events.count("BuildDegraded"))
	}
	if !dh.wasUpdateCalled() {
		t.Error("expected degraded handler UpdateImageBuildDegraded to be called")
	}
}

// TestIntegration_PoolReconcileUpdatesDegradedAndCreatesBuilds verifies:
// the pool reconciler updates degraded condition and ensures a MOSB exists.
func TestIntegration_PoolReconcileUpdatesDegradedAndCreatesBuilds(t *testing.T) {
	mosc := testMOSC()
	mcp := testMCP()
	mc := testMC()

	mcfgclient := fakemcfgclient.NewSimpleClientset()
	events := newTrackingEventRecorder()
	dh := &fakeDegradedHandler{}

	moscLister := &fakeMOSCListerForSelector{items: []*mcfgv1.MachineOSConfig{mosc}}
	mosbLister := &fakeMOSBListerForSelector{items: nil}
	mcpLister := &fakeMCPListerForSelector{items: []*mcfgv1.MachineConfigPool{mcp}}
	mcLister := &fakeMCListerForSelector{items: []*mcfgv1.MachineConfig{mc}}

	poolReconciler := NewPoolReconciler(
		mcfgclient, mcpLister, moscLister, mosbLister, mcLister,
		events, services.NewNoopMetricsRecorder(), dh,
		&utils.Listers{
			MachineOSBuildLister:    mosbLister,
			MachineOSConfigLister:   moscLister,
			MachineConfigPoolLister: mcpLister,
		},
	)

	ctx := context.Background()
	if err := poolReconciler.ReconcilePool(ctx, testPool); err != nil {
		t.Fatalf("ReconcilePool failed: %v", err)
	}

	// Verify MOSB was created.
	mosbList, err := mcfgclient.MachineconfigurationV1().MachineOSBuilds().List(ctx, metav1.ListOptions{})
	if err != nil {
		t.Fatalf("list MOSBs failed: %v", err)
	}
	if len(mosbList.Items) != 1 {
		t.Fatalf("expected 1 MOSB created, got %d", len(mosbList.Items))
	}

	if !dh.wasUpdateCalled() {
		t.Error("expected degraded handler to be called")
	}
}

// TestIntegration_CrossControllerChain_MOSCToMOSBToPool exercises the full chain:
// 1. MOSC reconcile → creates MOSB
// 2. MOSB reconcile (in failed state) → records failure, triggers degraded
// 3. Pool reconcile → updates degraded condition
func TestIntegration_CrossControllerChain_MOSCToMOSBToPool(t *testing.T) {
	mosc := testMOSC()
	mcp := testMCP()
	mc := testMC()

	mcfgclient := fakemcfgclient.NewSimpleClientset()
	events := newTrackingEventRecorder()
	dh := &fakeDegradedHandler{}

	mutableMOSB := &mutableMOSBLister{}
	moscLister := &fakeMOSCListerForSelector{items: []*mcfgv1.MachineOSConfig{mosc}}
	mcpLister := &fakeMCPListerForSelector{items: []*mcfgv1.MachineConfigPool{mcp}}
	mcLister := &fakeMCListerForSelector{items: []*mcfgv1.MachineConfig{mc}}

	utilListers := &utils.Listers{
		MachineOSBuildLister:    mutableMOSB,
		MachineOSConfigLister:   moscLister,
		MachineConfigPoolLister: mcpLister,
	}

	moscReconciler := NewMOSCReconciler(
		mcfgclient, nil, moscLister, mutableMOSB, mcpLister, mcLister,
		events, services.NewNoopMetricsRecorder(),
		&fakeSeeder{}, &fakeReuseChecker{},
	)

	mosbReconciler := NewMOSBReconciler(
		mcfgclient, nil, mutableMOSB, moscLister, mcpLister, mcLister,
		events, services.NewNoopMetricsRecorder(), dh, utilListers,
	)

	poolReconciler := NewPoolReconciler(
		mcfgclient, mcpLister, moscLister, mutableMOSB, mcLister,
		events, services.NewNoopMetricsRecorder(), dh, utilListers,
	)

	ctx := context.Background()

	// Step 1: MOSC reconcile → creates MOSB
	if err := moscReconciler.ReconcileMOSC(ctx, testMOSCName); err != nil {
		t.Fatalf("Step 1: ReconcileMOSC failed: %v", err)
	}
	if events.count("ConfigReconciled") != 1 {
		t.Fatalf("Step 1: expected ConfigReconciled event")
	}

	// Read the created MOSB from the fake client.
	mosbList, err := mcfgclient.MachineconfigurationV1().MachineOSBuilds().List(ctx, metav1.ListOptions{})
	if err != nil || len(mosbList.Items) != 1 {
		t.Fatalf("Step 1: expected 1 MOSB, got %d", len(mosbList.Items))
	}
	createdMOSB := mosbList.Items[0].DeepCopy()
	t.Logf("Step 1: MOSB %q created", createdMOSB.Name)

	// Step 2: Simulate build failure by setting status, add to lister.
	createdMOSB.Status.Conditions = []metav1.Condition{
		{Type: string(mcfgv1.MachineOSBuildFailed), Status: metav1.ConditionTrue, Message: "simulated failure"},
	}
	mutableMOSB.add(createdMOSB)

	if err := mosbReconciler.ReconcileMOSB(ctx, createdMOSB.Name); err != nil {
		t.Fatalf("Step 2: ReconcileMOSB failed: %v", err)
	}
	if events.count("BuildFailed") != 1 {
		t.Errorf("Step 2: expected BuildFailed event")
	}
	if events.count("BuildDegraded") != 1 {
		t.Errorf("Step 2: expected BuildDegraded event")
	}

	// Step 3: Pool reconcile picks up degraded condition.
	dh.updateCount.Store(0)
	if err := poolReconciler.ReconcilePool(ctx, testPool); err != nil {
		t.Fatalf("Step 3: ReconcilePool failed: %v", err)
	}
	if !dh.wasUpdateCalled() {
		t.Error("Step 3: expected degraded handler to be invoked by pool reconciler")
	}
}

// TestIntegration_ConcurrencyStressor runs all four reconcilers from
// multiple goroutines concurrently to exercise the race detector.
func TestIntegration_ConcurrencyStressor(t *testing.T) {
	mosc := testMOSC()
	mcp := testMCP()
	mc := testMC()

	mosb := &mcfgv1.MachineOSBuild{
		ObjectMeta: metav1.ObjectMeta{
			Name: "concurrent-mosb",
			Labels: map[string]string{
				constants.TargetMachineConfigPoolLabelKey: testPool,
				constants.MachineOSConfigNameLabelKey:     testMOSCName,
				constants.RenderedMachineConfigLabelKey:   testRenderedConfig,
			},
		},
	}

	job := &batchv1.Job{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "build-job",
			Namespace: ctrlcommon.MCONamespace,
			Labels: map[string]string{
				constants.MachineOSBuildNameLabelKey: "concurrent-mosb",
			},
		},
	}

	mcfgclient := fakemcfgclient.NewSimpleClientset([]runtime.Object{mosc}...)
	kubeclient := fakekube.NewSimpleClientset()

	moscLister := &fakeMOSCListerForSelector{items: []*mcfgv1.MachineOSConfig{mosc}}
	mosbLister := &mutableMOSBLister{items: []*mcfgv1.MachineOSBuild{mosb}}
	mcpLister := &fakeMCPListerForSelector{items: []*mcfgv1.MachineConfigPool{mcp}}
	mcLister := &fakeMCListerForSelector{items: []*mcfgv1.MachineConfig{mc}}
	jobLister := &integrationJobLister{items: []*batchv1.Job{job}}

	utilListers := &utils.Listers{
		MachineOSBuildLister:    mosbLister,
		MachineOSConfigLister:   moscLister,
		MachineConfigPoolLister: mcpLister,
	}

	events := newTrackingEventRecorder()
	metrics := services.NewNoopMetricsRecorder()
	dh := &fakeDegradedHandler{}

	moscR := NewMOSCReconciler(mcfgclient, kubeclient, moscLister, mosbLister, mcpLister, mcLister,
		events, metrics, &fakeSeeder{}, &fakeReuseChecker{})
	mosbR := NewMOSBReconciler(mcfgclient, kubeclient, mosbLister, moscLister, mcpLister, mcLister,
		events, metrics, dh, utilListers)
	poolR := NewPoolReconciler(mcfgclient, mcpLister, moscLister, mosbLister, mcLister,
		events, metrics, dh, utilListers)
	jobR := NewJobReconciler(mcfgclient, kubeclient, jobLister, mosbLister, moscLister,
		events, metrics, utilListers)

	ctx := context.Background()

	const workers = 8
	const iterations = 20
	var errCount atomic.Int64
	var wg sync.WaitGroup

	for w := 0; w < workers; w++ {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()
			for i := 0; i < iterations; i++ {
				switch id % 4 {
				case 0:
					if err := moscR.ReconcileMOSC(ctx, testMOSCName); err != nil {
						errCount.Add(1)
					}
				case 1:
					if err := mosbR.ReconcileMOSB(ctx, "concurrent-mosb"); err != nil {
						errCount.Add(1)
					}
				case 2:
					if err := poolR.ReconcilePool(ctx, testPool); err != nil {
						errCount.Add(1)
					}
				case 3:
					if err := jobR.ReconcileJob(ctx, fmt.Sprintf("%s/build-job", ctrlcommon.MCONamespace)); err != nil {
						// Builder creation errors expected with minimal test objects
					}
				}
			}
		}(w)
	}

	wg.Wait()

	t.Logf("Concurrency stressor: %d workers × %d iterations, %d reconcile errors (non-fatal for race test)",
		workers, iterations, errCount.Load())
}

// TestIntegration_PoolReconcileIdempotent verifies that running PoolReconcile
// twice doesn't create duplicate MOSBs.
func TestIntegration_PoolReconcileIdempotent(t *testing.T) {
	mosc := testMOSC()
	mcp := testMCP()
	mc := testMC()

	mcfgclient := fakemcfgclient.NewSimpleClientset()
	dh := &fakeDegradedHandler{}

	moscLister := &fakeMOSCListerForSelector{items: []*mcfgv1.MachineOSConfig{mosc}}
	mutableMOSB := &mutableMOSBLister{}
	mcpLister := &fakeMCPListerForSelector{items: []*mcfgv1.MachineConfigPool{mcp}}
	mcLister := &fakeMCListerForSelector{items: []*mcfgv1.MachineConfig{mc}}

	poolR := NewPoolReconciler(mcfgclient, mcpLister, moscLister, mutableMOSB, mcLister,
		services.NewNoopEventRecorder(), services.NewNoopMetricsRecorder(), dh,
		&utils.Listers{
			MachineOSBuildLister:    mutableMOSB,
			MachineOSConfigLister:   moscLister,
			MachineConfigPoolLister: mcpLister,
		},
	)

	ctx := context.Background()

	// First reconcile creates the MOSB.
	if err := poolR.ReconcilePool(ctx, testPool); err != nil {
		t.Fatalf("first ReconcilePool: %v", err)
	}

	// Add created MOSB to lister so second call sees it.
	mosbList, _ := mcfgclient.MachineconfigurationV1().MachineOSBuilds().List(ctx, metav1.ListOptions{})
	for i := range mosbList.Items {
		mutableMOSB.add(&mosbList.Items[i])
	}

	// Second reconcile should be a no-op.
	if err := poolR.ReconcilePool(ctx, testPool); err != nil {
		t.Fatalf("second ReconcilePool: %v", err)
	}

	mosbList2, _ := mcfgclient.MachineconfigurationV1().MachineOSBuilds().List(ctx, metav1.ListOptions{})
	if len(mosbList2.Items) != 1 {
		t.Errorf("expected 1 MOSB after idempotent reconcile, got %d", len(mosbList2.Items))
	}
}

// ---------------------------------------------------------------------------
// Integration-only fakes (not duplicated from other test files)
// ---------------------------------------------------------------------------

// integrationJobLister satisfies batchlisterv1.JobLister for integration tests.
type integrationJobLister struct {
	items []*batchv1.Job
}

func (f *integrationJobLister) List(_ labels.Selector) ([]*batchv1.Job, error) {
	return f.items, nil
}

func (f *integrationJobLister) Jobs(namespace string) batchlisterv1.JobNamespaceLister {
	return &integrationJobNamespaceLister{items: f.items, namespace: namespace}
}

func (f *integrationJobLister) GetPodJobs(_ *corev1.Pod) ([]batchv1.Job, error) {
	return nil, nil
}

type integrationJobNamespaceLister struct {
	items     []*batchv1.Job
	namespace string
}

func (f *integrationJobNamespaceLister) List(_ labels.Selector) ([]*batchv1.Job, error) {
	var ret []*batchv1.Job
	for _, j := range f.items {
		if j.Namespace == f.namespace {
			ret = append(ret, j)
		}
	}
	return ret, nil
}

func (f *integrationJobNamespaceLister) Get(name string) (*batchv1.Job, error) {
	for _, j := range f.items {
		if j.Namespace == f.namespace && j.Name == name {
			return j, nil
		}
	}
	return nil, notFound("Job", name)
}
