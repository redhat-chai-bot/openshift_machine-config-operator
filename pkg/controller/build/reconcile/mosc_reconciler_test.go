package reconcile

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/containers/image/v5/types"
	mcfgv1 "github.com/openshift/api/machineconfiguration/v1"
	fakemcfgclient "github.com/openshift/client-go/machineconfiguration/clientset/versioned/fake"
	"github.com/openshift/machine-config-operator/pkg/controller/build/constants"
	"github.com/openshift/machine-config-operator/pkg/controller/build/services"
	ctrlcommon "github.com/openshift/machine-config-operator/pkg/controller/common"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/runtime"
	k8sfake "k8s.io/client-go/kubernetes/fake"
	k8stesting "k8s.io/client-go/testing"
)

// --- fake seeder ---

type fakeSeeder struct {
	shouldSeed    bool
	preBuiltImage string
	needsCleanup  bool
	seedErr       error
	seedCalled    bool
}

func (f *fakeSeeder) ShouldSeed(_ *mcfgv1.MachineOSConfig) bool { return f.shouldSeed }
func (f *fakeSeeder) GetPreBuiltImage(_ *mcfgv1.MachineOSConfig) (string, bool) {
	return f.preBuiltImage, f.preBuiltImage != ""
}
func (f *fakeSeeder) NeedsAnnotationCleanup(_ *mcfgv1.MachineOSConfig) bool { return f.needsCleanup }
func (f *fakeSeeder) Seed(_ context.Context, _ *mcfgv1.MachineOSConfig, _ string) error {
	f.seedCalled = true
	return f.seedErr
}

// --- fake reuse checker ---

type fakeReuseChecker struct {
	result services.ImageReuseResult
	err    error
}

func (f *fakeReuseChecker) InspectImage(_ context.Context, _ string, _ *mcfgv1.MachineOSBuild) (*types.ImageInspectInfo, error) {
	return nil, nil
}

func (f *fakeReuseChecker) EvaluateReuse(_ context.Context, _ *mcfgv1.MachineOSConfig, _ *mcfgv1.MachineOSBuild) (services.ImageReuseResult, error) {
	return f.result, f.err
}

// --- tests ---

func TestMOSCReconciler_NotFound(t *testing.T) {
	r := &MOSCReconciler{
		moscLister: &fakeMOSCListerForSelector{items: nil},
		seeder:     &fakeSeeder{},
	}

	err := r.ReconcileMOSC(context.Background(), "nonexistent")
	if err != nil {
		t.Errorf("expected nil error for not-found MOSC, got: %v", err)
	}
}

func TestMOSCReconciler_Deletion_CleansUpBuildResources(t *testing.T) {
	moscName := "deleted-mosc"

	// Create a fake kubeclient with an orphaned build Job labeled with
	// the deleted MOSC name.
	job := &batchv1.Job{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "build-job-orphan",
			Namespace: "openshift-machine-config-operator",
			Labels: map[string]string{
				constants.MachineOSConfigNameLabelKey: moscName,
			},
		},
	}
	kubeclient := k8sfake.NewSimpleClientset(job)

	r := &MOSCReconciler{
		moscLister: &fakeMOSCListerForSelector{items: nil}, // MOSC not found
		kubeclient: kubeclient,
		seeder:     &fakeSeeder{},
	}

	err := r.ReconcileMOSC(context.Background(), moscName)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Verify the orphaned Job was deleted.
	jobs, err := kubeclient.BatchV1().Jobs("openshift-machine-config-operator").List(
		context.Background(), metav1.ListOptions{})
	if err != nil {
		t.Fatalf("list jobs: %v", err)
	}
	if len(jobs.Items) != 0 {
		t.Errorf("expected 0 jobs after cleanup, got %d", len(jobs.Items))
	}
}

func TestMOSCReconciler_Seeding(t *testing.T) {
	mosc := &mcfgv1.MachineOSConfig{
		ObjectMeta: metav1.ObjectMeta{
			Name: "test-mosc",
			Annotations: map[string]string{
				constants.PreBuiltImageAnnotationKey: "image:latest",
			},
		},
	}

	seeder := &fakeSeeder{shouldSeed: true, preBuiltImage: "image:latest"}

	r := &MOSCReconciler{
		moscLister: &fakeMOSCListerForSelector{items: []*mcfgv1.MachineOSConfig{mosc}},
		seeder:     seeder,
		events:     services.NewNoopEventRecorder(),
		metrics:    services.NewNoopMetricsRecorder(),
	}

	err := r.ReconcileMOSC(context.Background(), "test-mosc")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !seeder.seedCalled {
		t.Error("expected Seed() to be called")
	}
}

