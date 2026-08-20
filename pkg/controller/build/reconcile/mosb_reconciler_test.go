package reconcile

import (
	"context"
	"sync/atomic"
	"testing"

	mcfgv1 "github.com/openshift/api/machineconfiguration/v1"
	"github.com/openshift/machine-config-operator/pkg/controller/build/constants"
	"github.com/openshift/machine-config-operator/pkg/controller/build/services"
	"github.com/openshift/machine-config-operator/pkg/controller/build/utils"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
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
