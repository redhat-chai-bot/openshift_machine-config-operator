package v2

import (
	"fmt"

	mcfgv1 "github.com/openshift/api/machineconfiguration/v1"
	"github.com/openshift/machine-config-operator/pkg/apihelpers"
	"github.com/openshift/machine-config-operator/pkg/controller/build/constants"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// isMOSBInInitialState returns true if no build progress conditions are true,
// meaning the build has not yet started processing.
func isMOSBInInitialState(mosb *mcfgv1.MachineOSBuild) bool {
	for _, bp := range allBuildProgressConditions {
		if apihelpers.IsMachineOSBuildConditionTrue(mosb.Status.Conditions, bp) {
			return false
		}
	}
	return true
}

// isMOSBInTransientState returns true if the build is in a non-terminal
// in-progress state (Prepared or Building).
func isMOSBInTransientState(mosb *mcfgv1.MachineOSBuild) bool {
	for _, bp := range transientConditions {
		if apihelpers.IsMachineOSBuildConditionTrue(mosb.Status.Conditions, bp) {
			return true
		}
	}
	return false
}

// isMOSBInTerminalState returns true if the build has reached a final state
// (Succeeded, Failed, or Interrupted).
func isMOSBInTerminalState(mosb *mcfgv1.MachineOSBuild) bool {
	for _, bp := range terminalConditions {
		if apihelpers.IsMachineOSBuildConditionTrue(mosb.Status.Conditions, bp) {
			return true
		}
	}
	return false
}

// getMOSBTransientState returns which transient condition is currently true,
// or an empty string if none is.
func getMOSBTransientState(mosb *mcfgv1.MachineOSBuild) mcfgv1.BuildProgress {
	for _, bp := range transientConditions {
		if apihelpers.IsMachineOSBuildConditionTrue(mosb.Status.Conditions, bp) {
			return bp
		}
	}
	return ""
}

// getMOSBTerminalState returns which terminal condition is currently true,
// or an empty string if none is.
func getMOSBTerminalState(mosb *mcfgv1.MachineOSBuild) mcfgv1.BuildProgress {
	for _, bp := range terminalConditions {
		if apihelpers.IsMachineOSBuildConditionTrue(mosb.Status.Conditions, bp) {
			return bp
		}
	}
	return ""
}

// isMOSBStatusUpdateNeeded determines if a MachineOSBuild status update is
// needed based upon state transitions. Returns (needed, reason).
func isMOSBStatusUpdateNeeded(oldStatus, curStatus mcfgv1.MachineOSBuildStatus) (bool, string) {
	oldInitial := isStatusInitial(oldStatus)
	curInitial := isStatusInitial(curStatus)
	oldTransient := isStatusTransient(oldStatus)
	curTransient := isStatusTransient(curStatus)
	oldTerminal := isStatusTerminal(oldStatus)
	curTerminal := isStatusTerminal(curStatus)

	oldTransientState := getStatusTransientState(oldStatus)
	curTransientState := getStatusTransientState(curStatus)
	oldTerminalState := getStatusTerminalState(oldStatus)
	curTerminalState := getStatusTerminalState(curStatus)

	// From having no conditions to having the initial state set.
	if !hasAnyBuildCondition(oldStatus) && hasAnyBuildCondition(curStatus) && curInitial {
		return true, "in initial state"
	}

	// Initial -> transient
	if oldInitial && curTransient {
		return true, fmt.Sprintf("transitioned from initial state -> transient state (%s)", curTransientState)
	}

	// Transient -> different transient (only Prepared -> Building is valid)
	if oldTransient && curTransient && oldTransientState != curTransientState {
		reason := fmt.Sprintf("transitioned from transient state (%s) -> transient state (%s)", oldTransientState, curTransientState)
		isValid := oldTransientState == mcfgv1.MachineOSBuildPrepared && curTransientState == mcfgv1.MachineOSBuilding
		return isValid, reason
	}

	// Transient -> terminal
	if oldTransient && curTerminal {
		return true, fmt.Sprintf("transitioned from transient state (%s) -> terminal state (%s)", oldTransientState, curTerminalState)
	}

	// Initial -> terminal (rare but possible)
	if oldInitial && curTerminal {
		return true, fmt.Sprintf("transitioned from initial state -> terminal state (%s)", curTerminalState)
	}

	// Terminal -> terminal (invalid)
	if oldTerminal && curTerminal {
		return false, fmt.Sprintf("transitioned from terminal state (%s) -> terminal state (%s)", oldTerminalState, curTerminalState)
	}

	// Terminal -> transient (invalid)
	if oldTerminal && curTransient {
		return false, fmt.Sprintf("transitioned from terminal state (%s) -> transient state (%s)", oldTerminalState, curTransientState)
	}

	// Terminal -> initial (invalid)
	if oldTerminal && curInitial {
		return false, fmt.Sprintf("transitioned from terminal state (%s) -> initial state", oldTerminalState)
	}

	return false, ""
}

// Annotation helpers

// hasCurrentBuildAnnotation returns true if the MachineOSConfig has a non-empty
// current build annotation.
func hasCurrentBuildAnnotation(mosc *mcfgv1.MachineOSConfig) bool {
	return metav1.HasAnnotation(mosc.ObjectMeta, constants.CurrentMachineOSBuildAnnotationKey) &&
		mosc.Annotations[constants.CurrentMachineOSBuildAnnotationKey] != ""
}

// isCurrentBuildAnnotationEqual returns true if the MachineOSConfig's current
// build annotation equals the MachineOSBuild's name.
func isCurrentBuildAnnotationEqual(mosc *mcfgv1.MachineOSConfig, mosb *mcfgv1.MachineOSBuild) bool {
	if !hasCurrentBuildAnnotation(mosc) {
		return false
	}
	return mosc.Annotations[constants.CurrentMachineOSBuildAnnotationKey] == mosb.Name
}

// hasRebuildAnnotation returns true if the MachineOSConfig has the rebuild annotation.
func hasRebuildAnnotation(mosc *mcfgv1.MachineOSConfig) bool {
	return metav1.HasAnnotation(mosc.ObjectMeta, constants.RebuildMachineOSConfigAnnotationKey)
}

// Pre-built image helpers

// getPreBuiltImage returns the pre-built image from a MachineOSConfig's annotations
// and a boolean indicating if it exists and is non-empty.
func getPreBuiltImage(mosc *mcfgv1.MachineOSConfig) (string, bool) {
	image, exists := mosc.Annotations[constants.PreBuiltImageAnnotationKey]
	return image, exists && image != ""
}

// shouldSeedWithPreBuiltImage returns true if the MachineOSConfig should be seeded
// with a pre-built image (has the annotation but no current build annotation).
func shouldSeedWithPreBuiltImage(mosc *mcfgv1.MachineOSConfig) bool {
	_, hasImage := getPreBuiltImage(mosc)
	return hasImage && !hasCurrentBuildAnnotation(mosc)
}

// isPreBuiltImageAwaitingSeeding returns true if the MachineOSConfig has a pre-built
// image annotation but hasn't been seeded yet.
func isPreBuiltImageAwaitingSeeding(mosc *mcfgv1.MachineOSConfig) bool {
	_, hasImage := getPreBuiltImage(mosc)
	return hasImage && !hasCurrentBuildAnnotation(mosc)
}

// needsPreBuiltImageAnnotationCleanup returns true if seeding is complete and the
// pre-built image annotation can be safely removed.
func needsPreBuiltImageAnnotationCleanup(mosc *mcfgv1.MachineOSConfig) bool {
	_, hasImage := getPreBuiltImage(mosc)
	return hasCurrentBuildAnnotation(mosc) &&
		mosc.Status.CurrentImagePullSpec != "" &&
		hasImage
}

// Internal helpers for status-level queries (work on MachineOSBuildStatus
// directly rather than a full MachineOSBuild, for use in isMOSBStatusUpdateNeeded).

var (
	transientConditions = []mcfgv1.BuildProgress{
		mcfgv1.MachineOSBuildPrepared,
		mcfgv1.MachineOSBuilding,
	}
	terminalConditions = []mcfgv1.BuildProgress{
		mcfgv1.MachineOSBuildSucceeded,
		mcfgv1.MachineOSBuildFailed,
		mcfgv1.MachineOSBuildInterrupted,
	}
	allBuildProgressConditions = append(transientConditions, terminalConditions...)
)

func hasAnyBuildCondition(status mcfgv1.MachineOSBuildStatus) bool {
	for _, bp := range allBuildProgressConditions {
		if apihelpers.GetMachineOSBuildCondition(status, bp) != nil {
			return true
		}
	}
	return false
}

func isStatusInitial(status mcfgv1.MachineOSBuildStatus) bool {
	for _, bp := range allBuildProgressConditions {
		if apihelpers.IsMachineOSBuildConditionTrue(status.Conditions, bp) {
			return false
		}
	}
	return true
}

func isStatusTransient(status mcfgv1.MachineOSBuildStatus) bool {
	for _, bp := range transientConditions {
		if apihelpers.IsMachineOSBuildConditionTrue(status.Conditions, bp) {
			return true
		}
	}
	return false
}

func isStatusTerminal(status mcfgv1.MachineOSBuildStatus) bool {
	for _, bp := range terminalConditions {
		if apihelpers.IsMachineOSBuildConditionTrue(status.Conditions, bp) {
			return true
		}
	}
	return false
}

func getStatusTransientState(status mcfgv1.MachineOSBuildStatus) mcfgv1.BuildProgress {
	for _, bp := range transientConditions {
		if apihelpers.IsMachineOSBuildConditionTrue(status.Conditions, bp) {
			return bp
		}
	}
	return ""
}

func getStatusTerminalState(status mcfgv1.MachineOSBuildStatus) mcfgv1.BuildProgress {
	for _, bp := range terminalConditions {
		if apihelpers.IsMachineOSBuildConditionTrue(status.Conditions, bp) {
			return bp
		}
	}
	return ""
}
