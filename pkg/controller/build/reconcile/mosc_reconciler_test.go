package reconcile

import (
	"context"
	"testing"

	"github.com/containers/image/v5/types"
	mcfgv1 "github.com/openshift/api/machineconfiguration/v1"
	fakemcfgclient "github.com/openshift/client-go/machineconfiguration/clientset/versioned/fake"
	"github.com/openshift/machine-config-operator/pkg/controller/build/constants"
	ctrlcommon "github.com/openshift/machine-config-operator/pkg/controller/common"
	"github.com/openshift/machine-config-operator/pkg/controller/build/services"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
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
			MachineConfigPool:    mcfgv1.MachineConfigPoolReference{Name: "worker"},
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
				ctrlcommon.ReleaseImageVersionAnnotationKey:           "4.19.0",
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
