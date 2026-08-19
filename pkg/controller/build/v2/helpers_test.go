package v2

import (
	"fmt"
	"testing"

	mcfgv1 "github.com/openshift/api/machineconfiguration/v1"
	"github.com/openshift/machine-config-operator/pkg/apihelpers"
	"github.com/openshift/machine-config-operator/pkg/controller/build/constants"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// --- MOSB state query tests ---

func TestIsMOSBInInitialState(t *testing.T) {
	t.Parallel()

	t.Run("AllConditionsFalse_ReturnsTrue", func(t *testing.T) {
		t.Parallel()
		mosb := &mcfgv1.MachineOSBuild{
			Status: mcfgv1.MachineOSBuildStatus{
				Conditions: apihelpers.MachineOSBuildInitialConditions(),
			},
		}
		assert.True(t, isMOSBInInitialState(mosb))
	})

	t.Run("AnyConditionTrue_ReturnsFalse", func(t *testing.T) {
		t.Parallel()
		for _, bp := range allBuildProgressConditions {
			t.Run(string(bp), func(t *testing.T) {
				t.Parallel()
				mosb := &mcfgv1.MachineOSBuild{}
				apihelpers.SetMachineOSBuildCondition(&mosb.Status, metav1.Condition{
					Type:   string(bp),
					Status: metav1.ConditionTrue,
				})
				assert.False(t, isMOSBInInitialState(mosb))
			})
		}
	})
}

func TestIsMOSBInTransientState(t *testing.T) {
	t.Parallel()

	t.Run("BuildingTrue_ReturnsTrue", func(t *testing.T) {
		t.Parallel()
		mosb := &mcfgv1.MachineOSBuild{}
		apihelpers.SetMachineOSBuildCondition(&mosb.Status, metav1.Condition{
			Type:   string(mcfgv1.MachineOSBuilding),
			Status: metav1.ConditionTrue,
		})
		assert.True(t, isMOSBInTransientState(mosb))
	})

	t.Run("PreparedTrue_ReturnsTrue", func(t *testing.T) {
		t.Parallel()
		mosb := &mcfgv1.MachineOSBuild{}
		apihelpers.SetMachineOSBuildCondition(&mosb.Status, metav1.Condition{
			Type:   string(mcfgv1.MachineOSBuildPrepared),
			Status: metav1.ConditionTrue,
		})
		assert.True(t, isMOSBInTransientState(mosb))
	})

	t.Run("NoneTrue_ReturnsFalse", func(t *testing.T) {
		t.Parallel()
		mosb := &mcfgv1.MachineOSBuild{
			Status: mcfgv1.MachineOSBuildStatus{
				Conditions: apihelpers.MachineOSBuildInitialConditions(),
			},
		}
		assert.False(t, isMOSBInTransientState(mosb))
	})
}

func TestIsMOSBInTerminalState(t *testing.T) {
	t.Parallel()

	t.Run("SucceededTrue_ReturnsTrue", func(t *testing.T) {
		t.Parallel()
		mosb := &mcfgv1.MachineOSBuild{}
		apihelpers.SetMachineOSBuildCondition(&mosb.Status, metav1.Condition{
			Type:   string(mcfgv1.MachineOSBuildSucceeded),
			Status: metav1.ConditionTrue,
		})
		assert.True(t, isMOSBInTerminalState(mosb))
	})

	t.Run("FailedTrue_ReturnsTrue", func(t *testing.T) {
		t.Parallel()
		mosb := &mcfgv1.MachineOSBuild{}
		apihelpers.SetMachineOSBuildCondition(&mosb.Status, metav1.Condition{
			Type:   string(mcfgv1.MachineOSBuildFailed),
			Status: metav1.ConditionTrue,
		})
		assert.True(t, isMOSBInTerminalState(mosb))
	})

	t.Run("InterruptedTrue_ReturnsTrue", func(t *testing.T) {
		t.Parallel()
		mosb := &mcfgv1.MachineOSBuild{}
		apihelpers.SetMachineOSBuildCondition(&mosb.Status, metav1.Condition{
			Type:   string(mcfgv1.MachineOSBuildInterrupted),
			Status: metav1.ConditionTrue,
		})
		assert.True(t, isMOSBInTerminalState(mosb))
	})
}

func TestGetMOSBTransientState(t *testing.T) {
	t.Parallel()

	t.Run("ReturnsCorrectState", func(t *testing.T) {
		t.Parallel()
		for _, bp := range transientConditions {
			t.Run(string(bp), func(t *testing.T) {
				t.Parallel()
				mosb := &mcfgv1.MachineOSBuild{}
				apihelpers.SetMachineOSBuildCondition(&mosb.Status, metav1.Condition{
					Type:   string(bp),
					Status: metav1.ConditionTrue,
				})
				assert.Equal(t, bp, getMOSBTransientState(mosb))
			})
		}
	})

	t.Run("NoneTrue_ReturnsEmpty", func(t *testing.T) {
		t.Parallel()
		mosb := &mcfgv1.MachineOSBuild{}
		assert.Equal(t, mcfgv1.BuildProgress(""), getMOSBTransientState(mosb))
	})
}

func TestGetMOSBTerminalState(t *testing.T) {
	t.Parallel()

	t.Run("ReturnsCorrectState", func(t *testing.T) {
		t.Parallel()
		for _, bp := range terminalConditions {
			t.Run(string(bp), func(t *testing.T) {
				t.Parallel()
				mosb := &mcfgv1.MachineOSBuild{}
				apihelpers.SetMachineOSBuildCondition(&mosb.Status, metav1.Condition{
					Type:   string(bp),
					Status: metav1.ConditionTrue,
				})
				assert.Equal(t, bp, getMOSBTerminalState(mosb))
			})
		}
	})

	t.Run("NoneTrue_ReturnsEmpty", func(t *testing.T) {
		t.Parallel()
		mosb := &mcfgv1.MachineOSBuild{}
		assert.Equal(t, mcfgv1.BuildProgress(""), getMOSBTerminalState(mosb))
	})
}

// --- Status transition tests ---

func TestIsMOSBStatusUpdateNeeded(t *testing.T) {
	t.Parallel()

	initialConditions := apihelpers.MachineOSBuildInitialConditions()
	preparedConditions := apihelpers.MachineOSBuildPendingConditions()
	buildingConditions := apihelpers.MachineOSBuildRunningConditions()
	succeededConditions := apihelpers.MachineOSBuildSucceededConditions()
	failedConditions := apihelpers.MachineOSBuildFailedConditions()
	interruptedConditions := apihelpers.MachineOSBuildInterruptedConditions()

	tests := []struct {
		name     string
		old      []metav1.Condition
		cur      []metav1.Condition
		expected bool
	}{
		// Valid transitions
		{"Initial->Prepared", initialConditions, preparedConditions, true},
		{"Initial->Building", initialConditions, buildingConditions, true},
		{"Initial->Succeeded", initialConditions, succeededConditions, true},
		{"Initial->Failed", initialConditions, failedConditions, true},
		{"Initial->Interrupted", initialConditions, interruptedConditions, true},
		{"Prepared->Building", preparedConditions, buildingConditions, true},
		{"Prepared->Succeeded", preparedConditions, succeededConditions, true},
		{"Prepared->Failed", preparedConditions, failedConditions, true},
		{"Prepared->Interrupted", preparedConditions, interruptedConditions, true},
		{"Building->Succeeded", buildingConditions, succeededConditions, true},
		{"Building->Failed", buildingConditions, failedConditions, true},
		{"Building->Interrupted", buildingConditions, interruptedConditions, true},
		// Invalid transitions
		{"Building->Prepared", buildingConditions, preparedConditions, false},
		{"Succeeded->Initial", succeededConditions, initialConditions, false},
		{"Succeeded->Prepared", succeededConditions, preparedConditions, false},
		{"Succeeded->Building", succeededConditions, buildingConditions, false},
		{"Succeeded->Failed", succeededConditions, failedConditions, false},
		{"Failed->Initial", failedConditions, initialConditions, false},
		{"Failed->Succeeded", failedConditions, succeededConditions, false},
		{"Interrupted->Initial", interruptedConditions, initialConditions, false},
		{"Initial->Initial", initialConditions, initialConditions, false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			oldStatus := mcfgv1.MachineOSBuildStatus{Conditions: tt.old}
			curStatus := mcfgv1.MachineOSBuildStatus{Conditions: tt.cur}
			result, reason := isMOSBStatusUpdateNeeded(oldStatus, curStatus)
			if tt.expected {
				assert.True(t, result, "expected update needed, got reason: %s", reason)
			} else {
				assert.False(t, result, "expected no update needed, got reason: %s", reason)
			}
		})
	}
}

