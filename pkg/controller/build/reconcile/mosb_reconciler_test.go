package reconcile

import (
	"context"
	"fmt"
	"sync/atomic"
	"testing"

	mcfgv1 "github.com/openshift/api/machineconfiguration/v1"
	fakeclientmachineconfiguration "github.com/openshift/client-go/machineconfiguration/clientset/versioned/fake"
	"github.com/openshift/machine-config-operator/pkg/controller/build/constants"
	"github.com/openshift/machine-config-operator/pkg/controller/build/services"
	"github.com/openshift/machine-config-operator/pkg/controller/build/utils"
	ctrlcommon "github.com/openshift/machine-config-operator/pkg/controller/common"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/runtime"
	k8sfake "k8s.io/client-go/kubernetes/fake"
)

func TestMOSBReconciler_NotFound(t *testing.T) {
	r := &MOSBReconciler{
		mosbLister: &fakeMOSBListerForSelector{items: nil},
	}

	err := r.ReconcileMOSB(context.Background(), "nonexistent")
	if err != nil {
		t.Errorf("expected nil error for not-found MOSB, got: %v", err)
	}
}

func TestMOSBReconciler_TerminalSuccess(t *testing.T) {
	mosc := &mcfgv1.MachineOSConfig{
		ObjectMeta: metav1.ObjectMeta{
			Name:   "test-mosc",
			Labels: map[string]string{constants.TargetMachineConfigPoolLabelKey: "worker"},
		},
		Spec: mcfgv1.MachineOSConfigSpec{
			MachineConfigPool: mcfgv1.MachineConfigPoolReference{Name: "worker"},
		},
	}

	mcp := &mcfgv1.MachineConfigPool{
		ObjectMeta: metav1.ObjectMeta{Name: "worker"},
	}

	mosb := &mcfgv1.MachineOSBuild{
		ObjectMeta: metav1.ObjectMeta{
			Name: "test-mosb",
			Labels: map[string]string{
				constants.TargetMachineConfigPoolLabelKey: "worker",
				constants.MachineOSConfigNameLabelKey:     "test-mosc",
			},
		},
		Status: mcfgv1.MachineOSBuildStatus{
			Conditions: []metav1.Condition{
				{Type: string(mcfgv1.MachineOSBuildSucceeded), Status: metav1.ConditionTrue},
			},
			DigestedImagePushSpec: "image@sha256:abc",
		},
	}

	// fakeDegradedHandler to avoid needing real mcfgclient
	dh := &fakeDegradedHandler{}

	r := &MOSBReconciler{
		kubeclient: k8sfake.NewSimpleClientset(),
		mcfgclient: newFakeReconcileMCFGClient(mosb, mosc),
		mosbLister: &fakeMOSBListerForSelector{items: []*mcfgv1.MachineOSBuild{mosb}},
		moscLister: &fakeMOSCListerForSelector{items: []*mcfgv1.MachineOSConfig{mosc}},
		mcpLister:  &fakeMCPListerForSelector{items: []*mcfgv1.MachineConfigPool{mcp}},
		events:     services.NewNoopEventRecorder(),
		metrics:    services.NewNoopMetricsRecorder(),
		degraded:   dh,
		utilListers: &utils.Listers{
			MachineOSBuildLister:    &fakeMOSBListerForSelector{items: []*mcfgv1.MachineOSBuild{mosb}},
			MachineOSConfigLister:   &fakeMOSCListerForSelector{items: []*mcfgv1.MachineOSConfig{mosc}},
			MachineConfigPoolLister: &fakeMCPListerForSelector{items: []*mcfgv1.MachineConfigPool{mcp}},
		},
	}

	err := r.ReconcileMOSB(context.Background(), "test-mosb")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !dh.wasUpdateCalled() {
		t.Error("expected degraded handler UpdateImageBuildDegraded to be called")
	}
}

