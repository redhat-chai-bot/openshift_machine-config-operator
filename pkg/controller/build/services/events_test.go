package services

import (
	"fmt"
	"testing"

	mcfgv1 "github.com/openshift/api/machineconfiguration/v1"
	batchv1 "k8s.io/api/batch/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/tools/record"
)

func TestNewEventRecorder(t *testing.T) {
	fakeRecorder := record.NewFakeRecorder(10)
	r := NewEventRecorder(fakeRecorder)
	if r == nil {
		t.Fatal("NewEventRecorder returned nil")
	}
}

func TestRecordBuildStarted(t *testing.T) {
	fakeRecorder := record.NewFakeRecorder(10)
	r := NewEventRecorder(fakeRecorder)

	mosb := &mcfgv1.MachineOSBuild{
		ObjectMeta: metav1.ObjectMeta{Name: "test-build"},
		Spec: mcfgv1.MachineOSBuildSpec{
			MachineConfig: mcfgv1.MachineConfigReference{Name: "rendered-worker-abc"},
		},
	}
	mosc := &mcfgv1.MachineOSConfig{
		ObjectMeta: metav1.ObjectMeta{Name: "test-config"},
		Spec: mcfgv1.MachineOSConfigSpec{
			MachineConfigPool: mcfgv1.MachineConfigPoolReference{Name: "worker"},
		},
	}

	r.RecordBuildStarted(mosb, mosc)

	select {
	case event := <-fakeRecorder.Events:
		expected := fmt.Sprintf("Normal %s Started build for pool %q with config %q", EventBuildStarted, "worker", "rendered-worker-abc")
		if event != expected {
			t.Errorf("unexpected event:\ngot:  %q\nwant: %q", event, expected)
		}
	default:
		t.Error("expected an event to be recorded")
	}
}

func TestRecordBuildFailed(t *testing.T) {
	fakeRecorder := record.NewFakeRecorder(10)
	r := NewEventRecorder(fakeRecorder)

	mosb := &mcfgv1.MachineOSBuild{
		ObjectMeta: metav1.ObjectMeta{Name: "failed-build"},
	}

	r.RecordBuildFailed(mosb)

	select {
	case event := <-fakeRecorder.Events:
		expected := fmt.Sprintf("Warning %s Build failed; see MachineOSBuild %q status conditions for details", EventBuildFailed, "failed-build")
		if event != expected {
			t.Errorf("unexpected event:\ngot:  %q\nwant: %q", event, expected)
		}
	default:
		t.Error("expected an event to be recorded")
	}
}

func TestRecordJobCreated(t *testing.T) {
	fakeRecorder := record.NewFakeRecorder(10)
	r := NewEventRecorder(fakeRecorder)

	mosb := &mcfgv1.MachineOSBuild{
		ObjectMeta: metav1.ObjectMeta{Name: "build-1"},
	}
	job := &batchv1.Job{
		ObjectMeta: metav1.ObjectMeta{Name: "build-job-1"},
	}

	r.RecordJobCreated(mosb, job)

	select {
	case event := <-fakeRecorder.Events:
		expected := fmt.Sprintf("Normal %s Created build job: %s", EventJobCreated, "build-job-1")
		if event != expected {
			t.Errorf("unexpected event:\ngot:  %q\nwant: %q", event, expected)
		}
	default:
		t.Error("expected an event to be recorded")
	}
}

func TestRecordConfigReconciled(t *testing.T) {
	fakeRecorder := record.NewFakeRecorder(10)
	r := NewEventRecorder(fakeRecorder)

	mosc := &mcfgv1.MachineOSConfig{
		ObjectMeta: metav1.ObjectMeta{Name: "test-config"},
	}

	r.RecordConfigReconciled(mosc)

	select {
	case event := <-fakeRecorder.Events:
		expected := fmt.Sprintf("Normal %s MachineOSConfig spec change detected and reconciled: new build created or reused", EventConfigReconciled)
		if event != expected {
			t.Errorf("unexpected event:\ngot:  %q\nwant: %q", event, expected)
		}
	default:
		t.Error("expected an event to be recorded")
	}
}

