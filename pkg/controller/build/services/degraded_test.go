package services

import (
	"context"
	"fmt"
	"testing"

	mcfgv1 "github.com/openshift/api/machineconfiguration/v1"
	fakeclientmachineconfiguration "github.com/openshift/client-go/machineconfiguration/clientset/versioned/fake"
	mcfglistersv1 "github.com/openshift/client-go/machineconfiguration/listers/machineconfiguration/v1"
	"github.com/openshift/machine-config-operator/pkg/controller/build/constants"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/runtime"
)

func TestGetCurrentBuild_ReferencedByMOSC(t *testing.T) {
	mosc := &mcfgv1.MachineOSConfig{
		ObjectMeta: metav1.ObjectMeta{
			Name: "test-mosc",
			Annotations: map[string]string{
				constants.CurrentMachineOSBuildAnnotationKey: "build-2",
			},
		},
	}

	builds := []*mcfgv1.MachineOSBuild{
		{ObjectMeta: metav1.ObjectMeta{Name: "build-1"}},
		{ObjectMeta: metav1.ObjectMeta{Name: "build-2"}},
		{ObjectMeta: metav1.ObjectMeta{Name: "build-3"}},
	}

	result := getCurrentBuild(mosc, builds)
	if result == nil {
		t.Fatal("expected a build, got nil")
	}
	if result.Name != "build-2" {
		t.Errorf("expected build-2, got %s", result.Name)
	}
}

func TestGetCurrentBuild_MostRecent(t *testing.T) {
	mosc := &mcfgv1.MachineOSConfig{
		ObjectMeta: metav1.ObjectMeta{Name: "test-mosc"},
	}

	now := metav1.Now()
	earlier := metav1.NewTime(now.Add(-60_000_000_000)) // 60s earlier

	builds := []*mcfgv1.MachineOSBuild{
		{ObjectMeta: metav1.ObjectMeta{Name: "old-build", CreationTimestamp: earlier}},
		{ObjectMeta: metav1.ObjectMeta{Name: "new-build", CreationTimestamp: now}},
	}

	result := getCurrentBuild(mosc, builds)
	if result == nil {
		t.Fatal("expected a build, got nil")
	}
	if result.Name != "new-build" {
		t.Errorf("expected new-build, got %s", result.Name)
	}
}

func TestGetCurrentBuild_NoBuilds(t *testing.T) {
	mosc := &mcfgv1.MachineOSConfig{
		ObjectMeta: metav1.ObjectMeta{Name: "test-mosc"},
	}

	result := getCurrentBuild(mosc, nil)
	if result != nil {
		t.Errorf("expected nil, got %v", result)
	}
}

func TestGetBuildErrorFromMOSB_WithFailedCondition(t *testing.T) {
	mosb := &mcfgv1.MachineOSBuild{
		ObjectMeta: metav1.ObjectMeta{Name: "failed-build"},
		Status: mcfgv1.MachineOSBuildStatus{
			Conditions: []metav1.Condition{
				{
					Type:    string(mcfgv1.MachineOSBuildFailed),
					Status:  metav1.ConditionTrue,
					Message: "image push timed out",
				},
			},
		},
	}

	err := getBuildErrorFromMOSB(mosb)
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if err.Error() != "image push timed out" {
		t.Errorf("expected 'image push timed out', got %q", err.Error())
	}
}

func TestGetBuildErrorFromMOSB_NoCondition(t *testing.T) {
	mosb := &mcfgv1.MachineOSBuild{
		ObjectMeta: metav1.ObjectMeta{Name: "mystery-build"},
	}

	err := getBuildErrorFromMOSB(mosb)
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	expected := fmt.Sprintf("build %s failed (no failure condition found)", mosb.Name)
	if err.Error() != expected {
		t.Errorf("expected %q, got %q", expected, err.Error())
	}
}

// TestDegradedHandlerInterface verifies that degradedHandler implements DegradedHandler.
func TestDegradedHandlerInterface(t *testing.T) {
	// This is a compile-time check — if degradedHandler doesn't implement the
	// interface, this file won't compile.
	var _ DegradedHandler = &degradedHandler{}
}