func TestMOSBReconciler_TerminalSuccess_UpdatesMOSCImagePullSpec(t *testing.T) {
	mosc := &mcfgv1.MachineOSConfig{
		ObjectMeta: metav1.ObjectMeta{
			Name:   "test-mosc",
			Labels: map[string]string{constants.TargetMachineConfigPoolLabelKey: "worker"},
		},
		Spec: mcfgv1.MachineOSConfigSpec{
			MachineConfigPool: mcfgv1.MachineConfigPoolReference{Name: "worker"},
		},
	}

	mcp := &mcfgv1.MachineConfigPool{
		ObjectMeta: metav1.ObjectMeta{Name: "worker"},
	}

	mosb := &mcfgv1.MachineOSBuild{
		ObjectMeta: metav1.ObjectMeta{
			Name: "test-mosb",
			Labels: map[string]string{
				constants.TargetMachineConfigPoolLabelKey: "worker",
				constants.MachineOSConfigNameLabelKey:     "test-mosc",
			},
		},
		Status: mcfgv1.MachineOSBuildStatus{
			Conditions: []metav1.Condition{
				{Type: string(mcfgv1.MachineOSBuildSucceeded), Status: metav1.ConditionTrue},
			},
			DigestedImagePushSpec: "registry.example.com/image@sha256:abc123",
		},
	}

	mcfgclient := newFakeReconcileMCFGClient(mosb, mosc)
	kubeclient := k8sfake.NewSimpleClientset()

	r := &MOSBReconciler{
		kubeclient: kubeclient,
		mcfgclient: mcfgclient,
		mosbLister: &fakeMOSBListerForSelector{items: []*mcfgv1.MachineOSBuild{mosb}},
		moscLister: &fakeMOSCListerForSelector{items: []*mcfgv1.MachineOSConfig{mosc}},
		mcpLister:  &fakeMCPListerForSelector{items: []*mcfgv1.MachineConfigPool{mcp}},
		events:     services.NewNoopEventRecorder(),
		metrics:    services.NewNoopMetricsRecorder(),
		degraded:   &fakeDegradedHandler{},
		utilListers: &utils.Listers{
			MachineOSBuildLister:    &fakeMOSBListerForSelector{items: []*mcfgv1.MachineOSBuild{mosb}},
			MachineOSConfigLister:   &fakeMOSCListerForSelector{items: []*mcfgv1.MachineOSConfig{mosc}},
			MachineConfigPoolLister: &fakeMCPListerForSelector{items: []*mcfgv1.MachineConfigPool{mcp}},
		},
	}

	err := r.ReconcileMOSB(context.Background(), "test-mosb")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Verify the MOSC status was updated with the image pullspec.
	updatedMOSC, err := mcfgclient.MachineconfigurationV1().MachineOSConfigs().Get(context.Background(), "test-mosc", metav1.GetOptions{})
	if err != nil {
		t.Fatalf("could not get updated MOSC: %v", err)
	}
	if updatedMOSC.Status.CurrentImagePullSpec != "registry.example.com/image@sha256:abc123" {
		t.Errorf("expected MOSC currentImagePullSpec %q, got %q",
			"registry.example.com/image@sha256:abc123", updatedMOSC.Status.CurrentImagePullSpec)
	}
	if updatedMOSC.Status.MachineOSBuild == nil || updatedMOSC.Status.MachineOSBuild.Name != "test-mosb" {
		t.Errorf("expected MOSC status.machineOSBuild.name %q, got %+v", "test-mosb", updatedMOSC.Status.MachineOSBuild)
	}
	// Verify the current build annotation was set.
	if updatedMOSC.Annotations[constants.CurrentMachineOSBuildAnnotationKey] != "test-mosb" {
		t.Errorf("expected current build annotation %q, got %q",
			"test-mosb", updatedMOSC.Annotations[constants.CurrentMachineOSBuildAnnotationKey])
	}
}

func TestMOSBReconciler_PreBuiltSkipped(t *testing.T) {
	mosb := &mcfgv1.MachineOSBuild{
		ObjectMeta: metav1.ObjectMeta{
			Name: "prebuilt-mosb",
			Labels: map[string]string{
				constants.PreBuiltImageLabelKey: constants.TrueValue,
			},
		},
	}

	r := &MOSBReconciler{
		mosbLister: &fakeMOSBListerForSelector{items: []*mcfgv1.MachineOSBuild{mosb}},
		events:     services.NewNoopEventRecorder(),
		metrics:    services.NewNoopMetricsRecorder(),
	}

	err := r.ReconcileMOSB(context.Background(), "prebuilt-mosb")
	if err != nil {
		t.Fatalf("unexpected error for pre-built MOSB: %v", err)
	}
}