func TestRecordBuildDegraded(t *testing.T) {
	fakeRecorder := record.NewFakeRecorder(10)
	r := NewEventRecorder(fakeRecorder)

	mosc := &mcfgv1.MachineOSConfig{
		ObjectMeta: metav1.ObjectMeta{Name: "degraded-config"},
		Spec: mcfgv1.MachineOSConfigSpec{
			MachineConfigPool: mcfgv1.MachineConfigPoolReference{Name: "worker"},
		},
	}

	r.RecordBuildDegraded(mosc)

	select {
	case event := <-fakeRecorder.Events:
		expected := fmt.Sprintf("Warning %s Build for pool %q degraded; see MachineOSBuild status conditions for details", EventBuildDegraded, "worker")
		if event != expected {
			t.Errorf("unexpected event:\ngot:  %q\nwant: %q", event, expected)
		}
	default:
		t.Error("expected an event to be recorded")
	}
}

func TestRecordPoolConfigChanged(t *testing.T) {
	fakeRecorder := record.NewFakeRecorder(10)
	r := NewEventRecorder(fakeRecorder)

	mcp := &mcfgv1.MachineConfigPool{
		ObjectMeta: metav1.ObjectMeta{Name: "worker"},
	}

	r.RecordPoolConfigChanged(mcp, "old-rendered", "new-rendered")

	select {
	case event := <-fakeRecorder.Events:
		expected := fmt.Sprintf("Normal %s Rendered config changed from old-rendered to new-rendered", EventPoolConfigChanged)
		if event != expected {
			t.Errorf("unexpected event:\ngot:  %q\nwant: %q", event, expected)
		}
	default:
		t.Error("expected an event to be recorded")
	}
}

func TestRecordBuildPreparing(t *testing.T) {
	fakeRecorder := record.NewFakeRecorder(10)
	r := NewEventRecorder(fakeRecorder)
	mosb := &mcfgv1.MachineOSBuild{ObjectMeta: metav1.ObjectMeta{Name: "build-1"}}
	r.RecordBuildPreparing(mosb, "gathering configs")
	event := <-fakeRecorder.Events
	if expected := fmt.Sprintf("Normal %s Preparing build: gathering configs", EventBuildPreparing); event != expected {
		t.Errorf("got %q, want %q", event, expected)
	}
}

func TestRecordBuildBuilding(t *testing.T) {
	fakeRecorder := record.NewFakeRecorder(10)
	r := NewEventRecorder(fakeRecorder)
	mosb := &mcfgv1.MachineOSBuild{ObjectMeta: metav1.ObjectMeta{Name: "build-1"}}
	r.RecordBuildBuilding(mosb)
	event := <-fakeRecorder.Events
	if expected := fmt.Sprintf("Normal %s Build is now in progress", EventBuildBuilding); event != expected {
		t.Errorf("got %q, want %q", event, expected)
	}
}

func TestRecordBuildCompleted(t *testing.T) {
	fakeRecorder := record.NewFakeRecorder(10)
	r := NewEventRecorder(fakeRecorder)
	mosb := &mcfgv1.MachineOSBuild{ObjectMeta: metav1.ObjectMeta{Name: "build-1"}}
	r.RecordBuildCompleted(mosb, "registry.example.com/image@sha256:abc")
	event := <-fakeRecorder.Events
	if expected := fmt.Sprintf("Normal %s Build completed successfully, image: registry.example.com/image@sha256:abc", EventBuildCompleted); event != expected {
		t.Errorf("got %q, want %q", event, expected)
	}
}