// TestNewDegradedHandler verifies construction.
func TestNewDegradedHandler(t *testing.T) {
	// We can't easily set up a full fake client in this test, but we can verify
	// construction doesn't panic and returns non-nil.
	dh := NewDegradedHandler(nil, nil)
	if dh == nil {
		t.Fatal("NewDegradedHandler returned nil")
	}
}

// TestInitializeBuildDegraded_AlreadyFalse verifies the no-op path when condition is already False.
func TestInitializeBuildDegraded_AlreadyFalse(t *testing.T) {
	dh := &degradedHandler{}

	pool := &mcfgv1.MachineConfigPool{
		ObjectMeta: metav1.ObjectMeta{Name: "worker"},
		Status: mcfgv1.MachineConfigPoolStatus{
			Conditions: []mcfgv1.MachineConfigPoolCondition{
				{
					Type:   mcfgv1.MachineConfigPoolImageBuildDegraded,
					Status: "False",
				},
			},
		},
	}

	// Should be a no-op (mcfgclient is nil so would panic if called).
	err := dh.InitializeBuildDegraded(context.Background(), pool)
	if err != nil {
		t.Fatalf("expected no error for already-False condition, got: %v", err)
	}
}

// fakeMOSBLister is a simple in-memory lister for tests.
type fakeMOSBLister struct {
	items []*mcfgv1.MachineOSBuild
}

func (f *fakeMOSBLister) List(selector labels.Selector) ([]*mcfgv1.MachineOSBuild, error) {
	var out []*mcfgv1.MachineOSBuild
	for _, m := range f.items {
		if selector.Matches(labels.Set(m.Labels)) {
			out = append(out, m)
		}
	}
	return out, nil
}

func (f *fakeMOSBLister) Get(name string) (*mcfgv1.MachineOSBuild, error) {
	for _, m := range f.items {
		if m.Name == name {
			return m, nil
		}
	}
	return nil, fmt.Errorf("not found")
}

// Compile-time check.
var _ mcfglistersv1.MachineOSBuildLister = &fakeMOSBLister{}

func newFakeMCFGClient(objs ...runtime.Object) *fakeclientmachineconfiguration.Clientset {
	return fakeclientmachineconfiguration.NewSimpleClientset(objs...)
}

func TestInitializeBuildDegraded_SetsCondition(t *testing.T) {
	pool := &mcfgv1.MachineConfigPool{
		ObjectMeta: metav1.ObjectMeta{Name: "worker"},
	}
	client := newFakeMCFGClient(pool)
	dh := NewDegradedHandler(client, nil)

	err := dh.InitializeBuildDegraded(context.Background(), pool)
	if err != nil {
		t.Fatalf("InitializeBuildDegraded error: %v", err)
	}

	updated, err := client.MachineconfigurationV1().MachineConfigPools().Get(context.Background(), "worker", metav1.GetOptions{})
	if err != nil {
		t.Fatalf("get pool: %v", err)
	}
	found := false
	for _, c := range updated.Status.Conditions {
		if c.Type == mcfgv1.MachineConfigPoolImageBuildDegraded && c.Status == "False" {
			found = true
		}
	}
	if !found {
		t.Error("expected ImageBuildDegraded=False condition after InitializeBuildDegraded")
	}
}

func TestSyncBuildSuccess(t *testing.T) {
	pool := &mcfgv1.MachineConfigPool{
		ObjectMeta: metav1.ObjectMeta{Name: "worker"},
	}
	client := newFakeMCFGClient(pool)
	dh := NewDegradedHandler(client, nil)

	err := dh.SyncBuildSuccess(context.Background(), pool)
	if err != nil {
		t.Fatalf("SyncBuildSuccess error: %v", err)
	}

	updated, err := client.MachineconfigurationV1().MachineConfigPools().Get(context.Background(), "worker", metav1.GetOptions{})
	if err != nil {
		t.Fatalf("get pool: %v", err)
	}
	found := false
	for _, c := range updated.Status.Conditions {
		if c.Type == mcfgv1.MachineConfigPoolImageBuildDegraded && c.Status == "False" {
			found = true
		}
	}
	if !found {
		t.Error("expected ImageBuildDegraded=False condition")
	}
}

