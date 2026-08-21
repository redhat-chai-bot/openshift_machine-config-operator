package buildrequest

import (
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"
	"k8s.io/apimachinery/pkg/labels"

	mcfgv1 "github.com/openshift/api/machineconfiguration/v1"
	"github.com/openshift/machine-config-operator/pkg/controller/build/internal/buildlabels"
	"github.com/openshift/machine-config-operator/pkg/controller/build/internal/fixtures"
	testhelpers "github.com/openshift/machine-config-operator/test/helpers"
)

// Tests that a MachineOSBuild can be constructed correctly.
func TestMachineOSBuild(t *testing.T) {
	t.Parallel()

	poolName := "worker"

	getMachineConfig := func() *mcfgv1.MachineConfig {
		return fixtures.NewObjectsForTest(poolName).RenderedMachineConfig
	}

	getMachineOSConfig := func() *mcfgv1.MachineOSConfig {
		return testhelpers.NewMachineOSConfigBuilder(poolName).WithMachineConfigPool(poolName).MachineOSConfig()
	}

	getMachineConfigPool := func() *mcfgv1.MachineConfigPool {
		return testhelpers.NewMachineConfigPoolBuilder(poolName).MachineConfigPool()
	}

	// Some of the test cases expect the hash name to be the same. This is that hash value.
	expectedCommonHashName := "worker-699e6be74658adcb3ff2b48f32cd1584"

	testCases := []struct {
		name         string
		opts         MachineOSBuildOpts
		expectedName string
		errExpected  bool
	}{
		{
			name:        "Missing MachineConfigPool",
			errExpected: true,
			opts: MachineOSBuildOpts{
				MachineConfig:   getMachineConfig(),
				MachineOSConfig: getMachineOSConfig(),
			},
		},
		{
			name:        "Missing MachineOSConfig",
			errExpected: true,
			opts: MachineOSBuildOpts{
				MachineConfig:     getMachineConfig(),
				MachineConfigPool: getMachineConfigPool(),
			},
		},
		{
			name:        "Mismatched MachineConfigPool name and MachineOSConfig",
			errExpected: true,
			opts: MachineOSBuildOpts{
				MachineConfig:     getMachineConfig(),
				MachineOSConfig:   testhelpers.NewMachineOSConfigBuilder("worker").WithMachineConfigPool("other-pool").MachineOSConfig(),
				MachineConfigPool: getMachineConfigPool(),
			},
		},
		{
			name:        "Missing MachineConfig",
			errExpected: true,
			opts: MachineOSBuildOpts{
				MachineOSConfig:   getMachineOSConfig(),
				MachineConfigPool: getMachineConfigPool(),
			},
		},
		// These cases ensure that the hashed name remains stable.
		{
			name:         "All values present",
			expectedName: expectedCommonHashName,
			opts: MachineOSBuildOpts{
				MachineConfig:     getMachineConfig(),
				MachineOSConfig:   getMachineOSConfig(),
				MachineConfigPool: getMachineConfigPool(),
			},
		},
		// These cases ensure that pausing the MachineConfigPool does not affect the hash.
		{
			name:         "Unpaused MachineConfigPool",
			expectedName: expectedCommonHashName,
			opts: MachineOSBuildOpts{
				MachineConfig:     getMachineConfig(),
				MachineOSConfig:   getMachineOSConfig(),
				MachineConfigPool: getMachineConfigPool(),
			},
		},
		{
			name:         "Paused MachineConfigPool",
			expectedName: expectedCommonHashName,
			opts: MachineOSBuildOpts{
				MachineConfig:     getMachineConfig(),
				MachineOSConfig:   getMachineOSConfig(),
				MachineConfigPool: testhelpers.NewMachineConfigPoolBuilder(poolName).WithPaused().MachineConfigPool(),
			},
		},
	}

	for _, testCase := range testCases {
		testCase := testCase
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()

			if testCase.opts.MachineOSConfig != nil {
				testCase.opts.MachineOSConfig.Spec.RenderedImagePushSpec = "registry.hostname.com/org/repo:latest"
			}

			mosb, err := NewMachineOSBuild(testCase.opts)

			if testCase.errExpected {
				assert.Error(t, err)
				return
			} else {
				assert.NoError(t, err)
			}

			assert.Equal(t, testCase.expectedName, mosb.Name)

			expectedPullspec := fmt.Sprintf("registry.hostname.com/org/repo:%s", testCase.expectedName)
			assert.Equal(t, expectedPullspec, string(mosb.Spec.RenderedImagePushSpec))
			assert.Equal(t, testCase.opts.MachineConfigPool.Spec.Configuration.Name, mosb.Spec.MachineConfig.Name)
			assert.NotNil(t, mosb.Status.BuildStart)

			assert.True(t, buildlabels.MachineOSBuildSelector(testCase.opts.MachineOSConfig, testCase.opts.MachineConfigPool).Matches(labels.Set(mosb.Labels)))
			assert.Equal(t, buildlabels.GetMachineOSBuildLabels(testCase.opts.MachineOSConfig, testCase.opts.MachineConfigPool), mosb.Labels)
		})
	}
}