func TestEventRecordBuildInterrupted(t *testing.T) {
	fakeRecorder := record.NewFakeRecorder(10)
	r := NewEventRecorder(fakeRecorder)
	mosb := &mcfgv1.MachineOSBuild{ObjectMeta: metav1.ObjectMeta{Name: "build-1"}}
	r.RecordBuildInterrupted(mosb, "config changed")
	event := <-fakeRecorder.Events
	if expected := fmt.Sprintf("Warning %s Build interrupted: config changed", EventBuildInterrupted); event != expected {
		t.Errorf("got %q, want %q", event, expected)
	}
}

func TestRecordBuildDeleted(t *testing.T) {
	fakeRecorder := record.NewFakeRecorder(10)
	r := NewEventRecorder(fakeRecorder)
	mosb := &mcfgv1.MachineOSBuild{ObjectMeta: metav1.ObjectMeta{Name: "build-1"}}
	r.RecordBuildDeleted(mosb, "superseded")
	event := <-fakeRecorder.Events
	if expected := fmt.Sprintf("Normal %s Build deleted: superseded", EventBuildDeleted); event != expected {
		t.Errorf("got %q, want %q", event, expected)
	}
}

func TestRecordJobStarted(t *testing.T) {
	fakeRecorder := record.NewFakeRecorder(10)
	r := NewEventRecorder(fakeRecorder)
	mosb := &mcfgv1.MachineOSBuild{ObjectMeta: metav1.ObjectMeta{Name: "build-1"}}
	job := &batchv1.Job{ObjectMeta: metav1.ObjectMeta{Name: "job-1"}}
	r.RecordJobStarted(mosb, job)
	event := <-fakeRecorder.Events
	if expected := fmt.Sprintf("Normal %s Build job started: job-1", EventJobStarted); event != expected {
		t.Errorf("got %q, want %q", event, expected)
	}
}

func TestRecordJobCompleted(t *testing.T) {
	fakeRecorder := record.NewFakeRecorder(10)
	r := NewEventRecorder(fakeRecorder)
	mosb := &mcfgv1.MachineOSBuild{ObjectMeta: metav1.ObjectMeta{Name: "build-1"}}
	job := &batchv1.Job{ObjectMeta: metav1.ObjectMeta{Name: "job-1"}}
	r.RecordJobCompleted(mosb, job)
	event := <-fakeRecorder.Events
	if expected := fmt.Sprintf("Normal %s Build job completed: job-1", EventJobCompleted); event != expected {
		t.Errorf("got %q, want %q", event, expected)
	}
}

func TestRecordJobFailed(t *testing.T) {
	fakeRecorder := record.NewFakeRecorder(10)
	r := NewEventRecorder(fakeRecorder)
	mosb := &mcfgv1.MachineOSBuild{ObjectMeta: metav1.ObjectMeta{Name: "build-1"}}
	job := &batchv1.Job{ObjectMeta: metav1.ObjectMeta{Name: "job-1"}}
	r.RecordJobFailed(mosb, job)
	event := <-fakeRecorder.Events
	expected := fmt.Sprintf("Warning %s Build job %q failed; see MachineOSBuild %q status conditions for details", EventJobFailed, "job-1", "build-1")
	if event != expected {
		t.Errorf("got %q, want %q", event, expected)
	}
}

func TestRecordJobDeleted(t *testing.T) {
	fakeRecorder := record.NewFakeRecorder(10)
	r := NewEventRecorder(fakeRecorder)
	mosb := &mcfgv1.MachineOSBuild{ObjectMeta: metav1.ObjectMeta{Name: "build-1"}}
	r.RecordJobDeleted(mosb, "old-job")
	event := <-fakeRecorder.Events
	if expected := fmt.Sprintf("Normal %s Build job deleted: old-job", EventJobDeleted); event != expected {
		t.Errorf("got %q, want %q", event, expected)
	}
}

