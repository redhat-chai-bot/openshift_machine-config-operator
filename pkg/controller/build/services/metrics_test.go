package services

import (
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus"
)

func TestNewMetricsRecorder(t *testing.T) {
	reg := prometheus.NewRegistry()
	m, err := NewMetricsRecorder(reg)
	if err != nil {
		t.Fatalf("NewMetricsRecorder failed: %v", err)
	}
	if m == nil {
		t.Fatal("NewMetricsRecorder returned nil")
	}
}

func TestNewMetricsRecorderDuplicateRegistration(t *testing.T) {
	reg := prometheus.NewRegistry()
	_, err := NewMetricsRecorder(reg)
	if err != nil {
		t.Fatalf("first registration failed: %v", err)
	}

	// Second registration on same registry should fail.
	_, err = NewMetricsRecorder(reg)
	if err == nil {
		t.Fatal("expected error on duplicate registration, got nil")
	}
}

func TestRecordBuildLifecycle(t *testing.T) {
	reg := prometheus.NewRegistry()
	m, err := NewMetricsRecorder(reg)
	if err != nil {
		t.Fatalf("NewMetricsRecorder failed: %v", err)
	}

	pool := "worker"
	start := time.Now().Add(-5 * time.Minute)

	// Run through a full lifecycle — should not panic.
	m.RecordBuildStarted(pool)
	m.RecordBuildBuilding(pool)
	m.RecordBuildCompleted(pool, start)
}

func TestMetricsRecordBuildFailed(t *testing.T) {
	reg := prometheus.NewRegistry()
	m, err := NewMetricsRecorder(reg)
	if err != nil {
		t.Fatalf("NewMetricsRecorder failed: %v", err)
	}

	start := time.Now().Add(-2 * time.Minute)
	m.RecordBuildStarted("worker")
	m.RecordBuildFailed("worker", start)
}

func TestRecordBuildInterrupted(t *testing.T) {
	reg := prometheus.NewRegistry()
	m, err := NewMetricsRecorder(reg)
	if err != nil {
		t.Fatalf("NewMetricsRecorder failed: %v", err)
	}

	m.RecordBuildStarted("worker")
	m.RecordBuildInterrupted("worker")
}

func TestRecordImagePushLifecycle(t *testing.T) {
	reg := prometheus.NewRegistry()
	m, err := NewMetricsRecorder(reg)
	if err != nil {
		t.Fatalf("NewMetricsRecorder failed: %v", err)
	}

	m.RecordImagePushStarted("worker")
	m.RecordImagePushCompleted("worker")
}

func TestRecordImagePushFailed(t *testing.T) {
	reg := prometheus.NewRegistry()
	m, err := NewMetricsRecorder(reg)
	if err != nil {
		t.Fatalf("NewMetricsRecorder failed: %v", err)
	}

	m.RecordImagePushStarted("worker")
	m.RecordImagePushFailed("worker")
}

func TestRecordBuildJobState(t *testing.T) {
	reg := prometheus.NewRegistry()
	m, err := NewMetricsRecorder(reg)
	if err != nil {
		t.Fatalf("NewMetricsRecorder failed: %v", err)
	}

	m.RecordBuildJobState("worker", StateBuilding)
	m.RecordBuildJobState("worker", StateSucceeded)
}

func TestRecordConfigChange(t *testing.T) {
	reg := prometheus.NewRegistry()
	m, err := NewMetricsRecorder(reg)
	if err != nil {
		t.Fatalf("NewMetricsRecorder failed: %v", err)
	}

	m.RecordConfigChange("worker")
}

func TestRecordBuildRetry(t *testing.T) {
	reg := prometheus.NewRegistry()
	m, err := NewMetricsRecorder(reg)
	if err != nil {
		t.Fatalf("NewMetricsRecorder failed: %v", err)
	}

	m.RecordBuildRetry("worker")
}

func TestUpdateLayeredNodesCount(t *testing.T) {
	reg := prometheus.NewRegistry()
	m, err := NewMetricsRecorder(reg)
	if err != nil {
		t.Fatalf("NewMetricsRecorder failed: %v", err)
	}

	m.UpdateLayeredNodesCount("worker", 5)
}

func TestRecordBuildQueueDuration(t *testing.T) {
	reg := prometheus.NewRegistry()
	m, err := NewMetricsRecorder(reg)
	if err != nil {
		t.Fatalf("NewMetricsRecorder failed: %v", err)
	}

	m.RecordBuildQueueDuration("worker", time.Now().Add(-30*time.Second))
}

func TestUpdateOCLRolloutCounts(t *testing.T) {
	reg := prometheus.NewRegistry()
	m, err := NewMetricsRecorder(reg)
	if err != nil {
		t.Fatalf("NewMetricsRecorder failed: %v", err)
	}

	m.UpdateOCLRolloutCounts("worker", 3, 5)
}

func TestUpdateMOSCCount(t *testing.T) {
	reg := prometheus.NewRegistry()
	m, err := NewMetricsRecorder(reg)
	if err != nil {
		t.Fatalf("NewMetricsRecorder failed: %v", err)
	}

	m.UpdateMOSCCount(2)
}

func TestMetricsGathered(t *testing.T) {
	reg := prometheus.NewRegistry()
	m, err := NewMetricsRecorder(reg)
	if err != nil {
		t.Fatalf("NewMetricsRecorder failed: %v", err)
	}

	// Perform some operations.
	m.RecordBuildStarted("worker")
	m.RecordBuildBuilding("worker")
	m.RecordConfigChange("worker")
	m.UpdateMOSCCount(1)

	// Gather all metrics to ensure registration is correct.
	families, err := reg.Gather()
	if err != nil {
		t.Fatalf("failed to gather metrics: %v", err)
	}

	// We should have metrics registered.
	if len(families) == 0 {
		t.Error("expected at least one metric family, got none")
	}

	// Check for specific metric names.
	names := map[string]bool{}
	for _, f := range families {
		names[f.GetName()] = true
	}

	expected := []string{"ocl_build_state", "ocl_config_change_total", "mco_mosc_count"}
	for _, name := range expected {
		if !names[name] {
			t.Errorf("expected metric %q not found in gathered metrics", name)
		}
	}
}

// TestNoopMetricsRecorder verifies that all methods are callable.
func TestNoopMetricsRecorder(t *testing.T) {
	m := NewNoopMetricsRecorder()

	m.RecordBuildStarted("p")
	m.RecordBuildBuilding("p")
	m.RecordBuildCompleted("p", time.Now())
	m.RecordBuildFailed("p", time.Now())
	m.RecordBuildInterrupted("p")
	m.RecordBuildJobState("p", "s")
	m.RecordConfigChange("p")
	m.RecordBuildRetry("p")
	m.UpdateLayeredNodesCount("p", 0)
	m.RecordImagePushStarted("p")
	m.RecordImagePushCompleted("p")
	m.RecordImagePushFailed("p")
	m.RecordBuildQueueDuration("p", time.Now())
	m.UpdateOCLRolloutCounts("p", 0, 0)
	m.UpdateMOSCCount(0)
}
