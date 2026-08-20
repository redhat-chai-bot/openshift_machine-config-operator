package reconcile

import (
	"context"
	"fmt"
	"testing"

	mcfgv1 "github.com/openshift/api/machineconfiguration/v1"
	fakemcfgclient "github.com/openshift/client-go/machineconfiguration/clientset/versioned/fake"
	"github.com/openshift/machine-config-operator/pkg/controller/build/services"
	"github.com/openshift/machine-config-operator/pkg/controller/build/utils"
	ctrlcommon "github.com/openshift/machine-config-operator/pkg/controller/common"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
)

func TestPoolReconciler_NotFound(t *testing.T) {
	r := &PoolReconciler{
		mcpLister: &fakeMCPListerForSelector{items: nil},
	}

	err := r.ReconcilePool(context.Background(), "nonexistent")
	if err != nil {
		t.Errorf("expected nil error for not-found pool, got: %v", err)
	}
}

func TestPoolReconciler_NoMOSC(t *testing.T) {
	mcp := &mcfgv1.MachineConfigPool{
		ObjectMeta: metav1.ObjectMeta{Name: "worker"},
	}

	r := &PoolReconciler{
		mcpLister:  &fakeMCPListerForSelector{items: []*mcfgv1.MachineConfigPool{mcp}},
		moscLister: &fakeMOSCListerForSelector{items: nil},
		events:     services.NewNoopEventRecorder(),
		metrics:    services.NewNoopMetricsRecorder(),
		utilListers: &utils.Listers{
			MachineOSBuildLister:    &fakeMOSBListerForSelector{items: nil},
			MachineOSConfigLister:   &fakeMOSCListerForSelector{items: nil},
			MachineConfigPoolLister: &fakeMCPListerForSelector{items: []*mcfgv1.MachineConfigPool{mcp}},
		},
	}

	err := r.ReconcilePool(context.Background(), "worker")
	if err != nil {
		t.Errorf("expected nil error when no MOSC exists, got: %v", err)
	}
}