// Ensures that the labels are consistent between NewMachineOSBuild and the
// test fixture given the same input data. This is required because it would
// cause a circular import for the fixtures package to use the
// buildlabels.GetMachineOSBuildLabels() function..
func TestMachineOSBuildLabelConsistency(t *testing.T) {
	t.Parallel()

	poolName := "worker"

	obj := fixtures.NewObjectsForTest(poolName)

	mosb, err := NewMachineOSBuild(MachineOSBuildOpts{
		MachineConfig:     fixtures.NewObjectsForTest(poolName).RenderedMachineConfig,
		MachineConfigPool: obj.MachineConfigPool,
		MachineOSConfig:   obj.MachineOSConfig,
	})

	assert.NoError(t, err)

	assert.True(t, buildlabels.MachineOSBuildSelector(obj.MachineOSConfig, obj.MachineConfigPool).Matches(labels.Set(mosb.Labels)))
	assert.Equal(t, mosb.Labels, buildlabels.GetMachineOSBuildLabels(obj.MachineOSConfig, obj.MachineConfigPool))
	assert.Equal(t, obj.MachineOSBuild.Labels, mosb.Labels)
}

// TestMachineOSBuildHashStability is a regression test that pins the MD5-based
// MOSB name for a known set of inputs. The hash MUST remain byte-identical
// across runs — any drift means the MOSB naming contract has been broken.
//
// Run with: go test -run TestMachineOSBuildHashStability -count=1000
func TestMachineOSBuildHashStability(t *testing.T) {
	t.Parallel()

	poolName := "worker"

	// Use the same constructors as TestMachineOSBuild to ensure we get the
	// same canonical inputs that produced the pinned hash.
	mc := fixtures.NewObjectsForTest(poolName).RenderedMachineConfig
	mosc := testhelpers.NewMachineOSConfigBuilder(poolName).WithMachineConfigPool(poolName).MachineOSConfig()
	mosc.Spec.RenderedImagePushSpec = "registry.hostname.com/org/repo:latest"
	mcp := testhelpers.NewMachineConfigPoolBuilder(poolName).MachineConfigPool()

	opts := MachineOSBuildOpts{
		MachineConfig:     mc,
		MachineOSConfig:   mosc,
		MachineConfigPool: mcp,
	}

	// This is the expected, pinned hash name. If this value ever changes, it
	// means the hash inputs or algorithm have drifted and must be investigated.
	const pinnedName = "worker-699e6be74658adcb3ff2b48f32cd1584"

	mosb, err := NewMachineOSBuild(opts)
	assert.NoError(t, err)
	assert.Equal(t, pinnedName, mosb.Name,
		"MOSB name hash has drifted from the pinned value — this is a breaking change")

	// Verify the hash portion alone is stable.
	hash, err := opts.getHashedName()
	assert.NoError(t, err)
	assert.Equal(t, "699e6be74658adcb3ff2b48f32cd1584", hash,
		"raw MD5 hash has drifted — the salt, input ordering, or serialisation changed")
}