func TestRecordConfigReconcileFailed(t *testing.T) {
	fakeRecorder := record.NewFakeRecorder(10)
	r := NewEventRecorder(fakeRecorder)
	mosc := &mcfgv1.MachineOSConfig{ObjectMeta: metav1.ObjectMeta{Name: "cfg-1"}}
	r.RecordConfigReconcileFailed(mosc, fmt.Errorf("bad config"))
	event := <-fakeRecorder.Events
	if expected := fmt.Sprintf("Warning %s Failed to reconcile MachineOSConfig spec change: bad config", EventConfigReconcileFailed); event != expected {
		t.Errorf("got %q, want %q", event, expected)
	}
}

func TestRecordRebuildRequested(t *testing.T) {
	fakeRecorder := record.NewFakeRecorder(10)
	r := NewEventRecorder(fakeRecorder)
	mosc := &mcfgv1.MachineOSConfig{ObjectMeta: metav1.ObjectMeta{Name: "cfg-1"}}
	r.RecordRebuildRequested(mosc, "user annotation")
	event := <-fakeRecorder.Events
	if expected := fmt.Sprintf("Normal %s Rebuild requested: user annotation", EventRebuildRequested); event != expected {
		t.Errorf("got %q, want %q", event, expected)
	}
}

func TestRecordConfigDeleted(t *testing.T) {
	fakeRecorder := record.NewFakeRecorder(10)
	r := NewEventRecorder(fakeRecorder)
	mosc := &mcfgv1.MachineOSConfig{ObjectMeta: metav1.ObjectMeta{Name: "cfg-1"}}
	r.RecordConfigDeleted(mosc)
	event := <-fakeRecorder.Events
	expected := fmt.Sprintf("Normal %s MachineOSConfig %q deleted, removing associated builds", EventConfigDeleted, "cfg-1")
	if event != expected {
		t.Errorf("got %q, want %q", event, expected)
	}
}

func TestRecordBuildRecovered(t *testing.T) {
	fakeRecorder := record.NewFakeRecorder(10)
	r := NewEventRecorder(fakeRecorder)
	mosc := &mcfgv1.MachineOSConfig{
		ObjectMeta: metav1.ObjectMeta{Name: "cfg-1"},
		Spec: mcfgv1.MachineOSConfigSpec{
			MachineConfigPool: mcfgv1.MachineConfigPoolReference{Name: "worker"},
		},
	}
	r.RecordBuildRecovered(mosc)
	event := <-fakeRecorder.Events
	if expected := fmt.Sprintf("Normal %s Build for pool %q recovered from degraded state", EventBuildRecovered, "worker"); event != expected {
		t.Errorf("got %q, want %q", event, expected)
	}
}

// TestNoopEventRecorder verifies that all methods can be called without panic.
func TestNoopEventRecorder(t *testing.T) {
	r := NewNoopEventRecorder()
	mosb := &mcfgv1.MachineOSBuild{}
	mosc := &mcfgv1.MachineOSConfig{}
	job := &batchv1.Job{}
	mcp := &mcfgv1.MachineConfigPool{}

	// These should not panic.
	r.RecordBuildStarted(mosb, mosc)
	r.RecordBuildPreparing(mosb, "msg")
	r.RecordBuildBuilding(mosb)
	r.RecordBuildCompleted(mosb, "img")
	r.RecordBuildFailed(mosb)
	r.RecordBuildInterrupted(mosb, "reason")
	r.RecordBuildDeleted(mosb, "reason")
	r.RecordJobCreated(mosb, job)
	r.RecordJobStarted(mosb, job)
	r.RecordJobCompleted(mosb, job)
	r.RecordJobFailed(mosb, job)
	r.RecordJobDeleted(mosb, "job-name")
	r.RecordConfigReconciled(mosc)
	r.RecordConfigReconcileFailed(mosc, fmt.Errorf("err"))
	r.RecordRebuildRequested(mosc, "reason")
	r.RecordConfigDeleted(mosc)
	r.RecordPoolConfigChanged(mcp, "old", "new")
	r.RecordBuildDegraded(mosc)
	r.RecordBuildRecovered(mosc)
}
