package v2

import (
	"context"
	"testing"

	mcfgv1 "github.com/openshift/api/machineconfiguration/v1"
	fakeclientmachineconfigv1 "github.com/openshift/client-go/machineconfiguration/clientset/versioned/fake"
	mcfglistersv1 "github.com/openshift/client-go/machineconfiguration/listers/machineconfiguration/v1"
	"github.com/openshift/machine-config-operator/pkg/apihelpers"
	"github.com/openshift/machine-config-operator/pkg/controller/build/constants"
	ctrlcommon "github.com/openshift/machine-config-operator/pkg/controller/common"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	fakecorev1client "k8s.io/client-go/kubernetes/fake"
	"k8s.io/client-go/tools/cache"
)

const testImageSpec = "registry.example.com/org/repo@sha256:abc123def456"

// --- Test helpers ---

func newTestMOSCForSeeding(name, poolName string) *mcfgv1.MachineOSConfig {
	return &mcfgv1.MachineOSConfig{
		ObjectMeta: metav1.ObjectMeta{
			Name: name,
			Annotations: map[string]string{
				constants.PreBuiltImageAnnotationKey: testImageSpec,
			},
		},
		Spec: mcfgv1.MachineOSConfigSpec{
			MachineConfigPool: mcfgv1.MachineConfigPoolReference{Name: poolName},
			RenderedImagePushSpec: mcfgv1.ImageTagFormat("registry.example.com/org/repo:latest"),
			RenderedImagePushSecret: mcfgv1.ImageSecretObjectReference{
				Name: "push-secret",
			},
		},
	}
}

func newTestMCForSeeding(name string) *mcfgv1.MachineConfig {
	return &mcfgv1.MachineConfig{
		ObjectMeta: metav1.ObjectMeta{
			Name: name,
			Annotations: map[string]string{
				ctrlcommon.ReleaseImageVersionAnnotationKey:            "4.19.0-test",
				ctrlcommon.GeneratedByControllerVersionAnnotationKey:   "4.19.0-test",
			},
		},
		Spec: mcfgv1.MachineConfigSpec{
			OSImageURL: "registry.example.com/os-image:latest",
		},
	}
}

func newTestMCPForSeeding(name, renderedConfig string) *mcfgv1.MachineConfigPool {
	return &mcfgv1.MachineConfigPool{
		ObjectMeta: metav1.ObjectMeta{Name: name},
		Spec: mcfgv1.MachineConfigPoolSpec{
			Configuration: mcfgv1.MachineConfigPoolStatusConfiguration{
				ObjectReference: corev1.ObjectReference{Name: renderedConfig},
			},
		},
	}
}

func newTestSeedManager(t *testing.T, mosc *mcfgv1.MachineOSConfig, mcp *mcfgv1.MachineConfigPool, mc *mcfgv1.MachineConfig, createSecret bool) (*SeedManager, *fakeclientmachineconfigv1.Clientset, *fakecorev1client.Clientset) {
	t.Helper()

	mcfgclient := fakeclientmachineconfigv1.NewSimpleClientset()
	if mosc != nil {
		_, err := mcfgclient.MachineconfigurationV1().MachineOSConfigs().Create(context.Background(), mosc, metav1.CreateOptions{})
		require.NoError(t, err)
	}
	if mcp != nil {
		_, err := mcfgclient.MachineconfigurationV1().MachineConfigPools().Create(context.Background(), mcp, metav1.CreateOptions{})
		require.NoError(t, err)
	}
	if mc != nil {
		_, err := mcfgclient.MachineconfigurationV1().MachineConfigs().Create(context.Background(), mc, metav1.CreateOptions{})
		require.NoError(t, err)
	}

	kubeclient := fakecorev1client.NewSimpleClientset()
	if createSecret {
		secret := &corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "push-secret",
				Namespace: ctrlcommon.MCONamespace,
			},
			Type: corev1.SecretTypeDockerConfigJson,
			Data: map[string][]byte{
				corev1.DockerConfigJsonKey: []byte(`{"auths":{"registry.example.com":{"auth":"dGVzdDp0ZXN0"}}}`),
			},
		}
		_, err := kubeclient.CoreV1().Secrets(ctrlcommon.MCONamespace).Create(context.Background(), secret, metav1.CreateOptions{})
		require.NoError(t, err)
	}

	mcpIndexer := cache.NewIndexer(cache.MetaNamespaceKeyFunc, cache.Indexers{})
	mcIndexer := cache.NewIndexer(cache.MetaNamespaceKeyFunc, cache.Indexers{})
	moscIndexer := cache.NewIndexer(cache.MetaNamespaceKeyFunc, cache.Indexers{})
	mosbIndexer := cache.NewIndexer(cache.MetaNamespaceKeyFunc, cache.Indexers{})

	if mcp != nil {
		mcpIndexer.Add(mcp)
	}
	if mc != nil {
		mcIndexer.Add(mc)
	}
	if mosc != nil {
		moscIndexer.Add(mosc)
	}

	l := &listers{
		machineConfigPoolLister: mcfglistersv1.NewMachineConfigPoolLister(mcpIndexer),
		machineConfigLister:     mcfglistersv1.NewMachineConfigLister(mcIndexer),
		machineOSConfigLister:   mcfglistersv1.NewMachineOSConfigLister(moscIndexer),
		machineOSBuildLister:    mcfglistersv1.NewMachineOSBuildLister(mosbIndexer),
	}

	return NewSeedManager(mcfgclient, kubeclient, l), mcfgclient, kubeclient
}

