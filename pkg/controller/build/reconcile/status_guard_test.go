package reconcile

import (
	"testing"

	mcfgv1 "github.com/openshift/api/machineconfiguration/v1"
	"github.com/openshift/machine-config-operator/pkg/apihelpers"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func TestIsMachineOSBuildStatusUpdateNeeded(t *testing.T) {
	tests := []struct {
		name       string
		oldStatus  mcfgv1.MachineOSBuildStatus
		curStatus  mcfgv1.MachineOSBuildStatus
		wantNeeded bool
	}{
		{
			name:      "no conditions → initial state",
			oldStatus: mcfgv1.MachineOSBuildStatus{},
			curStatus: mcfgv1.MachineOSBuildStatus{
				Conditions: apihelpers.MachineOSBuildInitialConditions(),
			},
			wantNeeded: true,
		},
		{
			name: "initial → building (transient)",
			oldStatus: mcfgv1.MachineOSBuildStatus{
				Conditions: apihelpers.MachineOSBuildInitialConditions(),
			},
			curStatus: mcfgv1.MachineOSBuildStatus{
				Conditions: apihelpers.MachineOSBuildRunningConditions(),
			},
			wantNeeded: true,
		},
		{
			name: "building → succeeded (terminal)",
			oldStatus: mcfgv1.MachineOSBuildStatus{
				Conditions: apihelpers.MachineOSBuildRunningConditions(),
			},
			curStatus: mcfgv1.MachineOSBuildStatus{
				Conditions: apihelpers.MachineOSBuildSucceededConditions(),
			},
			wantNeeded: true,
		},
		{
			name: "succeeded → succeeded (terminal → terminal, redundant)",
			oldStatus: mcfgv1.MachineOSBuildStatus{
				Conditions: apihelpers.MachineOSBuildSucceededConditions(),
			},
			curStatus: mcfgv1.MachineOSBuildStatus{
				Conditions: apihelpers.MachineOSBuildSucceededConditions(),
			},
			wantNeeded: false,
		},
		{
			name: "succeeded → failed (terminal → terminal, invalid)",
			oldStatus: mcfgv1.MachineOSBuildStatus{
				Conditions: apihelpers.MachineOSBuildSucceededConditions(),
			},
			curStatus: mcfgv1.MachineOSBuildStatus{
				Conditions: apihelpers.MachineOSBuildFailedConditions(),
			},
			wantNeeded: false,
		},
		{
			name: "succeeded → building (terminal → transient, invalid)",
			oldStatus: mcfgv1.MachineOSBuildStatus{
				Conditions: apihelpers.MachineOSBuildSucceededConditions(),
			},
			curStatus: mcfgv1.MachineOSBuildStatus{
				Conditions: apihelpers.MachineOSBuildRunningConditions(),
			},
			wantNeeded: false,
		},
		{
			name: "building → failed",
			oldStatus: mcfgv1.MachineOSBuildStatus{
				Conditions: apihelpers.MachineOSBuildRunningConditions(),
			},
			curStatus: mcfgv1.MachineOSBuildStatus{
				Conditions: apihelpers.MachineOSBuildFailedConditions(),
			},
			wantNeeded: true,
		},
		{
			name: "initial → failed (direct to terminal)",
			oldStatus: mcfgv1.MachineOSBuildStatus{
				Conditions: apihelpers.MachineOSBuildInitialConditions(),
			},
			curStatus: mcfgv1.MachineOSBuildStatus{
				Conditions: apihelpers.MachineOSBuildFailedConditions(),
			},
			wantNeeded: true,
		},
		{
			name: "prepared → building (valid transient transition)",
			oldStatus: mcfgv1.MachineOSBuildStatus{
				Conditions: apihelpers.MachineOSBuildPendingConditions(),
			},
			curStatus: mcfgv1.MachineOSBuildStatus{
				Conditions: apihelpers.MachineOSBuildRunningConditions(),
			},
			wantNeeded: true,
		},
		{
			name: "building → building (same state, no update)",
			oldStatus: mcfgv1.MachineOSBuildStatus{
				Conditions: []metav1.Condition{
					{Type: string(mcfgv1.MachineOSBuilding), Status: metav1.ConditionTrue},
				},
			},
			curStatus: mcfgv1.MachineOSBuildStatus{
				Conditions: []metav1.Condition{
					{Type: string(mcfgv1.MachineOSBuilding), Status: metav1.ConditionTrue},
				},
			},
			wantNeeded: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			gotNeeded, reason := IsMachineOSBuildStatusUpdateNeeded(tt.oldStatus, tt.curStatus)
			if gotNeeded != tt.wantNeeded {
				t.Errorf("IsMachineOSBuildStatusUpdateNeeded() needed = %v, want %v (reason: %s)",
					gotNeeded, tt.wantNeeded, reason)
			}
		})
	}
}

func TestLogStatusGuardResult(t *testing.T) {
	// Exercise both branches of logStatusGuardResult for coverage.
	logStatusGuardResult("test-mosb", true, "needed: test reason")
	logStatusGuardResult("test-mosb", false, "skipped: test reason")
	logStatusGuardResult("test-mosb", false, "")
}
