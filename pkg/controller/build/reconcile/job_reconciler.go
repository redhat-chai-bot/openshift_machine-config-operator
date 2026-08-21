package reconcile

import (
	"context"
	"fmt"

	mcfgv1 "github.com/openshift/api/machineconfiguration/v1"
	mcfgclientset "github.com/openshift/client-go/machineconfiguration/clientset/versioned"
	mcfglistersv1 "github.com/openshift/client-go/machineconfiguration/listers/machineconfiguration/v1"
	"github.com/openshift/machine-config-operator/pkg/controller/build/buildrequest"
	"github.com/openshift/machine-config-operator/pkg/controller/build/constants"
	"github.com/openshift/machine-config-operator/pkg/controller/build/imagebuilder"
	"github.com/openshift/machine-config-operator/pkg/controller/build/services"
	"github.com/openshift/machine-config-operator/pkg/controller/build/utils"
	ctrlcommon "github.com/openshift/machine-config-operator/pkg/controller/common"
	k8serrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	batchlisterv1 "k8s.io/client-go/listers/batch/v1"
	clientset "k8s.io/client-go/kubernetes"
	"k8s.io/client-go/tools/cache"
	"k8s.io/klog/v2"
)

// JobReconciler handles level-based reconciliation of build Job objects.
// It maps Job completion/failure status onto MachineOSBuild status.
type JobReconciler struct {
	mcfgclient mcfgclientset.Interface
	kubeclient clientset.Interface

	jobLister  batchlisterv1.JobLister
	mosbLister mcfglistersv1.MachineOSBuildLister
	moscLister mcfglistersv1.MachineOSConfigLister

	events  services.EventRecorder
	metrics services.MetricsRecorder

	utilListers *utils.Listers
}

// NewJobReconciler constructs a JobReconciler from the shared Deps container.
func NewJobReconciler(d Deps) *JobReconciler {
	a := d.Accessors
	return &JobReconciler{
		mcfgclient: a.Mcfgclient,
		kubeclient: a.Kubeclient,
		jobLister:  a.JobLister,
		mosbLister: a.MachineOSBuildLister,
		moscLister: a.MachineOSConfigLister,
		events:     d.Events,
		metrics:    d.Metrics,
		utilListers: &utils.Listers{
			MachineOSBuildLister:    a.MachineOSBuildLister,
			MachineOSConfigLister:   a.MachineOSConfigLister,
			MachineConfigPoolLister: a.MachineConfigPoolLister,
			NodeLister:              a.NodeLister,
		},
	}
}

// ReconcileJob is the level-based reconciliation entry point for a Job
// identified by key (namespace/name).
func (r *JobReconciler) ReconcileJob(ctx context.Context, key string) error {
	ns, name, err := cache.SplitMetaNamespaceKey(key)
	if err != nil {
		return fmt.Errorf("invalid job key %q: %w", key, err)
	}

	job, err := r.jobLister.Jobs(ns).Get(name)
	if k8serrors.IsNotFound(err) {
		klog.V(4).Infof("JobReconciler: Job %q deleted", key)
		return nil
	}
	if err != nil {
		return fmt.Errorf("could not get Job %q: %w", key, err)
	}

	// Only process jobs with the MOSB label.
	if !metav1.HasLabel(job.ObjectMeta, constants.MachineOSBuildNameLabelKey) {
		return nil
	}

	mosbName := job.Labels[constants.MachineOSBuildNameLabelKey]
	mosb, err := r.mosbLister.Get(mosbName)
	if k8serrors.IsNotFound(err) {
		klog.V(4).Infof("JobReconciler: MOSB %q for job %q not found, skipping", mosbName, key)
		return nil
	}
	if err != nil {
		return fmt.Errorf("could not get MOSB %q for job %q: %w", mosbName, key, err)
	}

	mosb = mosb.DeepCopy()

	// Map job status → MOSB status.
	return r.mapJobStatusToBuildStatus(ctx, mosb, job)
}