// --- ShouldSeed / SeedIfNeeded tests ---

func TestSeedIfNeeded_NoAnnotation_NotHandled(t *testing.T) {
	t.Parallel()

	mosc := &mcfgv1.MachineOSConfig{
		ObjectMeta: metav1.ObjectMeta{Name: "worker-os-config"},
		Spec: mcfgv1.MachineOSConfigSpec{
			MachineConfigPool: mcfgv1.MachineConfigPoolReference{Name: "worker"},
		},
	}

	mgr, _, _ := newTestSeedManager(t, mosc, nil, nil, false)

	handled, err := mgr.SeedIfNeeded(context.Background(), mosc)
	require.NoError(t, err)
	assert.False(t, handled)
}

func TestSeedIfNeeded_HasAnnotation_HasCurrentBuild_NotHandled(t *testing.T) {
	t.Parallel()

	mosc := &mcfgv1.MachineOSConfig{
		ObjectMeta: metav1.ObjectMeta{
			Name: "worker-os-config",
			Annotations: map[string]string{
				constants.PreBuiltImageAnnotationKey:         testImageSpec,
				constants.CurrentMachineOSBuildAnnotationKey: "existing-build",
			},
		},
		Spec: mcfgv1.MachineOSConfigSpec{
			MachineConfigPool: mcfgv1.MachineConfigPoolReference{Name: "worker"},
		},
	}

	mgr, _, _ := newTestSeedManager(t, mosc, nil, nil, false)

	handled, err := mgr.SeedIfNeeded(context.Background(), mosc)
	require.NoError(t, err)
	assert.False(t, handled, "should not seed when currentBuild annotation already exists")
}

func TestSeedIfNeeded_HasAnnotation_NoCurrentBuild_Seeds(t *testing.T) {
	t.Parallel()

	mosc := newTestMOSCForSeeding("worker-os-config", "worker")
	mcp := newTestMCPForSeeding("worker", "rendered-worker-1")
	mc := newTestMCForSeeding("rendered-worker-1")

	mgr, mcfgclient, _ := newTestSeedManager(t, mosc, mcp, mc, true)

	handled, err := mgr.SeedIfNeeded(context.Background(), mosc)
	require.NoError(t, err)
	assert.True(t, handled)

	// Verify the MOSC was updated with a current build annotation.
	updatedMOSC, err := mcfgclient.MachineconfigurationV1().MachineOSConfigs().Get(context.Background(), "worker-os-config", metav1.GetOptions{})
	require.NoError(t, err)
	assert.NotEmpty(t, updatedMOSC.Annotations[constants.CurrentMachineOSBuildAnnotationKey])
}

func TestSeedIfNeeded_CreatesSyntheticMOSB(t *testing.T) {
	t.Parallel()

	mosc := newTestMOSCForSeeding("worker-os-config", "worker")
	mcp := newTestMCPForSeeding("worker", "rendered-worker-1")
	mc := newTestMCForSeeding("rendered-worker-1")

	mgr, mcfgclient, _ := newTestSeedManager(t, mosc, mcp, mc, true)

	handled, err := mgr.SeedIfNeeded(context.Background(), mosc)
	require.NoError(t, err)
	require.True(t, handled)

	// Find the created MOSB.
	mosbList, err := mcfgclient.MachineconfigurationV1().MachineOSBuilds().List(context.Background(), metav1.ListOptions{})
	require.NoError(t, err)
	require.Len(t, mosbList.Items, 1, "expected exactly one MachineOSBuild to be created")

	mosb := mosbList.Items[0]
	// The MOSB should have Succeeded=True.
	assert.True(t, apihelpers.IsMachineOSBuildConditionTrue(mosb.Status.Conditions, mcfgv1.MachineOSBuildSucceeded))
	// Should have the digested image pullspec.
	assert.Equal(t, mcfgv1.ImageDigestFormat(testImageSpec), mosb.Status.DigestedImagePushSpec)
}