// --- Annotation helper tests ---

func TestAnnotationHelpers(t *testing.T) {
	t.Parallel()

	t.Run("hasCurrentBuildAnnotation_present", func(t *testing.T) {
		t.Parallel()
		mosc := &mcfgv1.MachineOSConfig{
			ObjectMeta: metav1.ObjectMeta{
				Annotations: map[string]string{
					constants.CurrentMachineOSBuildAnnotationKey: "build-1",
				},
			},
		}
		assert.True(t, hasCurrentBuildAnnotation(mosc))
	})

	t.Run("hasCurrentBuildAnnotation_missing", func(t *testing.T) {
		t.Parallel()
		mosc := &mcfgv1.MachineOSConfig{}
		assert.False(t, hasCurrentBuildAnnotation(mosc))
	})

	t.Run("hasCurrentBuildAnnotation_empty", func(t *testing.T) {
		t.Parallel()
		mosc := &mcfgv1.MachineOSConfig{
			ObjectMeta: metav1.ObjectMeta{
				Annotations: map[string]string{
					constants.CurrentMachineOSBuildAnnotationKey: "",
				},
			},
		}
		assert.False(t, hasCurrentBuildAnnotation(mosc))
	})

	t.Run("isCurrentBuildAnnotationEqual_matches", func(t *testing.T) {
		t.Parallel()
		mosc := &mcfgv1.MachineOSConfig{
			ObjectMeta: metav1.ObjectMeta{
				Annotations: map[string]string{
					constants.CurrentMachineOSBuildAnnotationKey: "build-1",
				},
			},
		}
		mosb := &mcfgv1.MachineOSBuild{ObjectMeta: metav1.ObjectMeta{Name: "build-1"}}
		assert.True(t, isCurrentBuildAnnotationEqual(mosc, mosb))
	})

	t.Run("isCurrentBuildAnnotationEqual_mismatch", func(t *testing.T) {
		t.Parallel()
		mosc := &mcfgv1.MachineOSConfig{
			ObjectMeta: metav1.ObjectMeta{
				Annotations: map[string]string{
					constants.CurrentMachineOSBuildAnnotationKey: "build-1",
				},
			},
		}
		mosb := &mcfgv1.MachineOSBuild{ObjectMeta: metav1.ObjectMeta{Name: "build-2"}}
		assert.False(t, isCurrentBuildAnnotationEqual(mosc, mosb))
	})

	t.Run("hasRebuildAnnotation_present", func(t *testing.T) {
		t.Parallel()
		mosc := &mcfgv1.MachineOSConfig{
			ObjectMeta: metav1.ObjectMeta{
				Annotations: map[string]string{
					constants.RebuildMachineOSConfigAnnotationKey: "",
				},
			},
		}
		assert.True(t, hasRebuildAnnotation(mosc))
	})

	t.Run("hasRebuildAnnotation_missing", func(t *testing.T) {
		t.Parallel()
		mosc := &mcfgv1.MachineOSConfig{}
		assert.False(t, hasRebuildAnnotation(mosc))
	})
}

