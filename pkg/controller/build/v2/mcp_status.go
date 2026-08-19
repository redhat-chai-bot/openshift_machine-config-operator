package v2

import (
	"context"
	"fmt"

	mcfgv1 "github.com/openshift/api/machineconfiguration/v1"
	mcfgclientset "github.com/openshift/client-go/machineconfiguration/clientset/versioned"
	"github.com/openshift/machine-config-operator/pkg/apihelpers"
	"github.com/openshift/machine-config-operator/pkg/controller/build/constants"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/client-go/util/retry"
	"k8s.io/klog/v2"
)

// nonBuildDegradedTypes lists MCP condition types that indicate degradation
// unrelated to image builds. When any of these is True, new builds should be
// prevented. MachineConfigPoolImageBuildDegraded is intentionally excluded so
// that pools degraded only from a build failure can still retry.
var nonBuildDegradedTypes = []mcfgv1.MachineConfigPoolConditionType{
	mcfgv1.MachineConfigPoolNodeDegraded,
	mcfgv1.MachineConfigPoolRenderDegraded,
	mcfgv1.MachineConfigPoolPinnedImageSetsDegraded,
	mcfgv1.MachineConfigPoolSynchronizerDegraded,
}

// MCPStatusManager manages the ImageBuildDegraded condition on MachineConfigPools.
type MCPStatusManager struct {
	mcfgclient mcfgclientset.Interface
	*listers
}

// NewMCPStatusManager creates a new MCPStatusManager.
func NewMCPStatusManager(mcfgclient mcfgclientset.Interface, l *listers) *MCPStatusManager {
	return &MCPStatusManager{
		mcfgclient: mcfgclient,
		listers:    l,
	}
}

// ShouldPreventBuild returns true if the MCP has any non-build degradation
// condition set to True (node, render, pinned image sets, or synchronizer
// degradation). Pools degraded only due to ImageBuildDegraded are allowed to
// start new builds (retry).
func (m *MCPStatusManager) ShouldPreventBuild(mcp *mcfgv1.MachineConfigPool) bool {
	for _, condType := range nonBuildDegradedTypes {
		if apihelpers.IsMachineConfigPoolConditionTrue(mcp.Status.Conditions, condType) {
			return true
		}
	}
	return false
}

// SetBuildStarted clears the ImageBuildDegraded condition (sets it to False)
// when a new build starts, allowing retry after a previous failure.
func (m *MCPStatusManager) SetBuildStarted(ctx context.Context, mcp *mcfgv1.MachineConfigPool) error {
	return m.MarkMCPBuildable(ctx, mcp,
		string(mcfgv1.MachineConfigPoolBuilding),
		"Build started for pool "+mcp.Name,
	)
}

// SetBuildSucceeded clears the ImageBuildDegraded condition (sets it to False)
// when a build completes successfully.
func (m *MCPStatusManager) SetBuildSucceeded(ctx context.Context, mcp *mcfgv1.MachineConfigPool) error {
	return m.MarkMCPBuildable(ctx, mcp,
		string(mcfgv1.MachineConfigPoolBuildSuccess),
		"Build succeeded for pool "+mcp.Name,
	)
}

// SetBuildFailed sets ImageBuildDegraded=True on the MCP. It returns the
// original buildErr so callers can propagate the build error.
func (m *MCPStatusManager) SetBuildFailed(ctx context.Context, mcp *mcfgv1.MachineConfigPool, buildErr error, mosbName string) error {
	updateErr := m.MarkMCPNotBuildable(ctx, mcp,
		string(mcfgv1.MachineConfigPoolBuildFailed),
		fmt.Sprintf("Failed to build OS image for pool %s (MachineOSBuild: %s): %v", mcp.Name, mosbName, buildErr),
	)
	if updateErr != nil {
		klog.Errorf("Error updating MachineConfigPool %s BuildDegraded status: %v", mcp.Name, updateErr)
	}
	// Always return the original build error.
	return buildErr
}

// UpdateFromActiveBuild examines the MachineOSBuilds for the given MOSC and
// updates the MCP's ImageBuildDegraded condition based on the currently active build.
func (m *MCPStatusManager) UpdateFromActiveBuild(ctx context.Context, mcp *mcfgv1.MachineConfigPool, mosc *mcfgv1.MachineOSConfig) error {
	mosbList, err := m.getMachineOSBuildsForMachineOSConfig(mosc)
	if err != nil {
		return fmt.Errorf("could not get MachineOSBuilds for MachineOSConfig %q: %w", mosc.Name, err)
	}

	activeBuild := getCurrentBuild(mosc, mosbList)

	// No builds exist — clear any existing BuildDegraded condition.
	if activeBuild == nil {
		return m.SetBuildSucceeded(ctx, mcp)
	}

	switch {
	case isMOSBInTerminalState(activeBuild) && getMOSBTerminalState(activeBuild) == mcfgv1.MachineOSBuildFailed:
		buildError := getBuildErrorFromMOSB(activeBuild)
		return m.SetBuildFailed(ctx, mcp, buildError, activeBuild.Name)
	case isMOSBInTerminalState(activeBuild) && getMOSBTerminalState(activeBuild) == mcfgv1.MachineOSBuildSucceeded:
		return m.SetBuildSucceeded(ctx, mcp)
	case isMOSBInTransientState(activeBuild):
		return m.SetBuildStarted(ctx, mcp)
	}

	return nil
}