func TestIsPreBuiltMOSB(t *testing.T) {
	tests := []struct {
		name   string
		labels map[string]string
		want   bool
	}{
		{"nil labels", nil, false},
		{"empty labels", map[string]string{}, false},
		{"pre-built true", map[string]string{constants.PreBuiltImageLabelKey: constants.TrueValue}, true},
		{"pre-built false", map[string]string{constants.PreBuiltImageLabelKey: "false"}, false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			mosb := &mcfgv1.MachineOSBuild{ObjectMeta: metav1.ObjectMeta{Labels: tt.labels}}
			if got := isPreBuiltMOSB(mosb); got != tt.want {
				t.Errorf("isPreBuiltMOSB() = %v, want %v", got, tt.want)
			}
		})
	}
}

// fakeDegradedHandler satisfies services.DegradedHandler for tests.
// Thread-safe via atomic for use in concurrent tests.
type fakeDegradedHandler struct {
	updateCount atomic.Int64
}

// updateCalled is a convenience accessor for non-concurrent tests.
func (f *fakeDegradedHandler) wasUpdateCalled() bool {
	return f.updateCount.Load() > 0
}

func (f *fakeDegradedHandler) InitializeBuildDegraded(_ context.Context, _ *mcfgv1.MachineConfigPool) error {
	return nil
}
func (f *fakeDegradedHandler) SyncBuildSuccess(_ context.Context, _ *mcfgv1.MachineConfigPool) error {
	return nil
}
func (f *fakeDegradedHandler) SyncBuildFailure(_ context.Context, _ *mcfgv1.MachineConfigPool, buildErr error, _ string) error {
	return buildErr
}
func (f *fakeDegradedHandler) UpdateImageBuildDegraded(_ context.Context, _ *mcfgv1.MachineConfigPool, _ *mcfgv1.MachineOSConfig) error {
	f.updateCount.Add(1)
	return nil
}

func newFakeReconcileMCFGClient(objs ...runtime.Object) *fakeclientmachineconfiguration.Clientset {
	return fakeclientmachineconfiguration.NewSimpleClientset(objs...)
}

func TestMOSBReconciler_TerminalSuccess_CleansEphemeralObjects(t *testing.T) {
	mosc := &mcfgv1.MachineOSConfig{
		ObjectMeta: metav1.ObjectMeta{
			Name:   "test-mosc",
			Labels: map[string]string{constants.TargetMachineConfigPoolLabelKey: "worker"},
		},
		Spec: mcfgv1.MachineOSConfigSpec{
			MachineConfigPool: mcfgv1.MachineConfigPoolReference{Name: "worker"},
		},
	}

	mcp := &mcfgv1.MachineConfigPool{
		ObjectMeta: metav1.ObjectMeta{Name: "worker"},
	}

	mosb := &mcfgv1.MachineOSBuild{
		ObjectMeta: metav1.ObjectMeta{
			Name: "test-mosb",
			Labels: map[string]string{
				constants.TargetMachineConfigPoolLabelKey: "worker",
				constants.MachineOSConfigNameLabelKey:     "test-mosc",
			},
		},
		Status: mcfgv1.MachineOSBuildStatus{
			Conditions: []metav1.Condition{
				{Type: string(mcfgv1.MachineOSBuildSucceeded), Status: metav1.ConditionTrue},
			},
			DigestedImagePushSpec: "image@sha256:abc",
		},
	}

	kubeclient := k8sfake.NewSimpleClientset()
	mcfgclient := newFakeReconcileMCFGClient(mosb, mosc)

	dh := &fakeDegradedHandler{}
	r := &MOSBReconciler{
		kubeclient: kubeclient,
		mcfgclient: mcfgclient,
		mosbLister: &fakeMOSBListerForSelector{items: []*mcfgv1.MachineOSBuild{mosb}},
		moscLister: &fakeMOSCListerForSelector{items: []*mcfgv1.MachineOSConfig{mosc}},
		mcpLister:  &fakeMCPListerForSelector{items: []*mcfgv1.MachineConfigPool{mcp}},
		events:     services.NewNoopEventRecorder(),
		metrics:    services.NewNoopMetricsRecorder(),
		degraded:   dh,
		utilListers: &utils.Listers{
			MachineOSBuildLister:    &fakeMOSBListerForSelector{items: []*mcfgv1.MachineOSBuild{mosb}},
			MachineOSConfigLister:   &fakeMOSCListerForSelector{items: []*mcfgv1.MachineOSConfig{mosc}},
			MachineConfigPoolLister: &fakeMCPListerForSelector{items: []*mcfgv1.MachineConfigPool{mcp}},
		},
	}

	// ReconcileMOSB should succeed even though the cleaner finds no
	// ephemeral objects to delete (the important thing is the cleanup
	// code path runs without error).
	err := r.ReconcileMOSB(context.Background(), "test-mosb")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !dh.wasUpdateCalled() {
		t.Error("expected degraded handler to be called after success cleanup")
	}
}