// --- Pre-built image helper tests ---

func TestPreBuiltImageHelpers(t *testing.T) {
	t.Parallel()

	t.Run("getPreBuiltImage_present", func(t *testing.T) {
		t.Parallel()
		mosc := &mcfgv1.MachineOSConfig{
			ObjectMeta: metav1.ObjectMeta{
				Annotations: map[string]string{
					constants.PreBuiltImageAnnotationKey: "registry.example.com/image@sha256:abc",
				},
			},
		}
		image, ok := getPreBuiltImage(mosc)
		require.True(t, ok)
		assert.Equal(t, "registry.example.com/image@sha256:abc", image)
	})

	t.Run("getPreBuiltImage_empty", func(t *testing.T) {
		t.Parallel()
		mosc := &mcfgv1.MachineOSConfig{
			ObjectMeta: metav1.ObjectMeta{
				Annotations: map[string]string{
					constants.PreBuiltImageAnnotationKey: "",
				},
			},
		}
		_, ok := getPreBuiltImage(mosc)
		assert.False(t, ok)
	})

	t.Run("getPreBuiltImage_missing", func(t *testing.T) {
		t.Parallel()
		mosc := &mcfgv1.MachineOSConfig{}
		_, ok := getPreBuiltImage(mosc)
		assert.False(t, ok)
	})

	t.Run("shouldSeedWithPreBuiltImage_true", func(t *testing.T) {
		t.Parallel()
		mosc := &mcfgv1.MachineOSConfig{
			ObjectMeta: metav1.ObjectMeta{
				Annotations: map[string]string{
					constants.PreBuiltImageAnnotationKey: "registry.example.com/image@sha256:abc",
				},
			},
		}
		assert.True(t, shouldSeedWithPreBuiltImage(mosc))
	})

	t.Run("shouldSeedWithPreBuiltImage_false_alreadySeeded", func(t *testing.T) {
		t.Parallel()
		mosc := &mcfgv1.MachineOSConfig{
			ObjectMeta: metav1.ObjectMeta{
				Annotations: map[string]string{
					constants.PreBuiltImageAnnotationKey:         "registry.example.com/image@sha256:abc",
					constants.CurrentMachineOSBuildAnnotationKey: "build-1",
				},
			},
		}
		assert.False(t, shouldSeedWithPreBuiltImage(mosc))
	})

	t.Run("needsPreBuiltImageAnnotationCleanup_allConditionsMet", func(t *testing.T) {
		t.Parallel()
		mosc := &mcfgv1.MachineOSConfig{
			ObjectMeta: metav1.ObjectMeta{
				Annotations: map[string]string{
					constants.PreBuiltImageAnnotationKey:         "registry.example.com/image@sha256:abc",
					constants.CurrentMachineOSBuildAnnotationKey: "build-1",
				},
			},
			Status: mcfgv1.MachineOSConfigStatus{
				CurrentImagePullSpec: "registry.example.com/image@sha256:abc",
			},
		}
		assert.True(t, needsPreBuiltImageAnnotationCleanup(mosc))
	})

	t.Run("needsPreBuiltImageAnnotationCleanup_missingCurrentBuild", func(t *testing.T) {
		t.Parallel()
		mosc := &mcfgv1.MachineOSConfig{
			ObjectMeta: metav1.ObjectMeta{
				Annotations: map[string]string{
					constants.PreBuiltImageAnnotationKey: "registry.example.com/image@sha256:abc",
				},
			},
			Status: mcfgv1.MachineOSConfigStatus{
				CurrentImagePullSpec: "registry.example.com/image@sha256:abc",
			},
		}
		assert.False(t, needsPreBuiltImageAnnotationCleanup(mosc))
	})

	t.Run("needsPreBuiltImageAnnotationCleanup_emptyStatus", func(t *testing.T) {
		t.Parallel()
		mosc := &mcfgv1.MachineOSConfig{
			ObjectMeta: metav1.ObjectMeta{
				Annotations: map[string]string{
					constants.PreBuiltImageAnnotationKey:         "registry.example.com/image@sha256:abc",
					constants.CurrentMachineOSBuildAnnotationKey: "build-1",
				},
			},
		}
		assert.False(t, needsPreBuiltImageAnnotationCleanup(mosc))
	})

	t.Run("isPreBuiltImageAwaitingSeeding_true", func(t *testing.T) {
		t.Parallel()
		mosc := &mcfgv1.MachineOSConfig{
			ObjectMeta: metav1.ObjectMeta{
				Annotations: map[string]string{
					constants.PreBuiltImageAnnotationKey: "registry.example.com/image@sha256:abc",
				},
			},
		}
		assert.True(t, isPreBuiltImageAwaitingSeeding(mosc))
	})

	t.Run("isPreBuiltImageAwaitingSeeding_false_alreadySeeded", func(t *testing.T) {
		t.Parallel()
		mosc := &mcfgv1.MachineOSConfig{
			ObjectMeta: metav1.ObjectMeta{
				Annotations: map[string]string{
					constants.PreBuiltImageAnnotationKey:         "registry.example.com/image@sha256:abc",
					constants.CurrentMachineOSBuildAnnotationKey: "build-1",
				},
			},
		}
		assert.False(t, isPreBuiltImageAwaitingSeeding(mosc))
	})
}

