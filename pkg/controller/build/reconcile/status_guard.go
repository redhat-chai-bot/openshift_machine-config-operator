package reconcile

import (
	"fmt"

	mcfgv1 "github.com/openshift/api/machineconfiguration/v1"
	ctrlcommon "github.com/openshift/machine-config-operator/pkg/controller/common"
	"k8s.io/klog/v2"
)

// IsMachineOSBuildStatusUpdateNeeded determines if a MachineOSBuild status
// update is needed by comparing the old (current) status with the desired
// status. It prevents redundant API writes when the MOSB status hasn't
// actually changed (e.g., duplicate job events).
//
// Updates are needed primarily when transitioning between states:
//
//	initial → transient → terminal
//
// Once a build enters a terminal state, no further transitions are valid.
func IsMachineOSBuildStatusUpdateNeeded(oldStatus, curStatus mcfgv1.MachineOSBuildStatus) (bool, string) {
	oldState := ctrlcommon.NewMachineOSBuildStateFromStatus(oldStatus)
	curState := ctrlcommon.NewMachineOSBuildStateFromStatus(curStatus)

	// From having no build conditions to having the initial state set.
	if !oldState.HasBuildConditions() && curState.HasBuildConditions() && curState.IsInInitialState() {
		return true, "in initial state"
	}

	oldTransientState := oldState.GetTransientState()
	curTransientState := curState.GetTransientState()

	// From initial state -> pending or building.
	if oldState.IsInInitialState() && curState.IsInTransientState() {
		return true, fmt.Sprintf("transitioned from initial state -> transient state (%s)", curTransientState)
	}

	// From pending -> building, but not building -> pending.
	if oldState.IsInTransientState() && curState.IsInTransientState() && oldTransientState != curTransientState {
		reason := fmt.Sprintf("transitioned from transient state (%s) -> transient state (%s)", oldTransientState, curTransientState)
		isValid := oldTransientState == mcfgv1.MachineOSBuildPrepared && curTransientState == mcfgv1.MachineOSBuilding
		return isValid, reason
	}

	oldTerminalState := oldState.GetTerminalState()
	curTerminalState := curState.GetTerminalState()

	// From building -> {success, failure, interrupted}
	if oldState.IsInTransientState() && curState.IsInTerminalState() {
		return true, fmt.Sprintf("transitioned from transient state (%s) -> terminal state (%s)", oldTransientState, curTerminalState)
	}

	// From initial state -> {success, failure, interrupted}
	if oldState.IsInInitialState() && curState.IsInTerminalState() {
		return true, fmt.Sprintf("transitioned from initial state -> terminal state (%s)", curTerminalState)
	}

	// Once a build enters a terminal state, it cannot transition to any other state.

	if oldState.IsInTerminalState() && curState.IsInTerminalState() {
		return false, fmt.Sprintf("transitioned from terminal state (%s) -> terminal state (%s)", oldTerminalState, curTerminalState)
	}

	if oldState.IsInTerminalState() && curState.IsInTransientState() {
		return false, fmt.Sprintf("transitioned from terminal state (%s) -> transient state (%s)", oldTerminalState, curTransientState)
	}

	if oldState.IsInTerminalState() && curState.IsInInitialState() {
		return false, fmt.Sprintf("transitioned from terminal state (%s) -> initial state", oldTerminalState)
	}

	return false, ""
}

// logStatusGuardResult logs whether a MOSB status update was skipped or
// allowed, aiding observability.
func logStatusGuardResult(mosbName string, needed bool, reason string) {
	if needed {
		klog.V(4).Infof("MOSB %q status update needed: %s", mosbName, reason)
	} else if reason != "" {
		klog.V(4).Infof("MOSB %q status update skipped: %s", mosbName, reason)
	}
}