func TestMOSCReconciler_AnnotationCleanup(t *testing.T) {
	mosc := &mcfgv1.MachineOSConfig{
		ObjectMeta: metav1.ObjectMeta{
			Name: "test-mosc",
			Annotations: map[string]string{
				constants.PreBuiltImageAnnotationKey:         "image:latest",
				constants.CurrentMachineOSBuildAnnotationKey: "build-1",
			},
		},
		Status: mcfgv1.MachineOSConfigStatus{
			CurrentImagePullSpec: "image@sha256:abc",
		},
	}

	mcfgclient := fakemcfgclient.NewSimpleClientset([]runtime.Object{mosc}...)

	r := &MOSCReconciler{
		mcfgclient: mcfgclient,
		moscLister: &fakeMOSCListerForSelector{items: []*mcfgv1.MachineOSConfig{mosc}},
		seeder:     &fakeSeeder{needsCleanup: true},
		events:     services.NewNoopEventRecorder(),
		metrics:    services.NewNoopMetricsRecorder(),
	}

	err := r.ReconcileMOSC(context.Background(), "test-mosc")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestMOSCReconciler_RebuildAnnotation(t *testing.T) {
	mosc := &mcfgv1.MachineOSConfig{
		ObjectMeta: metav1.ObjectMeta{
			Name: "test-mosc",
			Annotations: map[string]string{
				constants.RebuildMachineOSConfigAnnotationKey: "",
				constants.CurrentMachineOSBuildAnnotationKey:  "old-build",
			},
		},
		Spec: mcfgv1.MachineOSConfigSpec{
			MachineConfigPool:     mcfgv1.MachineConfigPoolReference{Name: "worker"},
			RenderedImagePushSpec: "registry.example.com/ocp:latest",
		},
	}

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

	mcfgclient := fakemcfgclient.NewSimpleClientset([]runtime.Object{mosc}...)

	r := &MOSCReconciler{
		mcfgclient: mcfgclient,
		moscLister: &fakeMOSCListerForSelector{items: []*mcfgv1.MachineOSConfig{mosc}},
		mosbLister: &fakeMOSBListerForSelector{items: nil},
		mcpLister:  &fakeMCPListerForSelector{items: []*mcfgv1.MachineConfigPool{mcp}},
		mcLister:   &fakeMCListerForSelector{items: []*mcfgv1.MachineConfig{mc}},
		seeder:     &fakeSeeder{},
		reuse:      &fakeReuseChecker{},
		events:     services.NewNoopEventRecorder(),
		metrics:    services.NewNoopMetricsRecorder(),
	}

	err := r.ReconcileMOSC(context.Background(), "test-mosc")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestHasRebuildAnnotation(t *testing.T) {
	tests := []struct {
		name        string
		annotations map[string]string
		want        bool
	}{
		{"nil", nil, false},
		{"empty", map[string]string{}, false},
		{"has", map[string]string{constants.RebuildMachineOSConfigAnnotationKey: ""}, true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			mosc := &mcfgv1.MachineOSConfig{ObjectMeta: metav1.ObjectMeta{Annotations: tt.annotations}}
			if got := hasRebuildAnnotation(mosc); got != tt.want {
				t.Errorf("hasRebuildAnnotation() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestIsMOSBCurrentForMOSC(t *testing.T) {
	mosc := &mcfgv1.MachineOSConfig{
		ObjectMeta: metav1.ObjectMeta{
			Annotations: map[string]string{
				constants.CurrentMachineOSBuildAnnotationKey: "build-1",
			},
		},
	}

	if !isMOSBCurrentForMOSC(mosc, &mcfgv1.MachineOSBuild{ObjectMeta: metav1.ObjectMeta{Name: "build-1"}}) {
		t.Error("expected true for matching build")
	}
	if isMOSBCurrentForMOSC(mosc, &mcfgv1.MachineOSBuild{ObjectMeta: metav1.ObjectMeta{Name: "build-2"}}) {
		t.Error("expected false for non-matching build")
	}
}

func TestUpdateMOSCStatus(t *testing.T) {
	mosc := &mcfgv1.MachineOSConfig{
		ObjectMeta: metav1.ObjectMeta{Name: "mosc-1"},
		Spec: mcfgv1.MachineOSConfigSpec{
			MachineConfigPool: mcfgv1.MachineConfigPoolReference{Name: "worker"},
		},
	}
	mosb := &mcfgv1.MachineOSBuild{
		ObjectMeta: metav1.ObjectMeta{Name: "mosb-1"},
		Status: mcfgv1.MachineOSBuildStatus{
			DigestedImagePushSpec: "image@sha256:abc",
		},
	}

	client := fakemcfgclient.NewSimpleClientset(mosc)
	r := &MOSCReconciler{
		mcfgclient: client,
		moscLister: &fakeMOSCListerForSelector{items: []*mcfgv1.MachineOSConfig{mosc}},
		events:     services.NewNoopEventRecorder(),
		metrics:    services.NewNoopMetricsRecorder(),
	}

	err := r.updateMOSCStatus(context.Background(), mosc, mosb)
	if err != nil {
		t.Fatalf("updateMOSCStatus error: %v", err)
	}
}

// TestUpdateMOSCStatus_NoResourceVersionConflict verifies that after
// Update() sets the annotation, the subsequent UpdateStatus() uses the
// updated object (with new resourceVersion) so both writes succeed
// without a conflict.
func TestUpdateMOSCStatus_NoResourceVersionConflict(t *testing.T) {
	mosc := &mcfgv1.MachineOSConfig{
		ObjectMeta: metav1.ObjectMeta{
			Name:            "mosc-rv",
			ResourceVersion: "100",
		},
		Spec: mcfgv1.MachineOSConfigSpec{
			MachineConfigPool: mcfgv1.MachineConfigPoolReference{Name: "worker"},
		},
	}
	mosb := &mcfgv1.MachineOSBuild{
		ObjectMeta: metav1.ObjectMeta{Name: "mosb-rv"},
		Status: mcfgv1.MachineOSBuildStatus{
			DigestedImagePushSpec: "image@sha256:deadbeef",
		},
	}

	client := fakemcfgclient.NewSimpleClientset(mosc)
	r := &MOSCReconciler{
		mcfgclient: client,
		moscLister: &fakeMOSCListerForSelector{items: []*mcfgv1.MachineOSConfig{mosc}},
		events:     services.NewNoopEventRecorder(),
		metrics:    services.NewNoopMetricsRecorder(),
	}

	err := r.updateMOSCStatus(context.Background(), mosc, mosb)
	if err != nil {
		t.Fatalf("updateMOSCStatus error: %v", err)
	}

	// Verify both the annotation and the status were set.
	updated, err := client.MachineconfigurationV1().MachineOSConfigs().Get(context.Background(), "mosc-rv", metav1.GetOptions{})
	if err != nil {
		t.Fatalf("get MOSC: %v", err)
	}
	if updated.Annotations[constants.CurrentMachineOSBuildAnnotationKey] != "mosb-rv" {
		t.Errorf("expected current build annotation %q, got %q", "mosb-rv", updated.Annotations[constants.CurrentMachineOSBuildAnnotationKey])
	}
	if updated.Status.CurrentImagePullSpec != "image@sha256:deadbeef" {
		t.Errorf("expected status pullspec %q, got %q", "image@sha256:deadbeef", updated.Status.CurrentImagePullSpec)
	}
	if updated.Status.MachineOSBuild == nil || updated.Status.MachineOSBuild.Name != "mosb-rv" {
		t.Errorf("expected status MachineOSBuild.Name %q, got %+v", "mosb-rv", updated.Status.MachineOSBuild)
	}
}

func TestEnsureBuildExists_AlreadyCurrent(t *testing.T) {
	mosc := &mcfgv1.MachineOSConfig{
		ObjectMeta: metav1.ObjectMeta{
			Name: "mosc-1",
			Annotations: map[string]string{
				constants.CurrentMachineOSBuildAnnotationKey: "existing-mosb",
			},
		},
		Spec: mcfgv1.MachineOSConfigSpec{
			MachineConfigPool: mcfgv1.MachineConfigPoolReference{Name: "worker"},
		},
	}
	mosb := &mcfgv1.MachineOSBuild{
		ObjectMeta: metav1.ObjectMeta{
			Name: "existing-mosb",
			Labels: map[string]string{
				constants.TargetMachineConfigPoolLabelKey: "worker",
				constants.MachineOSConfigNameLabelKey:     "mosc-1",
			},
		},
	}

	r := &MOSCReconciler{
		mosbLister: &fakeMOSBListerForSelector{items: []*mcfgv1.MachineOSBuild{mosb}},
		events:     services.NewNoopEventRecorder(),
		metrics:    services.NewNoopMetricsRecorder(),
	}

	err := r.ensureBuildExists(context.Background(), mosc)
	if err != nil {
		t.Fatalf("expected no error when build already exists, got: %v", err)
	}
}

func TestEnsureBuildExists_SeedingPending(t *testing.T) {
	mosc := &mcfgv1.MachineOSConfig{
		ObjectMeta: metav1.ObjectMeta{
			Name: "mosc-1",
			Annotations: map[string]string{
				constants.PreBuiltImageAnnotationKey: "image:latest",
			},
		},
		Spec: mcfgv1.MachineOSConfigSpec{
			MachineConfigPool: mcfgv1.MachineConfigPoolReference{Name: "worker"},
		},
	}

	fakeSeeder := services.NewSeeder(nil, nil, nil, nil)
	r := &MOSCReconciler{
		mosbLister: &fakeMOSBListerForSelector{items: nil},
		seeder:     fakeSeeder,
		events:     services.NewNoopEventRecorder(),
		metrics:    services.NewNoopMetricsRecorder(),
	}

	err := r.ensureBuildExists(context.Background(), mosc)
	if err != nil {
		t.Fatalf("expected no error when seeding pending, got: %v", err)
	}
}

func TestEnsureBuildExists_CreateNew(t *testing.T) {
	mosc := &mcfgv1.MachineOSConfig{
		ObjectMeta: metav1.ObjectMeta{Name: "mosc-1"},
		Spec: mcfgv1.MachineOSConfigSpec{
			MachineConfigPool:     mcfgv1.MachineConfigPoolReference{Name: "worker"},
			RenderedImagePushSpec: "registry.example.com/image",
		},
	}
	mc := &mcfgv1.MachineConfig{
		ObjectMeta: metav1.ObjectMeta{
			Name: "rendered-worker-abc",
			Annotations: map[string]string{
				ctrlcommon.ReleaseImageVersionAnnotationKey:          "4.19.0",
				ctrlcommon.GeneratedByControllerVersionAnnotationKey: "4.19.0",
			},
		},
	}
	mcp := &mcfgv1.MachineConfigPool{
		ObjectMeta: metav1.ObjectMeta{Name: "worker"},
		Spec: mcfgv1.MachineConfigPoolSpec{
			Configuration: mcfgv1.MachineConfigPoolStatusConfiguration{
				ObjectReference: corev1.ObjectReference{Name: "rendered-worker-abc"},
			},
		},
	}

	client := fakemcfgclient.NewSimpleClientset()
	fakeSeeder := services.NewSeeder(nil, nil, nil, nil)
	fakeReuse := &fakeReuseChecker{}

	r := &MOSCReconciler{
		mcfgclient: client,
		moscLister: &fakeMOSCListerForSelector{items: []*mcfgv1.MachineOSConfig{mosc}},
		mosbLister: &fakeMOSBListerForSelector{items: nil},
		mcpLister:  &fakeMCPListerForSelector{items: []*mcfgv1.MachineConfigPool{mcp}},
		mcLister:   &fakeMCListerForSelector{items: []*mcfgv1.MachineConfig{mc}},
		seeder:     fakeSeeder,
		reuse:      fakeReuse,
		events:     services.NewNoopEventRecorder(),
		metrics:    services.NewNoopMetricsRecorder(),
	}

	err := r.ensureBuildExists(context.Background(), mosc)
	if err != nil {
		t.Fatalf("ensureBuildExists error: %v", err)
	}
}

func TestHandleRebuild(t *testing.T) {
	mosc := &mcfgv1.MachineOSConfig{
		ObjectMeta: metav1.ObjectMeta{
			Name: "mosc-1",
			Annotations: map[string]string{
				constants.RebuildMachineOSConfigAnnotationKey: "true",
				constants.CurrentMachineOSBuildAnnotationKey:  "old-build",
			},
		},
		Spec: mcfgv1.MachineOSConfigSpec{
			MachineConfigPool:     mcfgv1.MachineConfigPoolReference{Name: "worker"},
			RenderedImagePushSpec: "registry.example.com/image",
		},
	}
	mc := &mcfgv1.MachineConfig{
		ObjectMeta: metav1.ObjectMeta{
			Name: "rendered-worker-abc",
			Annotations: map[string]string{
				ctrlcommon.ReleaseImageVersionAnnotationKey:          "4.19.0",
				ctrlcommon.GeneratedByControllerVersionAnnotationKey: "4.19.0",
			},
		},
	}
	mcp := &mcfgv1.MachineConfigPool{
		ObjectMeta: metav1.ObjectMeta{Name: "worker"},
		Spec: mcfgv1.MachineConfigPoolSpec{
			Configuration: mcfgv1.MachineConfigPoolStatusConfiguration{
				ObjectReference: corev1.ObjectReference{Name: "rendered-worker-abc"},
			},
		},
	}

	client := fakemcfgclient.NewSimpleClientset(mosc)
	r := &MOSCReconciler{
		mcfgclient: client,
		moscLister: &fakeMOSCListerForSelector{items: []*mcfgv1.MachineOSConfig{mosc}},
		mosbLister: &fakeMOSBListerForSelector{items: nil},
		mcpLister:  &fakeMCPListerForSelector{items: []*mcfgv1.MachineConfigPool{mcp}},
		mcLister:   &fakeMCListerForSelector{items: []*mcfgv1.MachineConfig{mc}},
		seeder:     services.NewSeeder(nil, nil, nil, nil),
		reuse:      &fakeReuseChecker{},
		events:     services.NewNoopEventRecorder(),
		metrics:    services.NewNoopMetricsRecorder(),
	}

	err := r.handleRebuild(context.Background(), mosc)
	if err != nil {
		t.Fatalf("handleRebuild error: %v", err)
	}

	// handleRebuild should create a new MOSB (the old one was deleted).
	mosbList, err := client.MachineconfigurationV1().MachineOSBuilds().List(context.Background(), metav1.ListOptions{})
	if err != nil {
		t.Fatalf("list MOSBs: %v", err)
	}
	if len(mosbList.Items) == 0 {
		t.Error("expected at least one MOSB to be created by handleRebuild")
	}
}

func TestEnsureBuildExists_ReusePath(t *testing.T) {
	mosc := &mcfgv1.MachineOSConfig{
		ObjectMeta: metav1.ObjectMeta{Name: "mosc-1"},
		Spec: mcfgv1.MachineOSConfigSpec{
			MachineConfigPool:     mcfgv1.MachineConfigPoolReference{Name: "worker"},
			RenderedImagePushSpec: "registry.example.com/image",
		},
	}
	mc := &mcfgv1.MachineConfig{
		ObjectMeta: metav1.ObjectMeta{
			Name: "rendered-worker-abc",
			Annotations: map[string]string{
				ctrlcommon.ReleaseImageVersionAnnotationKey:          "4.19.0",
				ctrlcommon.GeneratedByControllerVersionAnnotationKey: "4.19.0",
			},
		},
	}
	mcp := &mcfgv1.MachineConfigPool{
		ObjectMeta: metav1.ObjectMeta{Name: "worker"},
		Spec: mcfgv1.MachineConfigPoolSpec{
			Configuration: mcfgv1.MachineConfigPoolStatusConfiguration{
				ObjectReference: corev1.ObjectReference{Name: "rendered-worker-abc"},
			},
		},
	}

	// Build the expected MOSB name by generating it
	expectedMOSBName := "mosc-1-b4aa8faa5f63ec669c27bdf69eecefcd" // from prior test run
	existingMOSB := &mcfgv1.MachineOSBuild{
		ObjectMeta: metav1.ObjectMeta{
			Name: expectedMOSBName,
			Labels: map[string]string{
				constants.TargetMachineConfigPoolLabelKey: "worker",
				constants.MachineOSConfigNameLabelKey:     "mosc-1",
			},
		},
		Status: mcfgv1.MachineOSBuildStatus{
			Conditions: []metav1.Condition{
				{Type: string(mcfgv1.MachineOSBuildSucceeded), Status: metav1.ConditionTrue},
			},
			DigestedImagePushSpec: "image@sha256:abc",
		},
	}

	client := fakemcfgclient.NewSimpleClientset(mosc)
	r := &MOSCReconciler{
		mcfgclient: client,
		moscLister: &fakeMOSCListerForSelector{items: []*mcfgv1.MachineOSConfig{mosc}},
		mosbLister: &fakeMOSBListerForSelector{items: []*mcfgv1.MachineOSBuild{existingMOSB}},
		mcpLister:  &fakeMCPListerForSelector{items: []*mcfgv1.MachineConfigPool{mcp}},
		mcLister:   &fakeMCListerForSelector{items: []*mcfgv1.MachineConfig{mc}},
		seeder:     services.NewSeeder(nil, nil, nil, nil),
		reuse:      &fakeReuseChecker{result: services.ImageReuseResult{CanReuse: true}},
		events:     services.NewNoopEventRecorder(),
		metrics:    services.NewNoopMetricsRecorder(),
	}

	err := r.ensureBuildExists(context.Background(), mosc)
	if err != nil {
		t.Fatalf("ensureBuildExists error: %v", err)
	}
}

func TestEnsureBuildExists_ReuseNeedsRebuild(t *testing.T) {
	mosc := &mcfgv1.MachineOSConfig{
		ObjectMeta: metav1.ObjectMeta{Name: "mosc-1"},
		Spec: mcfgv1.MachineOSConfigSpec{
			MachineConfigPool:     mcfgv1.MachineConfigPoolReference{Name: "worker"},
			RenderedImagePushSpec: "registry.example.com/image",
		},
	}
	mc := &mcfgv1.MachineConfig{
		ObjectMeta: metav1.ObjectMeta{
			Name: "rendered-worker-abc",
			Annotations: map[string]string{
				ctrlcommon.ReleaseImageVersionAnnotationKey:          "4.19.0",
				ctrlcommon.GeneratedByControllerVersionAnnotationKey: "4.19.0",
			},
		},
	}
	mcp := &mcfgv1.MachineConfigPool{
		ObjectMeta: metav1.ObjectMeta{Name: "worker"},
		Spec: mcfgv1.MachineConfigPoolSpec{
			Configuration: mcfgv1.MachineConfigPoolStatusConfiguration{
				ObjectReference: corev1.ObjectReference{Name: "rendered-worker-abc"},
			},
		},
	}

	expectedMOSBName := "mosc-1-b4aa8faa5f63ec669c27bdf69eecefcd"
	existingMOSB := &mcfgv1.MachineOSBuild{
		ObjectMeta: metav1.ObjectMeta{
			Name: expectedMOSBName,
			Labels: map[string]string{
				constants.TargetMachineConfigPoolLabelKey: "worker",
				constants.MachineOSConfigNameLabelKey:     "mosc-1",
			},
		},
	}

	client := fakemcfgclient.NewSimpleClientset(existingMOSB)
	r := &MOSCReconciler{
		mcfgclient: client,
		moscLister: &fakeMOSCListerForSelector{items: []*mcfgv1.MachineOSConfig{mosc}},
		mosbLister: &fakeMOSBListerForSelector{items: []*mcfgv1.MachineOSBuild{existingMOSB}},
		mcpLister:  &fakeMCPListerForSelector{items: []*mcfgv1.MachineConfigPool{mcp}},
		mcLister:   &fakeMCListerForSelector{items: []*mcfgv1.MachineConfig{mc}},
		seeder:     services.NewSeeder(nil, nil, nil, nil),
		reuse:      &fakeReuseChecker{result: services.ImageReuseResult{NeedsRebuild: true}},
		events:     services.NewNoopEventRecorder(),
		metrics:    services.NewNoopMetricsRecorder(),
	}

	err := r.ensureBuildExists(context.Background(), mosc)
	if err != nil {
		t.Fatalf("ensureBuildExists error: %v", err)
	}
}

func TestCleanupPreBuiltAnnotation(t *testing.T) {
	mosc := &mcfgv1.MachineOSConfig{
		ObjectMeta: metav1.ObjectMeta{
			Name: "mosc-1",
			Annotations: map[string]string{
				constants.PreBuiltImageAnnotationKey:         "image:v1",
				constants.CurrentMachineOSBuildAnnotationKey: "mosb-1",
			},
		},
		Status: mcfgv1.MachineOSConfigStatus{
			CurrentImagePullSpec: "image@sha256:abc",
		},
	}

	client := fakemcfgclient.NewSimpleClientset(mosc)
	r := &MOSCReconciler{
		mcfgclient: client,
		events:     services.NewNoopEventRecorder(),
	}

	err := r.cleanupPreBuiltAnnotation(context.Background(), mosc)
	if err != nil {
		t.Fatalf("cleanupPreBuiltAnnotation error: %v", err)
	}

	updated, err := client.MachineconfigurationV1().MachineOSConfigs().Get(context.Background(), "mosc-1", metav1.GetOptions{})
	if err != nil {
		t.Fatalf("get mosc: %v", err)
	}
	if _, has := updated.Annotations[constants.PreBuiltImageAnnotationKey]; has {
		t.Error("expected pre-built annotation to be removed")
	}
}

func TestEnsureBuildExists_ListError(t *testing.T) {
	mosc := &mcfgv1.MachineOSConfig{
		ObjectMeta: metav1.ObjectMeta{Name: "mosc-1"},
		Spec: mcfgv1.MachineOSConfigSpec{
			MachineConfigPool: mcfgv1.MachineConfigPoolReference{Name: "worker"},
		},
	}

	errLister := &errorMOSBLister{err: fmt.Errorf("lister error")}
	r := &MOSCReconciler{
		mosbLister: errLister,
	}

	err := r.ensureBuildExists(context.Background(), mosc)
	if err == nil {
		t.Fatal("expected error from lister failure")
	}
}

// errorMOSBLister always returns an error.
type errorMOSBLister struct {
	err error
}

func (e *errorMOSBLister) List(_ labels.Selector) ([]*mcfgv1.MachineOSBuild, error) {
	return nil, e.err
}

func (e *errorMOSBLister) Get(_ string) (*mcfgv1.MachineOSBuild, error) {
	return nil, e.err
}

func TestReconcileMOSC_GetError(t *testing.T) {
	r := &MOSCReconciler{
		moscLister: &errorMOSCListerRec{err: fmt.Errorf("lister broken")},
	}
	err := r.ReconcileMOSC(context.Background(), "test-mosc")
	if err == nil {
		t.Fatal("expected error from lister failure")
	}
}

// errorMOSCListerRec always returns a non-NotFound error.
type errorMOSCListerRec struct {
	err error
}

func (e *errorMOSCListerRec) List(_ labels.Selector) ([]*mcfgv1.MachineOSConfig, error) {
	return nil, e.err
}
func (e *errorMOSCListerRec) Get(_ string) (*mcfgv1.MachineOSConfig, error) {
	return nil, e.err
}

func TestCleanupPreBuiltAnnotation_UpdateError(t *testing.T) {
	mosc := &mcfgv1.MachineOSConfig{
		ObjectMeta: metav1.ObjectMeta{
			Name: "mosc-1",
			Annotations: map[string]string{
				constants.PreBuiltImageAnnotationKey: "image:v1",
			},
		},
	}

	// Don't pre-create the mosc in the fake client → Update will return NotFound
	client := fakemcfgclient.NewSimpleClientset()
	r := &MOSCReconciler{
		mcfgclient: client,
		events:     services.NewNoopEventRecorder(),
	}

	err := r.cleanupPreBuiltAnnotation(context.Background(), mosc)
	if err == nil {
		t.Fatal("expected error from Update on non-existent MOSC")
	}
}

func TestHandleRebuild_NoBuildAnnotation(t *testing.T) {
	mosc := &mcfgv1.MachineOSConfig{
		ObjectMeta: metav1.ObjectMeta{
			Name: "mosc-1",
			Annotations: map[string]string{
				constants.RebuildMachineOSConfigAnnotationKey: "true",
				// No CurrentMachineOSBuildAnnotationKey
			},
		},
	}

	r := &MOSCReconciler{}

	err := r.handleRebuild(context.Background(), mosc)
	if err != nil {
		t.Fatalf("expected nil for missing current build annotation, got: %v", err)
	}
}

func TestEnsureBuildExists_ReuseEvaluateError(t *testing.T) {
	mosc := &mcfgv1.MachineOSConfig{
		ObjectMeta: metav1.ObjectMeta{Name: "mosc-1"},
		Spec: mcfgv1.MachineOSConfigSpec{
			MachineConfigPool:     mcfgv1.MachineConfigPoolReference{Name: "worker"},
			RenderedImagePushSpec: "registry.example.com/image",
		},
	}
	mc := &mcfgv1.MachineConfig{
		ObjectMeta: metav1.ObjectMeta{
			Name: "rendered-worker-abc",
			Annotations: map[string]string{
				ctrlcommon.ReleaseImageVersionAnnotationKey:          "4.19.0",
				ctrlcommon.GeneratedByControllerVersionAnnotationKey: "4.19.0",
			},
		},
	}
	mcp := &mcfgv1.MachineConfigPool{
		ObjectMeta: metav1.ObjectMeta{Name: "worker"},
		Spec: mcfgv1.MachineConfigPoolSpec{
			Configuration: mcfgv1.MachineConfigPoolStatusConfiguration{
				ObjectReference: corev1.ObjectReference{Name: "rendered-worker-abc"},
			},
		},
	}
	expectedMOSBName := "mosc-1-b4aa8faa5f63ec669c27bdf69eecefcd"
	existingMOSB := &mcfgv1.MachineOSBuild{
		ObjectMeta: metav1.ObjectMeta{
			Name: expectedMOSBName,
			Labels: map[string]string{
				constants.TargetMachineConfigPoolLabelKey: "worker",
				constants.MachineOSConfigNameLabelKey:     "mosc-1",
			},
		},
	}

	r := &MOSCReconciler{
		mosbLister: &fakeMOSBListerForSelector{items: []*mcfgv1.MachineOSBuild{existingMOSB}},
		mcpLister:  &fakeMCPListerForSelector{items: []*mcfgv1.MachineConfigPool{mcp}},
		mcLister:   &fakeMCListerForSelector{items: []*mcfgv1.MachineConfig{mc}},
		seeder:     services.NewSeeder(nil, nil, nil, nil),
		reuse:      &fakeReuseChecker{err: fmt.Errorf("reuse check failed")},
		events:     services.NewNoopEventRecorder(),
		metrics:    services.NewNoopMetricsRecorder(),
	}

	err := r.ensureBuildExists(context.Background(), mosc)
	if err == nil {
		t.Fatal("expected error from reuse evaluation failure")
	}
}

func TestEnsureBuildExists_DesiredMOSBError(t *testing.T) {
	mosc := &mcfgv1.MachineOSConfig{
		ObjectMeta: metav1.ObjectMeta{Name: "mosc-1"},
		Spec: mcfgv1.MachineOSConfigSpec{
			MachineConfigPool: mcfgv1.MachineConfigPoolReference{Name: "missing-pool"},
		},
	}

	r := &MOSCReconciler{
		mosbLister: &fakeMOSBListerForSelector{items: nil},
		mcpLister:  &fakeMCPListerForSelector{items: nil},
		seeder:     services.NewSeeder(nil, nil, nil, nil),
	}

	err := r.ensureBuildExists(context.Background(), mosc)
	if err == nil {
		t.Fatal("expected error when MCP not found for desired MOSB")
	}
}

func TestCreateMachineOSBuild_AlreadyExists(t *testing.T) {
	mosc := &mcfgv1.MachineOSConfig{
		ObjectMeta: metav1.ObjectMeta{Name: "mosc-1"},
		Spec: mcfgv1.MachineOSConfigSpec{
			MachineConfigPool:     mcfgv1.MachineConfigPoolReference{Name: "worker"},
			RenderedImagePushSpec: "registry.example.com/image",
		},
	}
	mc := &mcfgv1.MachineConfig{
		ObjectMeta: metav1.ObjectMeta{
			Name: "rendered-worker-abc",
			Annotations: map[string]string{
				ctrlcommon.ReleaseImageVersionAnnotationKey:          "4.19.0",
				ctrlcommon.GeneratedByControllerVersionAnnotationKey: "4.19.0",
			},
		},
	}
	mcp := &mcfgv1.MachineConfigPool{
		ObjectMeta: metav1.ObjectMeta{Name: "worker"},
		Spec: mcfgv1.MachineConfigPoolSpec{
			Configuration: mcfgv1.MachineConfigPoolStatusConfiguration{
				ObjectReference: corev1.ObjectReference{Name: "rendered-worker-abc"},
			},
		},
	}

	// Pre-create the expected MOSB name to trigger AlreadyExists.
	expectedMOSBName := "mosc-1-b4aa8faa5f63ec669c27bdf69eecefcd"
	existingMOSB := &mcfgv1.MachineOSBuild{
		ObjectMeta: metav1.ObjectMeta{Name: expectedMOSBName},
	}

	client := fakemcfgclient.NewSimpleClientset(existingMOSB)
	r := &MOSCReconciler{
		mcfgclient: client,
		mcpLister:  &fakeMCPListerForSelector{items: []*mcfgv1.MachineConfigPool{mcp}},
		mcLister:   &fakeMCListerForSelector{items: []*mcfgv1.MachineConfig{mc}},
		events:     services.NewNoopEventRecorder(),
		metrics:    services.NewNoopMetricsRecorder(),
	}

	err := r.createMachineOSBuild(context.Background(), mosc)
	if err != nil {
		t.Fatalf("expected nil for AlreadyExists, got: %v", err)
	}
}

func TestHandleRebuild_DeleteError(t *testing.T) {
	mosc := &mcfgv1.MachineOSConfig{
		ObjectMeta: metav1.ObjectMeta{
			Name: "mosc-1",
			Annotations: map[string]string{
				constants.CurrentMachineOSBuildAnnotationKey: "build-that-exists",
			},
		},
		Spec: mcfgv1.MachineOSConfigSpec{
			MachineConfigPool:     mcfgv1.MachineConfigPoolReference{Name: "worker"},
			RenderedImagePushSpec: "registry.example.com/image",
		},
	}
	mc := &mcfgv1.MachineConfig{
		ObjectMeta: metav1.ObjectMeta{
			Name: "rendered-worker-abc",
			Annotations: map[string]string{
				ctrlcommon.ReleaseImageVersionAnnotationKey:          "4.19.0",
				ctrlcommon.GeneratedByControllerVersionAnnotationKey: "4.19.0",
			},
		},
	}
	mcp := &mcfgv1.MachineConfigPool{
		ObjectMeta: metav1.ObjectMeta{Name: "worker"},
		Spec: mcfgv1.MachineConfigPoolSpec{
			Configuration: mcfgv1.MachineConfigPoolStatusConfiguration{
				ObjectReference: corev1.ObjectReference{Name: "rendered-worker-abc"},
			},
		},
	}

	// Create client WITHOUT the build → Delete returns NotFound (which is tolerated).
	client := fakemcfgclient.NewSimpleClientset(mosc)
	r := &MOSCReconciler{
		mcfgclient: client,
		moscLister: &fakeMOSCListerForSelector{items: []*mcfgv1.MachineOSConfig{mosc}},
		mosbLister: &fakeMOSBListerForSelector{items: nil},
		mcpLister:  &fakeMCPListerForSelector{items: []*mcfgv1.MachineConfigPool{mcp}},
		mcLister:   &fakeMCListerForSelector{items: []*mcfgv1.MachineConfig{mc}},
		seeder:     services.NewSeeder(nil, nil, nil, nil),
		reuse:      &fakeReuseChecker{},
		events:     services.NewNoopEventRecorder(),
		metrics:    services.NewNoopMetricsRecorder(),
	}

	err := r.handleRebuild(context.Background(), mosc)
	if err != nil {
		t.Fatalf("handleRebuild error: %v", err)
	}
}

func TestUpdateMOSCStatus_ListerError(t *testing.T) {
	mosc := &mcfgv1.MachineOSConfig{
		ObjectMeta: metav1.ObjectMeta{Name: "missing-mosc"},
	}
	mosb := &mcfgv1.MachineOSBuild{
		ObjectMeta: metav1.ObjectMeta{Name: "mosb-1"},
	}

	r := &MOSCReconciler{
		moscLister: &fakeMOSCListerForSelector{items: nil},
	}

	err := r.updateMOSCStatus(context.Background(), mosc, mosb)
	if err == nil {
		t.Fatal("expected error for missing MOSC in lister")
	}
}

func TestBuildDesiredMOSB_MCPNotFound(t *testing.T) {
	mosc := &mcfgv1.MachineOSConfig{
		ObjectMeta: metav1.ObjectMeta{Name: "mosc-1"},
		Spec: mcfgv1.MachineOSConfigSpec{
			MachineConfigPool: mcfgv1.MachineConfigPoolReference{Name: "missing"},
		},
	}

	r := &MOSCReconciler{
		mcpLister: &fakeMCPListerForSelector{items: nil},
	}

	_, err := r.buildDesiredMOSB(mosc)
	if err == nil {
		t.Fatal("expected error for missing MCP")
	}
}

func TestBuildDesiredMOSB_MCNotFound(t *testing.T) {
	mcp := &mcfgv1.MachineConfigPool{
		ObjectMeta: metav1.ObjectMeta{Name: "worker"},
		Spec: mcfgv1.MachineConfigPoolSpec{
			Configuration: mcfgv1.MachineConfigPoolStatusConfiguration{
				ObjectReference: corev1.ObjectReference{Name: "missing-mc"},
			},
		},
	}
	mosc := &mcfgv1.MachineOSConfig{
		ObjectMeta: metav1.ObjectMeta{Name: "mosc-1"},
		Spec: mcfgv1.MachineOSConfigSpec{
			MachineConfigPool: mcfgv1.MachineConfigPoolReference{Name: "worker"},
		},
	}

	r := &MOSCReconciler{
		mcpLister: &fakeMCPListerForSelector{items: []*mcfgv1.MachineConfigPool{mcp}},
		mcLister:  &fakeMCListerForSelector{items: nil},
	}

	_, err := r.buildDesiredMOSB(mosc)
	if err == nil {
		t.Fatal("expected error for missing MC")
	}
}

func TestCleanupBuildResources_NilKubeclient(t *testing.T) {
	r := &MOSCReconciler{kubeclient: nil}
	err := r.cleanupBuildResources(context.Background(), "deleted-mosc")
	if err != nil {
		t.Fatalf("expected nil for nil kubeclient, got: %v", err)
	}
}

func TestCleanupBuildResources_FullCleanup(t *testing.T) {
	moscName := "deleted-mosc"
	lbls := map[string]string{constants.MachineOSConfigNameLabelKey: moscName}

	job := &batchv1.Job{
		ObjectMeta: metav1.ObjectMeta{
			Name: "orphan-job", Namespace: "openshift-machine-config-operator", Labels: lbls,
		},
	}
	cm := &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{
			Name: "orphan-cm", Namespace: "openshift-machine-config-operator", Labels: lbls,
		},
	}
	secret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name: "orphan-secret", Namespace: "openshift-machine-config-operator", Labels: lbls,
		},
	}

	kubeclient := k8sfake.NewSimpleClientset(job, cm, secret)
	r := &MOSCReconciler{kubeclient: kubeclient}

	err := r.cleanupBuildResources(context.Background(), moscName)
	if err != nil {
		t.Fatalf("cleanupBuildResources error: %v", err)
	}

	// Verify everything was deleted.
	jobs, _ := kubeclient.BatchV1().Jobs("openshift-machine-config-operator").List(
		context.Background(), metav1.ListOptions{})
	if len(jobs.Items) != 0 {
		t.Errorf("expected 0 jobs after cleanup, got %d", len(jobs.Items))
	}
	cms, _ := kubeclient.CoreV1().ConfigMaps("openshift-machine-config-operator").List(
		context.Background(), metav1.ListOptions{})
	if len(cms.Items) != 0 {
		t.Errorf("expected 0 configmaps after cleanup, got %d", len(cms.Items))
	}
	secrets, _ := kubeclient.CoreV1().Secrets("openshift-machine-config-operator").List(
		context.Background(), metav1.ListOptions{})
	if len(secrets.Items) != 0 {
		t.Errorf("expected 0 secrets after cleanup, got %d", len(secrets.Items))
	}
}

// TestCleanupBuildResources_AggregatesErrors verifies that when one resource
// type fails to delete, the other types still get a deletion attempt and all
// errors are collected via errors.Join instead of aborting on the first error.
func TestCleanupBuildResources_AggregatesErrors(t *testing.T) {
	moscName := "deleted-mosc"
	lbls := map[string]string{constants.MachineOSConfigNameLabelKey: moscName}

	job := &batchv1.Job{
		ObjectMeta: metav1.ObjectMeta{
			Name: "orphan-job", Namespace: ctrlcommon.MCONamespace, Labels: lbls,
		},
	}
	cm := &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{
			Name: "orphan-cm", Namespace: ctrlcommon.MCONamespace, Labels: lbls,
		},
	}
	secret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name: "orphan-secret", Namespace: ctrlcommon.MCONamespace, Labels: lbls,
		},
	}

	kubeclient := k8sfake.NewSimpleClientset(job, cm, secret)

	// Inject a reactor that makes Job deletes fail.
	kubeclient.PrependReactor("delete", "jobs", func(action k8stesting.Action) (bool, runtime.Object, error) {
		return true, nil, fmt.Errorf("injected job delete failure")
	})

	r := &MOSCReconciler{kubeclient: kubeclient}

	err := r.cleanupBuildResources(context.Background(), moscName)
	if err == nil {
		t.Fatal("expected aggregated error from job delete failure")
	}
	if !strings.Contains(err.Error(), "injected job delete failure") {
		t.Errorf("expected error to mention job failure, got: %v", err)
	}

	// Despite the job delete failing, ConfigMaps and Secrets should still
	// have been deleted.
	cms, _ := kubeclient.CoreV1().ConfigMaps(ctrlcommon.MCONamespace).List(
		context.Background(), metav1.ListOptions{})
	if len(cms.Items) != 0 {
		t.Errorf("expected 0 configmaps after cleanup (errors should not abort), got %d", len(cms.Items))
	}
	secrets, _ := kubeclient.CoreV1().Secrets(ctrlcommon.MCONamespace).List(
		context.Background(), metav1.ListOptions{})
	if len(secrets.Items) != 0 {
		t.Errorf("expected 0 secrets after cleanup (errors should not abort), got %d", len(secrets.Items))
	}
}