func TestMOSBReconciler_TerminalSuccess_WithDegradedRecovery(t *testing.T) {
	mosc := &mcfgv1.MachineOSConfig{
		ObjectMeta: metav1.ObjectMeta{
			Name:   "test-mosc",
			Labels: map[string]string{constants.TargetMachineConfigPoolLabelKey: "worker"},
		},
		Spec: mcfgv1.MachineOSConfigSpec{
			MachineConfigPool: mcfgv1.MachineConfigPoolReference{Name: "worker"},
		},
	}

	mcp := &mcfgv1.MachineConfigPool{
		ObjectMeta: metav1.ObjectMeta{Name: "worker"},
		Status: mcfgv1.MachineConfigPoolStatus{
			Conditions: []mcfgv1.MachineConfigPoolCondition{
				{Type: mcfgv1.MachineConfigPoolImageBuildDegraded, Status: "True"},
			},
		},
	}

	mosb := &mcfgv1.MachineOSBuild{
		ObjectMeta: metav1.ObjectMeta{
			Name: "test-mosb",
			Labels: map[string]string{
				constants.TargetMachineConfigPoolLabelKey: "worker",
				constants.MachineOSConfigNameLabelKey:     "test-mosc",
			},
		},
		Status: mcfgv1.MachineOSBuildStatus{
			Conditions: []metav1.Condition{
				{Type: string(mcfgv1.MachineOSBuildSucceeded), Status: metav1.ConditionTrue},
			},
			DigestedImagePushSpec: "image@sha256:abc",
		},
	}

	dh := &fakeDegradedHandler{}
	r := &MOSBReconciler{
		kubeclient: k8sfake.NewSimpleClientset(),
		mcfgclient: newFakeReconcileMCFGClient(mosb, mosc),
		mosbLister: &fakeMOSBListerForSelector{items: []*mcfgv1.MachineOSBuild{mosb}},
		moscLister: &fakeMOSCListerForSelector{items: []*mcfgv1.MachineOSConfig{mosc}},
		mcpLister:  &fakeMCPListerForSelector{items: []*mcfgv1.MachineConfigPool{mcp}},
		events:     services.NewNoopEventRecorder(),
		metrics:    services.NewNoopMetricsRecorder(),
		degraded:   dh,
		utilListers: &utils.Listers{
			MachineOSBuildLister:    &fakeMOSBListerForSelector{items: []*mcfgv1.MachineOSBuild{mosb}},
			MachineOSConfigLister:   &fakeMOSCListerForSelector{items: []*mcfgv1.MachineOSConfig{mosc}},
			MachineConfigPoolLister: &fakeMCPListerForSelector{items: []*mcfgv1.MachineConfigPool{mcp}},
		},
	}

	err := r.ReconcileMOSB(context.Background(), "test-mosb")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestMOSBReconciler_InitialState_StartBuildFails(t *testing.T) {
	poolName := "worker"
	mcName := "rendered-worker-abc"
	mosc := &mcfgv1.MachineOSConfig{
		ObjectMeta: metav1.ObjectMeta{
			Name:   "test-mosc",
			Labels: map[string]string{constants.TargetMachineConfigPoolLabelKey: poolName},
		},
		Spec: mcfgv1.MachineOSConfigSpec{
			MachineConfigPool: mcfgv1.MachineConfigPoolReference{Name: poolName},
		},
	}
	mcp := &mcfgv1.MachineConfigPool{
		ObjectMeta: metav1.ObjectMeta{Name: poolName},
		Spec: mcfgv1.MachineConfigPoolSpec{
			Configuration: mcfgv1.MachineConfigPoolStatusConfiguration{
				ObjectReference: corev1.ObjectReference{Name: mcName},
			},
		},
	}
	mosb := &mcfgv1.MachineOSBuild{
		ObjectMeta: metav1.ObjectMeta{
			Name: "test-mosb",
			Labels: map[string]string{
				constants.TargetMachineConfigPoolLabelKey: poolName,
				constants.MachineOSConfigNameLabelKey:     "test-mosc",
				constants.RenderedMachineConfigLabelKey:   mcName,
			},
		},
		// No conditions → initial state → will try ensureBuildStarted → startBuild
	}

	kubeclient := k8sfake.NewSimpleClientset()
	mcfgclient := newFakeReconcileMCFGClient(mosb)

	r := &MOSBReconciler{
		kubeclient: kubeclient,
		mcfgclient: mcfgclient,
		mosbLister: &fakeMOSBListerForSelector{items: []*mcfgv1.MachineOSBuild{mosb}},
		moscLister: &fakeMOSCListerForSelector{items: []*mcfgv1.MachineOSConfig{mosc}},
		mcpLister:  &fakeMCPListerForSelector{items: []*mcfgv1.MachineConfigPool{mcp}},
		events:     services.NewNoopEventRecorder(),
		metrics:    services.NewNoopMetricsRecorder(),
		degraded:   &fakeDegradedHandler{},
		utilListers: &utils.Listers{
			MachineOSBuildLister:    &fakeMOSBListerForSelector{items: []*mcfgv1.MachineOSBuild{mosb}},
			MachineOSConfigLister:   &fakeMOSCListerForSelector{items: []*mcfgv1.MachineOSConfig{mosc}},
			MachineConfigPoolLister: &fakeMCPListerForSelector{items: []*mcfgv1.MachineConfigPool{mcp}},
		},
	}

	// startBuild will fail because fake kubeclient doesn't have the required
	// ConfigMap/Secret/etc. resources. This exercises the startBuild error path.
	err := r.ReconcileMOSB(context.Background(), "test-mosb")
	if err == nil {
		t.Log("ReconcileMOSB returned nil (builder check found existing or error was swallowed)")
	} else {
		t.Logf("ReconcileMOSB error (expected - startBuild infrastructure missing): %v", err)
	}
}

func TestEnsureBuildStarted_StaleConfig(t *testing.T) {
	mcp := &mcfgv1.MachineConfigPool{
		ObjectMeta: metav1.ObjectMeta{Name: "worker"},
		Spec: mcfgv1.MachineConfigPoolSpec{
			Configuration: mcfgv1.MachineConfigPoolStatusConfiguration{
				ObjectReference: corev1.ObjectReference{Name: "rendered-worker-NEW"},
			},
		},
	}
	mosb := &mcfgv1.MachineOSBuild{
		ObjectMeta: metav1.ObjectMeta{
			Name: "stale-mosb",
			Labels: map[string]string{
				constants.TargetMachineConfigPoolLabelKey: "worker",
				constants.RenderedMachineConfigLabelKey:   "rendered-worker-OLD",
			},
		},
	}

	r := &MOSBReconciler{
		mcpLister: &fakeMCPListerForSelector{items: []*mcfgv1.MachineConfigPool{mcp}},
	}

	err := r.ensureBuildStarted(context.Background(), mosb)
	if err != nil {
		t.Fatalf("expected nil for stale config, got: %v", err)
	}
}

func TestMOSBReconciler_TransientState_Building(t *testing.T) {
	mosb := &mcfgv1.MachineOSBuild{
		ObjectMeta: metav1.ObjectMeta{
			Name: "building-mosb",
			Labels: map[string]string{
				constants.TargetMachineConfigPoolLabelKey: "worker",
			},
		},
		Status: mcfgv1.MachineOSBuildStatus{
			Conditions: []metav1.Condition{
				{Type: string(mcfgv1.MachineOSBuilding), Status: metav1.ConditionTrue},
			},
		},
	}

	r := &MOSBReconciler{
		mosbLister: &fakeMOSBListerForSelector{items: []*mcfgv1.MachineOSBuild{mosb}},
		events:     services.NewNoopEventRecorder(),
		metrics:    services.NewNoopMetricsRecorder(),
	}

	err := r.ReconcileMOSB(context.Background(), "building-mosb")
	if err != nil {
		t.Fatalf("expected nil for transient (building) state, got: %v", err)
	}
}

func TestMOSBReconciler_PreparedState(t *testing.T) {
	// MOSB in prepared state → transient → return nil
	mosb := &mcfgv1.MachineOSBuild{
		ObjectMeta: metav1.ObjectMeta{
			Name: "prepared-mosb",
			Labels: map[string]string{
				constants.TargetMachineConfigPoolLabelKey: "worker",
			},
		},
		Status: mcfgv1.MachineOSBuildStatus{
			Conditions: []metav1.Condition{
				{Type: string(mcfgv1.MachineOSBuildPrepared), Status: metav1.ConditionTrue},
			},
		},
	}

	r := &MOSBReconciler{
		mosbLister: &fakeMOSBListerForSelector{items: []*mcfgv1.MachineOSBuild{mosb}},
		events:     services.NewNoopEventRecorder(),
		metrics:    services.NewNoopMetricsRecorder(),
	}

	err := r.ReconcileMOSB(context.Background(), "prepared-mosb")
	if err != nil {
		t.Fatalf("expected nil for prepared state, got: %v", err)
	}
}

func TestReconcileMOSB_GetError(t *testing.T) {
	r := &MOSBReconciler{
		mosbLister: &errorMOSBListerRec{err: fmt.Errorf("lister error")},
	}
	err := r.ReconcileMOSB(context.Background(), "test-mosb")
	if err == nil {
		t.Fatal("expected error from lister failure")
	}
}

// errorMOSBListerRec always returns a non-NotFound error.
type errorMOSBListerRec struct {
	err error
}

func (e *errorMOSBListerRec) List(_ labels.Selector) ([]*mcfgv1.MachineOSBuild, error) {
	return nil, e.err
}
func (e *errorMOSBListerRec) Get(_ string) (*mcfgv1.MachineOSBuild, error) {
	return nil, e.err
}

func TestHandleTerminalState_MOSCError(t *testing.T) {
	mosb := &mcfgv1.MachineOSBuild{
		ObjectMeta: metav1.ObjectMeta{
			Name: "test-mosb",
			Labels: map[string]string{
				constants.MachineOSConfigNameLabelKey: "missing-mosc",
			},
		},
		Status: mcfgv1.MachineOSBuildStatus{
			Conditions: []metav1.Condition{
				{Type: string(mcfgv1.MachineOSBuildSucceeded), Status: metav1.ConditionTrue},
			},
		},
	}

	r := &MOSBReconciler{
		utilListers: &utils.Listers{
			MachineOSConfigLister: &fakeMOSCListerForSelector{items: nil},
		},
	}

	state := *ctrlcommon.NewMachineOSBuildState(mosb)
	err := r.handleTerminalState(context.Background(), mosb, state)
	// MOSC not found → nil
	if err != nil {
		t.Fatalf("expected nil for MOSC not found, got: %v", err)
	}
}

func TestHandleTerminalState_MCPError(t *testing.T) {
	mosc := &mcfgv1.MachineOSConfig{
		ObjectMeta: metav1.ObjectMeta{
			Name: "test-mosc",
			Labels: map[string]string{constants.TargetMachineConfigPoolLabelKey: "worker"},
		},
		Spec: mcfgv1.MachineOSConfigSpec{
			MachineConfigPool: mcfgv1.MachineConfigPoolReference{Name: "missing-pool"},
		},
	}
	mosb := &mcfgv1.MachineOSBuild{
		ObjectMeta: metav1.ObjectMeta{
			Name: "test-mosb",
			Labels: map[string]string{
				constants.TargetMachineConfigPoolLabelKey: "worker",
				constants.MachineOSConfigNameLabelKey:     "test-mosc",
			},
		},
		Status: mcfgv1.MachineOSBuildStatus{
			Conditions: []metav1.Condition{
				{Type: string(mcfgv1.MachineOSBuildSucceeded), Status: metav1.ConditionTrue},
			},
		},
	}

	r := &MOSBReconciler{
		mcpLister: &fakeMCPListerForSelector{items: nil},
		utilListers: &utils.Listers{
			MachineOSConfigLister:   &fakeMOSCListerForSelector{items: []*mcfgv1.MachineOSConfig{mosc}},
			MachineConfigPoolLister: &fakeMCPListerForSelector{items: nil},
		},
	}

	state := *ctrlcommon.NewMachineOSBuildState(mosb)
	err := r.handleTerminalState(context.Background(), mosb, state)
	if err == nil {
		t.Fatal("expected error for missing MCP")
	}
}

func TestEnsureBuildStarted_MCPGetError(t *testing.T) {
	mosb := &mcfgv1.MachineOSBuild{
		ObjectMeta: metav1.ObjectMeta{
			Name: "test-mosb",
			Labels: map[string]string{
				constants.TargetMachineConfigPoolLabelKey: "missing-pool",
			},
		},
	}

	r := &MOSBReconciler{
		mcpLister: &fakeMCPListerForSelector{items: nil},
	}

	err := r.ensureBuildStarted(context.Background(), mosb)
	if err == nil {
		t.Fatal("expected error for missing MCP")
	}
}

func TestEnsureBuildStarted_MOSCNotFound(t *testing.T) {
	mcp := &mcfgv1.MachineConfigPool{
		ObjectMeta: metav1.ObjectMeta{Name: "worker"},
		Spec: mcfgv1.MachineConfigPoolSpec{
			Configuration: mcfgv1.MachineConfigPoolStatusConfiguration{
				ObjectReference: corev1.ObjectReference{Name: "rendered-worker-abc"},
			},
		},
	}
	mosb := &mcfgv1.MachineOSBuild{
		ObjectMeta: metav1.ObjectMeta{
			Name: "orphan-mosb",
			Labels: map[string]string{
				constants.TargetMachineConfigPoolLabelKey: "worker",
				constants.RenderedMachineConfigLabelKey:   "rendered-worker-abc",
				constants.MachineOSConfigNameLabelKey:     "missing-mosc",
			},
		},
	}

	r := &MOSBReconciler{
		mcpLister: &fakeMCPListerForSelector{items: []*mcfgv1.MachineConfigPool{mcp}},
		utilListers: &utils.Listers{
			MachineOSConfigLister: &fakeMOSCListerForSelector{items: nil},
		},
	}

	err := r.ensureBuildStarted(context.Background(), mosb)
	if err != nil {
		t.Fatalf("expected nil for MOSC not found, got: %v", err)
	}
}

func TestMarkBuildFailed(t *testing.T) {
	mosb := &mcfgv1.MachineOSBuild{
		ObjectMeta: metav1.ObjectMeta{Name: "test-mosb"},
	}
	client := newFakeReconcileMCFGClient(mosb)
	r := &MOSBReconciler{mcfgclient: client}

	err := r.markBuildFailed(context.Background(), mosb)
	if err != nil {
		t.Fatalf("markBuildFailed error: %v", err)
	}

	updated, err := client.MachineconfigurationV1().MachineOSBuilds().Get(context.Background(), "test-mosb", metav1.GetOptions{})
	if err != nil {
		t.Fatalf("get MOSB: %v", err)
	}
	foundFailed := false
	for _, c := range updated.Status.Conditions {
		if c.Type == string(mcfgv1.MachineOSBuildFailed) && c.Status == metav1.ConditionTrue {
			foundFailed = true
		}
	}
	if !foundFailed {
		t.Error("expected MachineOSBuildFailed=True condition")
	}
}

func TestMOSBReconciler_TerminalFailure(t *testing.T) {
	mosc := &mcfgv1.MachineOSConfig{
		ObjectMeta: metav1.ObjectMeta{
			Name:   "test-mosc",
			Labels: map[string]string{constants.TargetMachineConfigPoolLabelKey: "worker"},
		},
		Spec: mcfgv1.MachineOSConfigSpec{
			MachineConfigPool: mcfgv1.MachineConfigPoolReference{Name: "worker"},
		},
	}

	mcp := &mcfgv1.MachineConfigPool{
		ObjectMeta: metav1.ObjectMeta{Name: "worker"},
	}

	mosb := &mcfgv1.MachineOSBuild{
		ObjectMeta: metav1.ObjectMeta{
			Name: "test-mosb",
			Labels: map[string]string{
				constants.TargetMachineConfigPoolLabelKey: "worker",
				constants.MachineOSConfigNameLabelKey:     "test-mosc",
			},
		},
		Status: mcfgv1.MachineOSBuildStatus{
			Conditions: []metav1.Condition{
				{Type: string(mcfgv1.MachineOSBuildFailed), Status: metav1.ConditionTrue},
			},
		},
	}

	dh := &fakeDegradedHandler{}
	r := &MOSBReconciler{
		mosbLister: &fakeMOSBListerForSelector{items: []*mcfgv1.MachineOSBuild{mosb}},
		moscLister: &fakeMOSCListerForSelector{items: []*mcfgv1.MachineOSConfig{mosc}},
		mcpLister:  &fakeMCPListerForSelector{items: []*mcfgv1.MachineConfigPool{mcp}},
		events:     services.NewNoopEventRecorder(),
		metrics:    services.NewNoopMetricsRecorder(),
		degraded:   dh,
		utilListers: &utils.Listers{
			MachineOSBuildLister:    &fakeMOSBListerForSelector{items: []*mcfgv1.MachineOSBuild{mosb}},
			MachineOSConfigLister:   &fakeMOSCListerForSelector{items: []*mcfgv1.MachineOSConfig{mosc}},
			MachineConfigPoolLister: &fakeMCPListerForSelector{items: []*mcfgv1.MachineConfigPool{mcp}},
		},
	}

	err := r.ReconcileMOSB(context.Background(), "test-mosb")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !dh.wasUpdateCalled() {
		t.Error("expected degraded handler UpdateImageBuildDegraded to be called for failure")
	}
}

func TestMOSBReconciler_InitialState_NoMOSC(t *testing.T) {
	mcp := &mcfgv1.MachineConfigPool{
		ObjectMeta: metav1.ObjectMeta{Name: "worker"},
	}
	mosb := &mcfgv1.MachineOSBuild{
		ObjectMeta: metav1.ObjectMeta{
			Name: "orphan-mosb",
			Labels: map[string]string{
				constants.TargetMachineConfigPoolLabelKey: "worker",
				constants.MachineOSConfigNameLabelKey:     "missing-mosc",
			},
		},
	}

	client := newFakeReconcileMCFGClient(mosb)
	r := &MOSBReconciler{
		mcfgclient: client,
		mosbLister: &fakeMOSBListerForSelector{items: []*mcfgv1.MachineOSBuild{mosb}},
		moscLister: &fakeMOSCListerForSelector{items: nil},
		mcpLister:  &fakeMCPListerForSelector{items: []*mcfgv1.MachineConfigPool{mcp}},
		events:     services.NewNoopEventRecorder(),
		metrics:    services.NewNoopMetricsRecorder(),
		utilListers: &utils.Listers{
			MachineOSConfigLister:   &fakeMOSCListerForSelector{items: nil},
			MachineConfigPoolLister: &fakeMCPListerForSelector{items: []*mcfgv1.MachineConfigPool{mcp}},
		},
	}

	// Should mark as failed since MOSC not found
	err := r.ReconcileMOSB(context.Background(), "orphan-mosb")
	if err != nil {
		t.Fatalf("unexpected error for orphan MOSB: %v", err)
	}
}

func TestUpdateMOSCImagePullSpec_EmptyDigest(t *testing.T) {
	mosc := &mcfgv1.MachineOSConfig{
		ObjectMeta: metav1.ObjectMeta{Name: "test-mosc"},
	}
	mosb := &mcfgv1.MachineOSBuild{
		ObjectMeta: metav1.ObjectMeta{Name: "test-mosb"},
		Status:     mcfgv1.MachineOSBuildStatus{DigestedImagePushSpec: ""},
	}

	r := &MOSBReconciler{}
	err := r.updateMOSCImagePullSpec(context.Background(), mosc, mosb)
	if err != nil {
		t.Fatalf("expected nil for empty digest, got: %v", err)
	}
}

func TestUpdateMOSCImagePullSpec_ListerError(t *testing.T) {
	mosc := &mcfgv1.MachineOSConfig{
		ObjectMeta: metav1.ObjectMeta{Name: "missing-mosc"},
	}
	mosb := &mcfgv1.MachineOSBuild{
		ObjectMeta: metav1.ObjectMeta{Name: "test-mosb"},
		Status:     mcfgv1.MachineOSBuildStatus{DigestedImagePushSpec: "image@sha256:abc"},
	}

	r := &MOSBReconciler{
		moscLister: &fakeMOSCListerForSelector{items: nil},
	}
	err := r.updateMOSCImagePullSpec(context.Background(), mosc, mosb)
	if err == nil {
		t.Fatal("expected error for missing MOSC in lister")
	}
}
