package services

import (
	"fmt"

	mcfgv1 "github.com/openshift/api/machineconfiguration/v1"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/client-go/tools/record"
)

// Event reason constants for OCL processes.
const (
	// Build lifecycle events
	EventBuildStarted     = "BuildStarted"
	EventBuildPreparing   = "BuildPreparing"
	EventBuildBuilding    = "BuildBuilding"
	EventBuildCompleted   = "BuildCompleted"
	EventBuildFailed      = "BuildFailed"
	EventBuildInterrupted = "BuildInterrupted"
	EventBuildDeleted     = "BuildDeleted"

	// Job events
	EventJobCreated   = "JobCreated"
	EventJobStarted   = "JobStarted"
	EventJobCompleted = "JobCompleted"
	EventJobFailed    = "JobFailed"
	EventJobDeleted   = "JobDeleted"

	// Config events
	EventConfigReconciled      = "ConfigReconciled"
	EventConfigReconcileFailed = "ConfigReconcileFailed"
	EventRebuildRequested      = "RebuildRequested"
	EventConfigDeleted         = "ConfigDeleted"

	// MachineConfigPool events
	EventPoolConfigChanged = "PoolConfigChanged"

	// Degradation events
	EventBuildDegraded  = "BuildDegraded"
	EventBuildRecovered = "BuildRecovered"
)

// EventRecorder defines the interface for recording OCL-specific Kubernetes events.
type EventRecorder interface {
	// Build lifecycle
	RecordBuildStarted(mosb *mcfgv1.MachineOSBuild, mosc *mcfgv1.MachineOSConfig)
	RecordBuildPreparing(mosb *mcfgv1.MachineOSBuild, message string)
	RecordBuildBuilding(mosb *mcfgv1.MachineOSBuild)
	RecordBuildCompleted(mosb *mcfgv1.MachineOSBuild, imagePullspec string)
	RecordBuildFailed(mosb *mcfgv1.MachineOSBuild)
	RecordBuildInterrupted(mosb *mcfgv1.MachineOSBuild, reason string)
	RecordBuildDeleted(mosb *mcfgv1.MachineOSBuild, reason string)

	// Job events
	RecordJobCreated(mosb *mcfgv1.MachineOSBuild, job *batchv1.Job)
	RecordJobStarted(mosb *mcfgv1.MachineOSBuild, job *batchv1.Job)
	RecordJobCompleted(mosb *mcfgv1.MachineOSBuild, job *batchv1.Job)
	RecordJobFailed(mosb *mcfgv1.MachineOSBuild, job *batchv1.Job)
	RecordJobDeleted(mosb *mcfgv1.MachineOSBuild, jobName string)

	// Config events
	RecordConfigReconciled(mosc *mcfgv1.MachineOSConfig)
	RecordConfigReconcileFailed(mosc *mcfgv1.MachineOSConfig, err error)
	RecordRebuildRequested(mosc *mcfgv1.MachineOSConfig, reason string)
	RecordConfigDeleted(mosc *mcfgv1.MachineOSConfig)

	// Pool events
	RecordPoolConfigChanged(mcp *mcfgv1.MachineConfigPool, oldConfig, newConfig string)

	// Degradation events
	RecordBuildDegraded(mosc *mcfgv1.MachineOSConfig)
	RecordBuildRecovered(mosc *mcfgv1.MachineOSConfig)
}

// oclEventRecorder implements EventRecorder by wrapping a Kubernetes event recorder.
type oclEventRecorder struct {
	recorder record.EventRecorder
}

// NewEventRecorder creates an EventRecorder backed by the given Kubernetes event recorder.
func NewEventRecorder(recorder record.EventRecorder) EventRecorder {
	return &oclEventRecorder{recorder: recorder}
}

// --- Build lifecycle ---

func (r *oclEventRecorder) RecordBuildStarted(mosb *mcfgv1.MachineOSBuild, mosc *mcfgv1.MachineOSConfig) {
	r.recorder.Event(mosb, corev1.EventTypeNormal, EventBuildStarted,
		fmt.Sprintf("Started build for pool %q with config %q",
			mosc.Spec.MachineConfigPool.Name, mosb.Spec.MachineConfig.Name))
}