// mapJobStatusToBuildStatus examines the job's current state and updates
// the MachineOSBuild status accordingly. This is idempotent — it reads
// current state and only writes if a transition is needed.
func (r *JobReconciler) mapJobStatusToBuildStatus(ctx context.Context, mosb *mcfgv1.MachineOSBuild, jobObj metav1.Object) error {
	mosc, err := utils.GetMachineOSConfigForMachineOSBuild(mosb, r.utilListers)
	if err != nil {
		if k8serrors.IsNotFound(err) {
			return nil
		}
		return fmt.Errorf("could not get MOSC for MOSB %q: %w", mosb.Name, err)
	}

	builder, err := buildrequest.NewBuilder(jobObj)
	if err != nil {
		return fmt.Errorf("could not create builder from job %q: %w", jobObj.GetName(), err)
	}

	// Use the observer to compute the desired MOSB status from the job.
	observer := imagebuilder.NewJobImageBuildObserverFromBuilder(r.kubeclient, r.mcfgclient, mosb, mosc, builder)
	desiredStatus, err := observer.MachineOSBuildStatus(ctx)
	if err != nil {
		if k8serrors.IsNotFound(err) {
			return nil
		}
		return fmt.Errorf("could not compute status for MOSB %q: %w", mosb.Name, err)
	}

	currentState := ctrlcommon.NewMachineOSBuildState(mosb)

	// Determine if a status update is needed by comparing states.
	desiredMOSB := mosb.DeepCopy()
	desiredMOSB.Status = desiredStatus
	desiredState := ctrlcommon.NewMachineOSBuildState(desiredMOSB)

	// Identify the state transition (if any).
	toBuilding := !currentState.IsBuilding() && desiredState.IsBuilding()
	toSuccess := !currentState.IsBuildSuccess() && desiredState.IsBuildSuccess()
	toFailure := !currentState.IsBuildFailure() && desiredState.IsBuildFailure()
	toInterrupted := !currentState.IsBuildInterrupted() && desiredState.IsBuildInterrupted()

	if !toBuilding && !toSuccess && !toFailure && !toInterrupted {
		return nil
	}

	// Guard: use the state-machine check to prevent redundant or
	// invalid MOSB status writes (e.g., duplicate job events, terminal→terminal).
	// Events and metrics are emitted only AFTER the guard confirms the
	// status write will proceed, making JobReconciler the sole authority
	// for terminal-event emission.
	updateNeeded, reason := IsMachineOSBuildStatusUpdateNeeded(mosb.Status, desiredStatus)
	logStatusGuardResult(mosb.Name, updateNeeded, reason)
	if !updateNeeded {
		return nil
	}

	poolName := mosc.Spec.MachineConfigPool.Name

	if toBuilding {
		r.events.RecordBuildBuilding(mosb)
		r.metrics.RecordBuildBuilding(poolName)
	}
	if toSuccess {
		r.events.RecordBuildCompleted(mosb, string(desiredStatus.DigestedImagePushSpec))
		r.metrics.RecordBuildCompleted(poolName, mosb.CreationTimestamp.Time)
	}
	if toFailure {
		r.events.RecordBuildFailed(mosb)
		r.metrics.RecordBuildFailed(poolName, mosb.CreationTimestamp.Time)
	}
	if toInterrupted {
		r.events.RecordBuildInterrupted(mosb, "build was interrupted")
		r.metrics.RecordBuildInterrupted(poolName)
	}

	mosb.Status = desiredStatus
	_, err = r.mcfgclient.MachineconfigurationV1().MachineOSBuilds().UpdateStatus(ctx, mosb, metav1.UpdateOptions{})
	if err != nil {
		return fmt.Errorf("could not update MOSB %q status: %w", mosb.Name, err)
	}

	klog.Infof("JobReconciler: updated MOSB %q status from job %s", mosb.Name, jobObj.GetName())
	return nil
}