func TestSyncBuildFailure(t *testing.T) {
	pool := &mcfgv1.MachineConfigPool{
		ObjectMeta: metav1.ObjectMeta{Name: "worker"},
	}
	client := newFakeMCFGClient(pool)
	dh := NewDegradedHandler(client, nil)

	buildErr := fmt.Errorf("image push timed out")
	returnedErr := dh.SyncBuildFailure(context.Background(), pool, buildErr, "mosb-1")
	if returnedErr != buildErr {
		t.Errorf("expected original buildErr returned, got %v", returnedErr)
	}

	updated, err := client.MachineconfigurationV1().MachineConfigPools().Get(context.Background(), "worker", metav1.GetOptions{})
	if err != nil {
		t.Fatalf("get pool: %v", err)
	}
	found := false
	for _, c := range updated.Status.Conditions {
		if c.Type == mcfgv1.MachineConfigPoolImageBuildDegraded && c.Status == "True" {
			found = true
		}
	}
	if !found {
		t.Error("expected ImageBuildDegraded=True condition after failure")
	}
}

func TestUpdateImageBuildDegraded_NoBuilds(t *testing.T) {
	pool := &mcfgv1.MachineConfigPool{
		ObjectMeta: metav1.ObjectMeta{Name: "worker"},
	}
	mosc := &mcfgv1.MachineOSConfig{
		ObjectMeta: metav1.ObjectMeta{
			Name: "mosc-1",
			Labels: map[string]string{
				constants.TargetMachineConfigPoolLabelKey: "worker",
			},
		},
		Spec: mcfgv1.MachineOSConfigSpec{
			MachineConfigPool: mcfgv1.MachineConfigPoolReference{Name: "worker"},
		},
	}
	client := newFakeMCFGClient(pool)
	lister := &fakeMOSBLister{items: nil}
	dh := NewDegradedHandler(client, lister)

	err := dh.UpdateImageBuildDegraded(context.Background(), pool, mosc)
	if err != nil {
		t.Fatalf("UpdateImageBuildDegraded error: %v", err)
	}
}

func TestUpdateImageBuildDegraded_FailedBuild(t *testing.T) {
	pool := &mcfgv1.MachineConfigPool{
		ObjectMeta: metav1.ObjectMeta{Name: "worker"},
	}
	mosc := &mcfgv1.MachineOSConfig{
		ObjectMeta: metav1.ObjectMeta{
			Name: "mosc-1",
			Annotations: map[string]string{
				constants.CurrentMachineOSBuildAnnotationKey: "mosb-fail",
			},
		},
		Spec: mcfgv1.MachineOSConfigSpec{
			MachineConfigPool: mcfgv1.MachineConfigPoolReference{Name: "worker"},
		},
	}
	failedMOSB := &mcfgv1.MachineOSBuild{
		ObjectMeta: metav1.ObjectMeta{
			Name: "mosb-fail",
			Labels: map[string]string{
				constants.TargetMachineConfigPoolLabelKey: "worker",
				constants.MachineOSConfigNameLabelKey:     "mosc-1",
			},
		},
		Status: mcfgv1.MachineOSBuildStatus{
			Conditions: []metav1.Condition{
				{
					Type:    string(mcfgv1.MachineOSBuildFailed),
					Status:  metav1.ConditionTrue,
					Message: "push error",
				},
			},
		},
	}
	client := newFakeMCFGClient(pool)
	lister := &fakeMOSBLister{items: []*mcfgv1.MachineOSBuild{failedMOSB}}
	dh := NewDegradedHandler(client, lister)

	// SyncBuildFailure returns the original buildErr (not a status update error).
	err := dh.UpdateImageBuildDegraded(context.Background(), pool, mosc)
	if err == nil {
		t.Fatal("expected error from SyncBuildFailure propagation")
	}
	if err.Error() != "push error" {
		t.Errorf("expected 'push error', got %q", err.Error())
	}
}