func (r *oclEventRecorder) RecordBuildPreparing(mosb *mcfgv1.MachineOSBuild, message string) {
	r.recorder.Event(mosb, corev1.EventTypeNormal, EventBuildPreparing,
		fmt.Sprintf("Preparing build: %s", message))
}

func (r *oclEventRecorder) RecordBuildBuilding(mosb *mcfgv1.MachineOSBuild) {
	r.recorder.Event(mosb, corev1.EventTypeNormal, EventBuildBuilding,
		"Build is now in progress")
}

func (r *oclEventRecorder) RecordBuildCompleted(mosb *mcfgv1.MachineOSBuild, imagePullspec string) {
	r.recorder.Event(mosb, corev1.EventTypeNormal, EventBuildCompleted,
		fmt.Sprintf("Build completed successfully, image: %s", imagePullspec))
}

func (r *oclEventRecorder) RecordBuildFailed(mosb *mcfgv1.MachineOSBuild) {
	r.recorder.Event(mosb, corev1.EventTypeWarning, EventBuildFailed,
		fmt.Sprintf("Build failed; see MachineOSBuild %q status conditions for details", mosb.Name))
}

func (r *oclEventRecorder) RecordBuildInterrupted(mosb *mcfgv1.MachineOSBuild, reason string) {
	r.recorder.Event(mosb, corev1.EventTypeWarning, EventBuildInterrupted,
		fmt.Sprintf("Build interrupted: %s", reason))
}

func (r *oclEventRecorder) RecordBuildDeleted(mosb *mcfgv1.MachineOSBuild, reason string) {
	r.recorder.Event(mosb, corev1.EventTypeNormal, EventBuildDeleted,
		fmt.Sprintf("Build deleted: %s", reason))
}

// --- Job events ---

func (r *oclEventRecorder) RecordJobCreated(mosb *mcfgv1.MachineOSBuild, job *batchv1.Job) {
	r.recorder.Event(mosb, corev1.EventTypeNormal, EventJobCreated,
		fmt.Sprintf("Created build job: %s", job.Name))
}

func (r *oclEventRecorder) RecordJobStarted(mosb *mcfgv1.MachineOSBuild, job *batchv1.Job) {
	r.recorder.Event(mosb, corev1.EventTypeNormal, EventJobStarted,
		fmt.Sprintf("Build job started: %s", job.Name))
}

func (r *oclEventRecorder) RecordJobCompleted(mosb *mcfgv1.MachineOSBuild, job *batchv1.Job) {
	r.recorder.Event(mosb, corev1.EventTypeNormal, EventJobCompleted,
		fmt.Sprintf("Build job completed: %s", job.Name))
}

func (r *oclEventRecorder) RecordJobFailed(mosb *mcfgv1.MachineOSBuild, job *batchv1.Job) {
	r.recorder.Event(mosb, corev1.EventTypeWarning, EventJobFailed,
		fmt.Sprintf("Build job %q failed; see MachineOSBuild %q status conditions for details", job.Name, mosb.Name))
}

func (r *oclEventRecorder) RecordJobDeleted(mosb *mcfgv1.MachineOSBuild, jobName string) {
	r.recorder.Event(mosb, corev1.EventTypeNormal, EventJobDeleted,
		fmt.Sprintf("Build job deleted: %s", jobName))
}

// --- Config events ---

func (r *oclEventRecorder) RecordConfigReconciled(mosc *mcfgv1.MachineOSConfig) {
	r.recorder.Event(mosc, corev1.EventTypeNormal, EventConfigReconciled,
		"MachineOSConfig spec change detected and reconciled: new build created or reused")
}

func (r *oclEventRecorder) RecordConfigReconcileFailed(mosc *mcfgv1.MachineOSConfig, err error) {
	r.recorder.Event(mosc, corev1.EventTypeWarning, EventConfigReconcileFailed,
		fmt.Sprintf("Failed to reconcile MachineOSConfig spec change: %v", err))
}

