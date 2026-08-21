package services

import (
	"context"
	"fmt"

	"github.com/openshift/machine-config-operator/pkg/apihelpers"
	"github.com/openshift/machine-config-operator/pkg/controller/build/constants"
	"github.com/openshift/machine-config-operator/pkg/controller/build/internal/buildlabels"
	ctrlcommon "github.com/openshift/machine-config-operator/pkg/controller/common"

	mcfgv1 "github.com/openshift/api/machineconfiguration/v1"
	mcfgclientset "github.com/openshift/client-go/machineconfiguration/clientset/versioned"
	mcfglistersv1 "github.com/openshift/client-go/machineconfiguration/listers/machineconfiguration/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/util/retry"
	"k8s.io/klog/v2"
)

// DegradedHandler manages the ImageBuildDegraded condition on MachineConfigPools.
type DegradedHandler interface {
	// InitializeBuildDegraded clears the BuildDegraded condition (sets False)
	// when a new build starts, with reason MachineConfigPoolBuilding.
	InitializeBuildDegraded(ctx context.Context, pool *mcfgv1.MachineConfigPool) error

	// SyncBuildSuccess sets BuildDegraded=False with reason
	// MachineConfigPoolBuildSuccess when a build completes successfully.
	SyncBuildSuccess(ctx context.Context, pool *mcfgv1.MachineConfigPool) error

	// SyncBuildFailure sets BuildDegraded=True with reason
	// MachineConfigPoolBuildFailed when a build fails. Returns the original
	// buildErr so callers can propagate it.
	SyncBuildFailure(ctx context.Context, pool *mcfgv1.MachineConfigPool, buildErr error, mosbName string) error

	// UpdateImageBuildDegraded examines all MachineOSBuilds for the given
	// MachineOSConfig and sets the ImageBuildDegraded condition based on the
	// currently active build's status.
	UpdateImageBuildDegraded(ctx context.Context, pool *mcfgv1.MachineConfigPool, mosc *mcfgv1.MachineOSConfig) error
}

// Compile-time interface satisfaction check.
var _ DegradedHandler = &degradedHandler{}

// degradedHandler is the production implementation of DegradedHandler.
type degradedHandler struct {
	mcfgclient mcfgclientset.Interface
	mosbLister mcfglistersv1.MachineOSBuildLister
}

// NewDegradedHandler creates a DegradedHandler that writes MCP conditions using the given client.
func NewDegradedHandler(
	mcfgclient mcfgclientset.Interface,
	mosbLister mcfglistersv1.MachineOSBuildLister,
) DegradedHandler {
	return &degradedHandler{
		mcfgclient: mcfgclient,
		mosbLister: mosbLister,
	}
}

func (d *degradedHandler) InitializeBuildDegraded(ctx context.Context, pool *mcfgv1.MachineConfigPool) error {
	// No-op if already False.
	if apihelpers.IsMachineConfigPoolConditionFalse(pool.Status.Conditions, mcfgv1.MachineConfigPoolImageBuildDegraded) {
		return nil
	}

	return retry.RetryOnConflict(retry.DefaultRetry, func() error {
		currentPool, err := d.mcfgclient.MachineconfigurationV1().MachineConfigPools().Get(ctx, pool.Name, metav1.GetOptions{})
		if err != nil {
			return err
		}

		cond := apihelpers.NewMachineConfigPoolCondition(
			mcfgv1.MachineConfigPoolImageBuildDegraded,
			corev1.ConditionFalse,
			string(mcfgv1.MachineConfigPoolBuilding),
			"Build started for pool "+currentPool.Name,
		)
		apihelpers.SetMachineConfigPoolCondition(&currentPool.Status, *cond)

		_, err = d.mcfgclient.MachineconfigurationV1().MachineConfigPools().UpdateStatus(ctx, currentPool, metav1.UpdateOptions{})
		return err
	})
}

func (d *degradedHandler) SyncBuildSuccess(ctx context.Context, pool *mcfgv1.MachineConfigPool) error {
	return retry.RetryOnConflict(retry.DefaultRetry, func() error {
		currentPool, err := d.mcfgclient.MachineconfigurationV1().MachineConfigPools().Get(ctx, pool.Name, metav1.GetOptions{})
		if err != nil {
			return err
		}

		cond := apihelpers.NewMachineConfigPoolCondition(
			mcfgv1.MachineConfigPoolImageBuildDegraded,
			corev1.ConditionFalse,
			string(mcfgv1.MachineConfigPoolBuildSuccess),
			"Build succeeded for pool "+currentPool.Name,
		)
		apihelpers.SetMachineConfigPoolCondition(&currentPool.Status, *cond)

		_, err = d.mcfgclient.MachineconfigurationV1().MachineConfigPools().UpdateStatus(ctx, currentPool, metav1.UpdateOptions{})
		return err
	})
}