func TestPoolReconciler_EnsuresBuild(t *testing.T) {
	mcp := &mcfgv1.MachineConfigPool{
		ObjectMeta: metav1.ObjectMeta{
			Name:   "worker",
			Labels: map[string]string{"pools.operator.machineconfiguration.openshift.io/worker": ""},
		},
		Spec: mcfgv1.MachineConfigPoolSpec{
			Configuration: mcfgv1.MachineConfigPoolStatusConfiguration{
				ObjectReference: corev1.ObjectReference{Name: "rendered-worker-1"},
			},
		},
	}

	mosc := &mcfgv1.MachineOSConfig{
		ObjectMeta: metav1.ObjectMeta{
			Name: "test-mosc",
		},
		Spec: mcfgv1.MachineOSConfigSpec{
			MachineConfigPool:     mcfgv1.MachineConfigPoolReference{Name: "worker"},
			RenderedImagePushSpec: "registry.example.com/ocp:latest",
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

	mcfgclient := fakemcfgclient.NewSimpleClientset()
	dh := &fakeDegradedHandler{}

	moscLister := &fakeMOSCListerForSelector{items: []*mcfgv1.MachineOSConfig{mosc}}
	mcpLister := &fakeMCPListerForSelector{items: []*mcfgv1.MachineConfigPool{mcp}}
	mosbLister := &fakeMOSBListerForSelector{items: nil}

	r := &PoolReconciler{
		mcfgclient: mcfgclient,
		mcpLister:  mcpLister,
		moscLister: moscLister,
		mosbLister: mosbLister,
		mcLister:   &fakeMCListerForSelector{items: []*mcfgv1.MachineConfig{mc}},
		events:     services.NewNoopEventRecorder(),
		metrics:    services.NewNoopMetricsRecorder(),
		degraded:   dh,
		utilListers: &utils.Listers{
			MachineOSBuildLister:    mosbLister,
			MachineOSConfigLister:   moscLister,
			MachineConfigPoolLister: mcpLister,
		},
	}

	err := r.ReconcilePool(context.Background(), "worker")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Verify a MOSB was created via the fake client.
	mosbList, err := mcfgclient.MachineconfigurationV1().MachineOSBuilds().List(context.Background(), metav1.ListOptions{})
	if err != nil {
		t.Fatalf("could not list MOSBs: %v", err)
	}
	if len(mosbList.Items) == 0 {
		t.Error("expected at least one MachineOSBuild to be created")
	}
}

// errorMCPLister always returns an error (non-NotFound).
type errorMCPLister struct {
	err error
}

func (e *errorMCPLister) List(_ labels.Selector) ([]*mcfgv1.MachineConfigPool, error) {
	return nil, e.err
}

func (e *errorMCPLister) Get(_ string) (*mcfgv1.MachineConfigPool, error) {
	return nil, e.err
}

func TestPoolReconciler_GetError(t *testing.T) {
	r := &PoolReconciler{
		mcpLister: &errorMCPLister{err: fmt.Errorf("lister error")},
	}
	err := r.ReconcilePool(context.Background(), "worker")
	if err == nil {
		t.Fatal("expected error from lister failure")
	}
}

func TestPoolReconciler_MOSCLookupError(t *testing.T) {
	mcp := &mcfgv1.MachineConfigPool{
		ObjectMeta: metav1.ObjectMeta{Name: "worker"},
	}

	// errorMOSCLister returns a non-NotFound error.
	r := &PoolReconciler{
		mcpLister: &fakeMCPListerForSelector{items: []*mcfgv1.MachineConfigPool{mcp}},
		utilListers: &utils.Listers{
			MachineOSConfigLister:   &errorMOSCLister{err: fmt.Errorf("lister broken")},
			MachineConfigPoolLister: &fakeMCPListerForSelector{items: []*mcfgv1.MachineConfigPool{mcp}},
		},
	}

	err := r.ReconcilePool(context.Background(), "worker")
	if err == nil {
		t.Fatal("expected error from MOSC lister failure")
	}
}

// errorMOSCLister always returns an error (non-NotFound).
type errorMOSCLister struct {
	err error
}

func (e *errorMOSCLister) List(_ labels.Selector) ([]*mcfgv1.MachineOSConfig, error) {
	return nil, e.err
}

func (e *errorMOSCLister) Get(_ string) (*mcfgv1.MachineOSConfig, error) {
	return nil, e.err
}

func TestEnsureBuildForPool_MOSBListerError(t *testing.T) {
	mcp := &mcfgv1.MachineConfigPool{
		ObjectMeta: metav1.ObjectMeta{Name: "worker"},
		Spec: mcfgv1.MachineConfigPoolSpec{
			Configuration: mcfgv1.MachineConfigPoolStatusConfiguration{
				ObjectReference: corev1.ObjectReference{Name: "rendered-worker-1"},
			},
		},
	}
	mosc := &mcfgv1.MachineOSConfig{
		ObjectMeta: metav1.ObjectMeta{Name: "mosc-1"},
		Spec: mcfgv1.MachineOSConfigSpec{
			MachineConfigPool:     mcfgv1.MachineConfigPoolReference{Name: "worker"},
			RenderedImagePushSpec: "registry.example.com/image",
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

	// errorMOSBLister returns a non-NotFound error.
	r := &PoolReconciler{
		mcpLister:  &fakeMCPListerForSelector{items: []*mcfgv1.MachineConfigPool{mcp}},
		mosbLister: &errorMOSBListerPool{err: fmt.Errorf("lister broken")},
		mcLister:   &fakeMCListerForSelector{items: []*mcfgv1.MachineConfig{mc}},
	}

	err := r.ensureBuildForPool(context.Background(), mcp, mosc)
	if err == nil {
		t.Fatal("expected error from MOSB lister failure")
	}
}

// errorMOSBListerPool always returns a non-NotFound error.
type errorMOSBListerPool struct {
	err error
}

func (e *errorMOSBListerPool) List(_ labels.Selector) ([]*mcfgv1.MachineOSBuild, error) {
	return nil, e.err
}
func (e *errorMOSBListerPool) Get(_ string) (*mcfgv1.MachineOSBuild, error) {
	return nil, e.err
}

func TestEnsureBuildForPool_MCNotFound(t *testing.T) {
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

	r := &PoolReconciler{
		mcpLister: &fakeMCPListerForSelector{items: []*mcfgv1.MachineConfigPool{mcp}},
		mcLister:  &fakeMCListerForSelector{items: nil},
	}

	err := r.ensureBuildForPool(context.Background(), mcp, mosc)
	if err == nil {
		t.Fatal("expected error for missing MC")
	}
}

func TestPoolReconciler_DegradedUpdateError(t *testing.T) {
	mcp := &mcfgv1.MachineConfigPool{
		ObjectMeta: metav1.ObjectMeta{
			Name:   "worker",
			Labels: map[string]string{"pools.operator.machineconfiguration.openshift.io/worker": ""},
		},
		Spec: mcfgv1.MachineConfigPoolSpec{
			Configuration: mcfgv1.MachineConfigPoolStatusConfiguration{
				ObjectReference: corev1.ObjectReference{Name: "rendered-worker-1"},
			},
		},
	}
	mosc := &mcfgv1.MachineOSConfig{
		ObjectMeta: metav1.ObjectMeta{Name: "test-mosc"},
		Spec: mcfgv1.MachineOSConfigSpec{
			MachineConfigPool:     mcfgv1.MachineConfigPoolReference{Name: "worker"},
			RenderedImagePushSpec: "registry.example.com/ocp:latest",
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

	mcfgclient := fakemcfgclient.NewSimpleClientset()
	// degraded handler that returns error.
	errDH := &errorDegradedHandler{err: fmt.Errorf("degraded update failed")}

	moscLister := &fakeMOSCListerForSelector{items: []*mcfgv1.MachineOSConfig{mosc}}
	mcpLister := &fakeMCPListerForSelector{items: []*mcfgv1.MachineConfigPool{mcp}}

	r := &PoolReconciler{
		mcfgclient: mcfgclient,
		mcpLister:  mcpLister,
		moscLister: moscLister,
		mosbLister: &fakeMOSBListerForSelector{items: nil},
		mcLister:   &fakeMCListerForSelector{items: []*mcfgv1.MachineConfig{mc}},
		events:     services.NewNoopEventRecorder(),
		metrics:    services.NewNoopMetricsRecorder(),
		degraded:   errDH,
		utilListers: &utils.Listers{
			MachineOSBuildLister:    &fakeMOSBListerForSelector{items: nil},
			MachineOSConfigLister:   moscLister,
			MachineConfigPoolLister: mcpLister,
		},
	}

	// degraded.UpdateImageBuildDegraded returns an error but it's logged, not returned.
	// The test still exercises the error path (line 87-89).
	err := r.ReconcilePool(context.Background(), "worker")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

// errorDegradedHandler always returns an error for UpdateImageBuildDegraded.
type errorDegradedHandler struct {
	err error
}

func (e *errorDegradedHandler) InitializeBuildDegraded(_ context.Context, _ *mcfgv1.MachineConfigPool) error {
	return e.err
}
func (e *errorDegradedHandler) SyncBuildSuccess(_ context.Context, _ *mcfgv1.MachineConfigPool) error {
	return e.err
}
func (e *errorDegradedHandler) SyncBuildFailure(_ context.Context, _ *mcfgv1.MachineConfigPool, buildErr error, _ string) error {
	return buildErr
}
func (e *errorDegradedHandler) UpdateImageBuildDegraded(_ context.Context, _ *mcfgv1.MachineConfigPool, _ *mcfgv1.MachineOSConfig) error {
	return e.err
}