func TestSeedIfNeeded_SyntheticMOSBHasPreBuiltImageLabel(t *testing.T) {
	t.Parallel()

	mosc := newTestMOSCForSeeding("worker-os-config", "worker")
	mcp := newTestMCPForSeeding("worker", "rendered-worker-1")
	mc := newTestMCForSeeding("rendered-worker-1")

	mgr, mcfgclient, _ := newTestSeedManager(t, mosc, mcp, mc, true)

	handled, err := mgr.SeedIfNeeded(context.Background(), mosc)
	require.NoError(t, err)
	require.True(t, handled)

	mosbList, err := mcfgclient.MachineconfigurationV1().MachineOSBuilds().List(context.Background(), metav1.ListOptions{})
	require.NoError(t, err)
	require.Len(t, mosbList.Items, 1)

	mosb := mosbList.Items[0]
	assert.Equal(t, constants.TrueValue, mosb.Labels[constants.PreBuiltImageLabelKey],
		"synthetic MOSB should have pre-built image label")
}

func TestSeedIfNeeded_UpdatesMOSCStatusWithPullspec(t *testing.T) {
	t.Parallel()

	mosc := newTestMOSCForSeeding("worker-os-config", "worker")
	mcp := newTestMCPForSeeding("worker", "rendered-worker-1")
	mc := newTestMCForSeeding("rendered-worker-1")

	mgr, mcfgclient, _ := newTestSeedManager(t, mosc, mcp, mc, true)

	_, err := mgr.SeedIfNeeded(context.Background(), mosc)
	require.NoError(t, err)

	updatedMOSC, err := mcfgclient.MachineconfigurationV1().MachineOSConfigs().Get(context.Background(), "worker-os-config", metav1.GetOptions{})
	require.NoError(t, err)
	assert.Equal(t, mcfgv1.ImageDigestFormat(testImageSpec), updatedMOSC.Status.CurrentImagePullSpec,
		"MOSC status should have the pre-built image pullspec")
}

func TestSeedIfNeeded_SetsMOSCCurrentBuildAnnotation(t *testing.T) {
	t.Parallel()

	mosc := newTestMOSCForSeeding("worker-os-config", "worker")
	mcp := newTestMCPForSeeding("worker", "rendered-worker-1")
	mc := newTestMCForSeeding("rendered-worker-1")

	mgr, mcfgclient, _ := newTestSeedManager(t, mosc, mcp, mc, true)

	_, err := mgr.SeedIfNeeded(context.Background(), mosc)
	require.NoError(t, err)

	updatedMOSC, err := mcfgclient.MachineconfigurationV1().MachineOSConfigs().Get(context.Background(), "worker-os-config", metav1.GetOptions{})
	require.NoError(t, err)

	currentBuild := updatedMOSC.Annotations[constants.CurrentMachineOSBuildAnnotationKey]
	assert.NotEmpty(t, currentBuild, "current build annotation should be set after seeding")

	// The annotation value should match the created MOSB name.
	mosbList, err := mcfgclient.MachineconfigurationV1().MachineOSBuilds().List(context.Background(), metav1.ListOptions{})
	require.NoError(t, err)
	require.Len(t, mosbList.Items, 1)
	assert.Equal(t, mosbList.Items[0].Name, currentBuild)
}

func TestSeedIfNeeded_MissingSecret_ReturnsError(t *testing.T) {
	t.Parallel()

	mosc := newTestMOSCForSeeding("worker-os-config", "worker")
	mcp := newTestMCPForSeeding("worker", "rendered-worker-1")
	mc := newTestMCForSeeding("rendered-worker-1")

	// Don't create the secret.
	mgr, _, _ := newTestSeedManager(t, mosc, mcp, mc, false)

	handled, err := mgr.SeedIfNeeded(context.Background(), mosc)
	assert.True(t, handled)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "push secret")
}

// --- NeedsCleanup tests ---

func TestNeedsCleanup_SeedingComplete_ReturnsTrue(t *testing.T) {
	t.Parallel()

	mosc := &mcfgv1.MachineOSConfig{
		ObjectMeta: metav1.ObjectMeta{
			Name: "worker-os-config",
			Annotations: map[string]string{
				constants.PreBuiltImageAnnotationKey:         testImageSpec,
				constants.CurrentMachineOSBuildAnnotationKey: "build-1",
			},
		},
		Status: mcfgv1.MachineOSConfigStatus{
			CurrentImagePullSpec: mcfgv1.ImageDigestFormat(testImageSpec),
		},
	}

	mgr, _, _ := newTestSeedManager(t, mosc, nil, nil, false)
	assert.True(t, mgr.NeedsCleanup(mosc))
}

