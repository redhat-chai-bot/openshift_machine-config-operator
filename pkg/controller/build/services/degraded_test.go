package services

import (
	"context"
	"fmt"
	"testing"

	mcfgv1 "github.com/openshift/api/machineconfiguration/v1"
	"github.com/openshift/machine-config-operator/pkg/controller/build/constants"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
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