// SetMCPBuildability is a generic method to set the ImageBuildDegraded condition
// on a MachineConfigPool to the given status with reason and message.
func (m *MCPStatusManager) SetMCPBuildability(ctx context.Context, mcp *mcfgv1.MachineConfigPool, status corev1.ConditionStatus, reason, message string) error {
	return retry.RetryOnConflict(retry.DefaultRetry, func() error {
		currentPool, err := m.mcfgclient.MachineconfigurationV1().MachineConfigPools().Get(ctx, mcp.Name, metav1.GetOptions{})
		if err != nil {
			return err
		}

		cond := apihelpers.NewMachineConfigPoolCondition(
			mcfgv1.MachineConfigPoolImageBuildDegraded,
			status,
			reason,
			message,
		)
		apihelpers.SetMachineConfigPoolCondition(&currentPool.Status, *cond)

		_, err = m.mcfgclient.MachineconfigurationV1().MachineConfigPools().UpdateStatus(ctx, currentPool, metav1.UpdateOptions{})
		return err
	})
}

// MarkMCPBuildable sets ImageBuildDegraded=False on the MCP (pool is healthy
// for builds). This is a no-op if the condition is already False with the
// same reason.
func (m *MCPStatusManager) MarkMCPBuildable(ctx context.Context, mcp *mcfgv1.MachineConfigPool, reason, message string) error {
	// Fast-path: if already False, skip the update.
	if apihelpers.IsMachineConfigPoolConditionFalse(mcp.Status.Conditions, mcfgv1.MachineConfigPoolImageBuildDegraded) {
		existing := apihelpers.GetMachineConfigPoolCondition(mcp.Status, mcfgv1.MachineConfigPoolImageBuildDegraded)
		if existing != nil && existing.Reason == reason {
			return nil
		}
	}
	return m.SetMCPBuildability(ctx, mcp, corev1.ConditionFalse, reason, message)
}

// MarkMCPNotBuildable sets ImageBuildDegraded=True on the MCP (pool has a
// build failure).
func (m *MCPStatusManager) MarkMCPNotBuildable(ctx context.Context, mcp *mcfgv1.MachineConfigPool, reason, message string) error {
	return m.SetMCPBuildability(ctx, mcp, corev1.ConditionTrue, reason, message)
}

// getMachineOSBuildsForMachineOSConfig returns all MachineOSBuilds that belong
// to the given MachineOSConfig by matching labels.
func (m *MCPStatusManager) getMachineOSBuildsForMachineOSConfig(mosc *mcfgv1.MachineOSConfig) ([]*mcfgv1.MachineOSBuild, error) {
	sel := labels.SelectorFromSet(labels.Set{
		constants.MachineOSConfigNameLabelKey: mosc.Name,
	})
	return m.machineOSBuildLister.List(sel)
}

// getCurrentBuild finds the most relevant MachineOSBuild from a list.
// Priority: 1) Referenced by MOSC annotation, 2) Building/Prepared, 3) Most recent.
func getCurrentBuild(mosc *mcfgv1.MachineOSConfig, mosbList []*mcfgv1.MachineOSBuild) *mcfgv1.MachineOSBuild {
	// First: look for the build currently referenced by the MOSC.
	for _, mosb := range mosbList {
		if isCurrentBuildAnnotationEqual(mosc, mosb) {
			return mosb
		}
	}

	// Second: look for active builds (building/prepared).
	for _, mosb := range mosbList {
		if isMOSBInTransientState(mosb) {
			return mosb
		}
	}

	// Third: return the most recent build.
	var mostRecent *mcfgv1.MachineOSBuild
	for _, mosb := range mosbList {
		if mostRecent == nil || mosb.CreationTimestamp.After(mostRecent.CreationTimestamp.Time) {
			mostRecent = mosb
		}
	}
	return mostRecent
}

// getBuildErrorFromMOSB extracts meaningful error info from a MachineOSBuild's
// Failed condition.
func getBuildErrorFromMOSB(mosb *mcfgv1.MachineOSBuild) error {
	for _, condition := range mosb.Status.Conditions {
		if condition.Type == string(mcfgv1.MachineOSBuildFailed) && condition.Status == metav1.ConditionTrue {
			return fmt.Errorf("%s: %s", condition.Reason, condition.Message)
		}
	}
	return fmt.Errorf("build failed for unknown reason")
}