func TestNeedsCleanup_SeedingIncomplete_ReturnsFalse(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		mosc *mcfgv1.MachineOSConfig
	}{
		{
			name: "no current build annotation",
			mosc: &mcfgv1.MachineOSConfig{
				ObjectMeta: metav1.ObjectMeta{
					Annotations: map[string]string{
						constants.PreBuiltImageAnnotationKey: testImageSpec,
					},
				},
			},
		},
		{
			name: "no status pullspec",
			mosc: &mcfgv1.MachineOSConfig{
				ObjectMeta: metav1.ObjectMeta{
					Annotations: map[string]string{
						constants.PreBuiltImageAnnotationKey:         testImageSpec,
						constants.CurrentMachineOSBuildAnnotationKey: "build-1",
					},
				},
			},
		},
		{
			name: "no pre-built image annotation",
			mosc: &mcfgv1.MachineOSConfig{
				ObjectMeta: metav1.ObjectMeta{
					Annotations: map[string]string{
						constants.CurrentMachineOSBuildAnnotationKey: "build-1",
					},
				},
				Status: mcfgv1.MachineOSConfigStatus{
					CurrentImagePullSpec: mcfgv1.ImageDigestFormat(testImageSpec),
				},
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			mgr, _, _ := newTestSeedManager(t, tt.mosc, nil, nil, false)
			assert.False(t, mgr.NeedsCleanup(tt.mosc))
		})
	}
}

// --- Cleanup tests ---

func TestCleanup_RemovesPreBuiltImageAnnotation(t *testing.T) {
	t.Parallel()

	mosc := &mcfgv1.MachineOSConfig{
		ObjectMeta: metav1.ObjectMeta{
			Name: "worker-os-config",
			Annotations: map[string]string{
				constants.PreBuiltImageAnnotationKey:         testImageSpec,
				constants.CurrentMachineOSBuildAnnotationKey: "build-1",
			},
		},
		Status: mcfgv1.MachineOSConfigStatus{
			CurrentImagePullSpec: mcfgv1.ImageDigestFormat(testImageSpec),
		},
	}

	mgr, mcfgclient, _ := newTestSeedManager(t, mosc, nil, nil, false)

	err := mgr.Cleanup(context.Background(), mosc)
	require.NoError(t, err)

	updated, err := mcfgclient.MachineconfigurationV1().MachineOSConfigs().Get(context.Background(), "worker-os-config", metav1.GetOptions{})
	require.NoError(t, err)

	_, hasPreBuiltAnnotation := updated.Annotations[constants.PreBuiltImageAnnotationKey]
	assert.False(t, hasPreBuiltAnnotation, "pre-built image annotation should be removed")

	// The current build annotation should still be present.
	assert.Equal(t, "build-1", updated.Annotations[constants.CurrentMachineOSBuildAnnotationKey])
}

// --- ShouldSeed tests ---

func TestShouldSeed_WithAnnotation_NoCurrentBuild_ReturnsTrue(t *testing.T) {
	t.Parallel()

	mosc := &mcfgv1.MachineOSConfig{
		ObjectMeta: metav1.ObjectMeta{
			Annotations: map[string]string{
				constants.PreBuiltImageAnnotationKey: testImageSpec,
			},
		},
	}

	mgr, _, _ := newTestSeedManager(t, mosc, nil, nil, false)
	assert.True(t, mgr.ShouldSeed(mosc))
}

func TestShouldSeed_WithAnnotation_HasCurrentBuild_ReturnsFalse(t *testing.T) {
	t.Parallel()

	mosc := &mcfgv1.MachineOSConfig{
		ObjectMeta: metav1.ObjectMeta{
			Annotations: map[string]string{
				constants.PreBuiltImageAnnotationKey:         testImageSpec,
				constants.CurrentMachineOSBuildAnnotationKey: "build-1",
			},
		},
	}

	mgr, _, _ := newTestSeedManager(t, mosc, nil, nil, false)
	assert.False(t, mgr.ShouldSeed(mosc))
}

func TestShouldSeed_NoAnnotation_ReturnsFalse(t *testing.T) {
	t.Parallel()

	mosc := &mcfgv1.MachineOSConfig{
		ObjectMeta: metav1.ObjectMeta{Name: "worker-os-config"},
	}

	mgr, _, _ := newTestSeedManager(t, mosc, nil, nil, false)
	assert.False(t, mgr.ShouldSeed(mosc))
}