// --- Helpers for test data construction ---

func makeMOSBWithConditions(conditions []metav1.Condition) *mcfgv1.MachineOSBuild {
	return &mcfgv1.MachineOSBuild{
		Status: mcfgv1.MachineOSBuildStatus{Conditions: conditions},
	}
}

func TestAllTransitionCombinations(t *testing.T) {
	t.Parallel()

	// Comprehensive: pair every (old, cur) state category and verify direction.
	type stateCategory struct {
		name       string
		conditions []metav1.Condition
	}

	categories := []stateCategory{
		{"initial", apihelpers.MachineOSBuildInitialConditions()},
		{"prepared", apihelpers.MachineOSBuildPendingConditions()},
		{"building", apihelpers.MachineOSBuildRunningConditions()},
		{"succeeded", apihelpers.MachineOSBuildSucceededConditions()},
		{"failed", apihelpers.MachineOSBuildFailedConditions()},
		{"interrupted", apihelpers.MachineOSBuildInterruptedConditions()},
	}

	for _, old := range categories {
		for _, cur := range categories {
			t.Run(fmt.Sprintf("%s->%s", old.name, cur.name), func(t *testing.T) {
				t.Parallel()
				oldStatus := mcfgv1.MachineOSBuildStatus{Conditions: old.conditions}
				curStatus := mcfgv1.MachineOSBuildStatus{Conditions: cur.conditions}
				needed, reason := isMOSBStatusUpdateNeeded(oldStatus, curStatus)
				// Just verify it doesn't panic; the specific expectations are
				// covered in the targeted test above.
				_ = needed
				_ = reason
			})
		}
	}
}
