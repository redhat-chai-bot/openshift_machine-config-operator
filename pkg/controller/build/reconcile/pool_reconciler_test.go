package reconcile

import (
	"context"
	"testing"

	mcfgv1 "github.com/openshift/api/machineconfiguration/v1"
	fakemcfgclient "github.com/openshift/client-go/machineconfiguration/clientset/versioned/fake"
	ctrlcommon "github.com/openshift/machine-config-operator/pkg/controller/common"
	"github.com/openshift/machine-config-operator/pkg/controller/build/services"
	"github.com/openshift/machine-config-operator/pkg/controller/build/utils"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
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
			MachineConfigPool:    mcfgv1.MachineConfigPoolReference{Name: "worker"},
			RenderedImagePushSpec: "registry.example.com/ocp:latest",
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