func (d *degradedHandler) SyncBuildFailure(ctx context.Context, pool *mcfgv1.MachineConfigPool, buildErr error, mosbName string) error {
	updateErr := retry.RetryOnConflict(retry.DefaultRetry, func() error {
		currentPool, err := d.mcfgclient.MachineconfigurationV1().MachineConfigPools().Get(ctx, pool.Name, metav1.GetOptions{})
		if err != nil {
			return err
		}

		cond := apihelpers.NewMachineConfigPoolCondition(
			mcfgv1.MachineConfigPoolImageBuildDegraded,
			corev1.ConditionTrue,
			string(mcfgv1.MachineConfigPoolBuildFailed),
			fmt.Sprintf("Failed to build OS image for pool %s (MachineOSBuild: %s): %v", currentPool.Name, mosbName, buildErr),
		)
		apihelpers.SetMachineConfigPoolCondition(&currentPool.Status, *cond)

		_, updateErr := d.mcfgclient.MachineconfigurationV1().MachineConfigPools().UpdateStatus(ctx, currentPool, metav1.UpdateOptions{})
		return updateErr
	})
	if updateErr != nil {
		klog.Errorf("Error updating MachineConfigPool %s BuildDegraded status: %v", pool.Name, updateErr)
	}
	return buildErr
}

func (d *degradedHandler) UpdateImageBuildDegraded(ctx context.Context, pool *mcfgv1.MachineConfigPool, mosc *mcfgv1.MachineOSConfig) error {
	sel := buildlabels.MachineOSBuildForPoolSelector(mosc)
	mosbList, err := d.mosbLister.List(sel)
	if err != nil {
		return fmt.Errorf("could not get MachineOSBuilds for MachineOSConfig %q: %w", mosc.Name, err)
	}

	activeBuild := getCurrentBuild(mosc, mosbList)

	// No builds → clear any existing degraded condition.
	if activeBuild == nil {
		return d.SyncBuildSuccess(ctx, pool)
	}

	activeState := ctrlcommon.NewMachineOSBuildState(activeBuild)

	switch {
	case activeState.IsBuildFailure():
		buildError := getBuildErrorFromMOSB(activeBuild)
		return d.SyncBuildFailure(ctx, pool, buildError, activeBuild.Name)
	case activeState.IsBuildSuccess():
		return d.SyncBuildSuccess(ctx, pool)
	case activeState.IsBuilding(), activeState.IsBuildPrepared():
		return d.InitializeBuildDegraded(ctx, pool)
	}

	return nil
}

// getCurrentBuild finds the most relevant build from a list of MachineOSBuilds.
// Priority: 1) Referenced by MOSC annotation, 2) Building/Prepared, 3) Most recent.
func getCurrentBuild(mosc *mcfgv1.MachineOSConfig, mosbList []*mcfgv1.MachineOSBuild) *mcfgv1.MachineOSBuild {
	var activeBuild *mcfgv1.MachineOSBuild
	var mostRecentBuild *mcfgv1.MachineOSBuild

	currentBuildName := mosc.Annotations[constants.CurrentMachineOSBuildAnnotationKey]

	for _, mosb := range mosbList {
		if currentBuildName != "" && mosb.Name == currentBuildName {
			activeBuild = mosb
			break
		}
	}

	if activeBuild == nil {
		for _, mosb := range mosbList {
			mosbState := ctrlcommon.NewMachineOSBuildState(mosb)
			if mosbState.IsBuilding() || mosbState.IsBuildPrepared() {
				activeBuild = mosb
				break
			}
		}
	}

	for _, mosb := range mosbList {
		if mostRecentBuild == nil || mosb.CreationTimestamp.After(mostRecentBuild.CreationTimestamp.Time) {
			mostRecentBuild = mosb
		}
	}

	if activeBuild == nil {
		activeBuild = mostRecentBuild
	}

	return activeBuild
}

// getBuildErrorFromMOSB extracts a build error from a MachineOSBuild's conditions.
func getBuildErrorFromMOSB(mosb *mcfgv1.MachineOSBuild) error {
	for _, cond := range mosb.Status.Conditions {
		if cond.Type == string(mcfgv1.MachineOSBuildFailed) && cond.Status == metav1.ConditionTrue {
			return fmt.Errorf("%s", cond.Message)
		}
	}
	return fmt.Errorf("build %s failed (no failure condition found)", mosb.Name)
}