func (r *oclEventRecorder) RecordRebuildRequested(mosc *mcfgv1.MachineOSConfig, reason string) {
	r.recorder.Event(mosc, corev1.EventTypeNormal, EventRebuildRequested,
		fmt.Sprintf("Rebuild requested: %s", reason))
}

func (r *oclEventRecorder) RecordConfigDeleted(mosc *mcfgv1.MachineOSConfig) {
	r.recorder.Event(mosc, corev1.EventTypeNormal, EventConfigDeleted,
		fmt.Sprintf("MachineOSConfig %q deleted, removing associated builds", mosc.Name))
}

// --- Pool events ---

func (r *oclEventRecorder) RecordPoolConfigChanged(mcp *mcfgv1.MachineConfigPool, oldConfig, newConfig string) {
	r.recorder.Event(mcp, corev1.EventTypeNormal, EventPoolConfigChanged,
		fmt.Sprintf("Rendered config changed from %s to %s", oldConfig, newConfig))
}

// --- Degradation events ---

func (r *oclEventRecorder) RecordBuildDegraded(mosc *mcfgv1.MachineOSConfig) {
	r.recorder.Event(mosc, corev1.EventTypeWarning, EventBuildDegraded,
		fmt.Sprintf("Build for pool %q degraded; see MachineOSBuild status conditions for details", mosc.Spec.MachineConfigPool.Name))
}

func (r *oclEventRecorder) RecordBuildRecovered(mosc *mcfgv1.MachineOSConfig) {
	r.recorder.Event(mosc, corev1.EventTypeNormal, EventBuildRecovered,
		fmt.Sprintf("Build for pool %q recovered from degraded state", mosc.Spec.MachineConfigPool.Name))
}

// noopEventRecorder is a no-op implementation of EventRecorder for testing.
type noopEventRecorder struct{}

// NewNoopEventRecorder returns an EventRecorder that silently discards all events.
func NewNoopEventRecorder() EventRecorder { return &noopEventRecorder{} }

func (*noopEventRecorder) RecordBuildStarted(*mcfgv1.MachineOSBuild, *mcfgv1.MachineOSConfig)  {}
func (*noopEventRecorder) RecordBuildPreparing(*mcfgv1.MachineOSBuild, string)                  {}
func (*noopEventRecorder) RecordBuildBuilding(*mcfgv1.MachineOSBuild)                           {}
func (*noopEventRecorder) RecordBuildCompleted(*mcfgv1.MachineOSBuild, string)                  {}
func (*noopEventRecorder) RecordBuildFailed(*mcfgv1.MachineOSBuild)                             {}
func (*noopEventRecorder) RecordBuildInterrupted(*mcfgv1.MachineOSBuild, string)                {}
func (*noopEventRecorder) RecordBuildDeleted(*mcfgv1.MachineOSBuild, string)                    {}
func (*noopEventRecorder) RecordJobCreated(*mcfgv1.MachineOSBuild, *batchv1.Job)                {}
func (*noopEventRecorder) RecordJobStarted(*mcfgv1.MachineOSBuild, *batchv1.Job)                {}
func (*noopEventRecorder) RecordJobCompleted(*mcfgv1.MachineOSBuild, *batchv1.Job)              {}
func (*noopEventRecorder) RecordJobFailed(*mcfgv1.MachineOSBuild, *batchv1.Job)                 {}
func (*noopEventRecorder) RecordJobDeleted(*mcfgv1.MachineOSBuild, string)                      {}
func (*noopEventRecorder) RecordConfigReconciled(*mcfgv1.MachineOSConfig)                       {}
func (*noopEventRecorder) RecordConfigReconcileFailed(*mcfgv1.MachineOSConfig, error)            {}
func (*noopEventRecorder) RecordRebuildRequested(*mcfgv1.MachineOSConfig, string)                {}
func (*noopEventRecorder) RecordConfigDeleted(*mcfgv1.MachineOSConfig)                          {}
func (*noopEventRecorder) RecordPoolConfigChanged(*mcfgv1.MachineConfigPool, string, string)     {}
func (*noopEventRecorder) RecordBuildDegraded(*mcfgv1.MachineOSConfig)                          {}
func (*noopEventRecorder) RecordBuildRecovered(*mcfgv1.MachineOSConfig)                         {}