func TestUpdateImageBuildDegraded_SucceededBuild(t *testing.T) {
	pool := &mcfgv1.MachineConfigPool{
		ObjectMeta: metav1.ObjectMeta{Name: "worker"},
	}
	mosc := &mcfgv1.MachineOSConfig{
		ObjectMeta: metav1.ObjectMeta{
			Name: "mosc-1",
			Annotations: map[string]string{
				constants.CurrentMachineOSBuildAnnotationKey: "mosb-ok",
			},
		},
		Spec: mcfgv1.MachineOSConfigSpec{
			MachineConfigPool: mcfgv1.MachineConfigPoolReference{Name: "worker"},
		},
	}
	successMOSB := &mcfgv1.MachineOSBuild{
		ObjectMeta: metav1.ObjectMeta{
			Name: "mosb-ok",
			Labels: map[string]string{
				constants.TargetMachineConfigPoolLabelKey: "worker",
				constants.MachineOSConfigNameLabelKey:     "mosc-1",
			},
		},
		Status: mcfgv1.MachineOSBuildStatus{
			Conditions: []metav1.Condition{
				{
					Type:   string(mcfgv1.MachineOSBuildSucceeded),
					Status: metav1.ConditionTrue,
				},
			},
		},
	}
	client := newFakeMCFGClient(pool)
	lister := &fakeMOSBLister{items: []*mcfgv1.MachineOSBuild{successMOSB}}
	dh := NewDegradedHandler(client, lister)

	err := dh.UpdateImageBuildDegraded(context.Background(), pool, mosc)
	if err != nil {
		t.Fatalf("UpdateImageBuildDegraded error: %v", err)
	}
}

func TestUpdateImageBuildDegraded_BuildingBuild(t *testing.T) {
	pool := &mcfgv1.MachineConfigPool{
		ObjectMeta: metav1.ObjectMeta{Name: "worker"},
	}
	mosc := &mcfgv1.MachineOSConfig{
		ObjectMeta: metav1.ObjectMeta{
			Name: "mosc-1",
			Annotations: map[string]string{
				constants.CurrentMachineOSBuildAnnotationKey: "mosb-building",
			},
		},
		Spec: mcfgv1.MachineOSConfigSpec{
			MachineConfigPool: mcfgv1.MachineConfigPoolReference{Name: "worker"},
		},
	}
	buildingMOSB := &mcfgv1.MachineOSBuild{
		ObjectMeta: metav1.ObjectMeta{
			Name: "mosb-building",
			Labels: map[string]string{
				constants.TargetMachineConfigPoolLabelKey: "worker",
				constants.MachineOSConfigNameLabelKey:     "mosc-1",
			},
		},
		Status: mcfgv1.MachineOSBuildStatus{
			Conditions: []metav1.Condition{
				{
					Type:   string(mcfgv1.MachineOSBuilding),
					Status: metav1.ConditionTrue,
				},
			},
		},
	}
	client := newFakeMCFGClient(pool)
	lister := &fakeMOSBLister{items: []*mcfgv1.MachineOSBuild{buildingMOSB}}
	dh := NewDegradedHandler(client, lister)

	err := dh.UpdateImageBuildDegraded(context.Background(), pool, mosc)
	if err != nil {
		t.Fatalf("UpdateImageBuildDegraded error: %v", err)
	}
}

func TestGetCurrentBuild_BuildingPriority(t *testing.T) {
	mosc := &mcfgv1.MachineOSConfig{
		ObjectMeta: metav1.ObjectMeta{Name: "mosc-1"},
	}
	now := metav1.Now()
	builds := []*mcfgv1.MachineOSBuild{
		{
			ObjectMeta: metav1.ObjectMeta{Name: "older", CreationTimestamp: now},
		},
		{
			ObjectMeta: metav1.ObjectMeta{Name: "building-one", CreationTimestamp: now},
			Status: mcfgv1.MachineOSBuildStatus{
				Conditions: []metav1.Condition{
					{Type: string(mcfgv1.MachineOSBuilding), Status: metav1.ConditionTrue},
				},
			},
		},
	}
	result := getCurrentBuild(mosc, builds)
	if result == nil || result.Name != "building-one" {
		t.Errorf("expected building-one, got %v", result)
	}
}
