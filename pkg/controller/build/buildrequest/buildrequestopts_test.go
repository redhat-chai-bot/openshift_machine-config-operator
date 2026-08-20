package buildrequest

import (
	"context"
	"testing"

	"github.com/openshift/machine-config-operator/pkg/controller/build/constants"
	"github.com/openshift/machine-config-operator/pkg/controller/build/internal/fixtures"
	ctrlcommon "github.com/openshift/machine-config-operator/pkg/controller/common"
	"github.com/stretchr/testify/assert"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/tools/cache"

	mcfgv1 "github.com/openshift/api/machineconfiguration/v1"
	mcfglistersv1 "github.com/openshift/client-go/machineconfiguration/listers/machineconfiguration/v1"
	corelistersv1 "k8s.io/client-go/listers/core/v1"
)

// newTestListers creates Listers backed by in-memory indexers populated
// from the given fake clients' objects.
func newTestListers(
	kubeObjects []runtime.Object,
	mcfgObjects []runtime.Object,
) *Listers {
	secretIndexer := cache.NewIndexer(cache.MetaNamespaceKeyFunc, cache.Indexers{cache.NamespaceIndex: cache.MetaNamespaceIndexFunc})
	cmIndexer := cache.NewIndexer(cache.MetaNamespaceKeyFunc, cache.Indexers{cache.NamespaceIndex: cache.MetaNamespaceIndexFunc})
	mcIndexer := cache.NewIndexer(cache.MetaNamespaceKeyFunc, cache.Indexers{})
	ccIndexer := cache.NewIndexer(cache.MetaNamespaceKeyFunc, cache.Indexers{})

	for _, obj := range kubeObjects {
		switch o := obj.(type) {
		case *corev1.Secret:
			secretIndexer.Add(o)
		case *corev1.ConfigMap:
			cmIndexer.Add(o)
		}
	}
	for _, obj := range mcfgObjects {
		switch o := obj.(type) {
		case *mcfgv1.MachineConfig:
			mcIndexer.Add(o)
		case *mcfgv1.ControllerConfig:
			ccIndexer.Add(o)
		}
	}

	return &Listers{
		SecretLister:           corelistersv1.NewSecretLister(secretIndexer),
		ConfigMapLister:        corelistersv1.NewConfigMapLister(cmIndexer),
		MachineConfigLister:    mcfglistersv1.NewMachineConfigLister(mcIndexer),
		ControllerConfigLister: mcfglistersv1.NewControllerConfigLister(ccIndexer),
	}
}

func TestBuildRequestOpts(t *testing.T) {
	testCases := []struct {
		name            string
		addlObjects     []runtime.Object
		addlObjectSetup func(*testing.T, *fixtures.ObjectsForTest)
		addlAsserts     func(*testing.T, BuildRequestOpts)
	}{
		{
			name: "no entitlement data",
			addlAsserts: func(t *testing.T, brOpts BuildRequestOpts) {
				assert.False(t, brOpts.HasEtcPkiRpmGpgKeys)
				assert.False(t, brOpts.HasEtcYumReposDConfigs)
				assert.False(t, brOpts.HasEtcPkiEntitlementKeys)
				assert.False(t, brOpts.hasUserDefinedBaseImagePullSecret)
			},
		},
		{
			name: "with etc-pki-entitlement data",
			addlObjects: []runtime.Object{
				&corev1.Secret{
					ObjectMeta: metav1.ObjectMeta{
						Name:      constants.EtcPkiEntitlementSecretName + "-" + ctrlcommon.MachineConfigPoolWorker,
						Namespace: ctrlcommon.MCONamespace,
					},
				},
			},
			addlAsserts: func(t *testing.T, brOpts BuildRequestOpts) {
				assert.False(t, brOpts.HasEtcPkiRpmGpgKeys)
				assert.False(t, brOpts.HasEtcYumReposDConfigs)
				assert.True(t, brOpts.HasEtcPkiEntitlementKeys)
				assert.False(t, brOpts.hasUserDefinedBaseImagePullSecret)
			},
		},
		{
			name: "with etc-yum-repos-d data",
			addlObjects: []runtime.Object{
				&corev1.ConfigMap{
					ObjectMeta: metav1.ObjectMeta{
						Name:      constants.EtcYumReposDConfigMapName,
						Namespace: ctrlcommon.MCONamespace,
					},
				},
			},
			addlAsserts: func(t *testing.T, brOpts BuildRequestOpts) {
				assert.False(t, brOpts.HasEtcPkiRpmGpgKeys)
				assert.True(t, brOpts.HasEtcYumReposDConfigs)
				assert.False(t, brOpts.HasEtcPkiEntitlementKeys)
				assert.False(t, brOpts.hasUserDefinedBaseImagePullSecret)
			},
		},
		{
			name: "with etc-pki-rpm-gpg-keys data",
			addlObjects: []runtime.Object{
				&corev1.Secret{
					ObjectMeta: metav1.ObjectMeta{
						Name:      constants.EtcPkiRpmGpgSecretName,
						Namespace: ctrlcommon.MCONamespace,
					},
				},
			},
			addlAsserts: func(t *testing.T, brOpts BuildRequestOpts) {
				assert.True(t, brOpts.HasEtcPkiRpmGpgKeys)
				assert.False(t, brOpts.HasEtcYumReposDConfigs)
				assert.False(t, brOpts.HasEtcPkiEntitlementKeys)
				assert.False(t, brOpts.hasUserDefinedBaseImagePullSecret)
			},
		},
		{
			name: "with all entitlements data",
			addlObjects: []runtime.Object{
				&corev1.ConfigMap{
					ObjectMeta: metav1.ObjectMeta{
						Name:      constants.EtcYumReposDConfigMapName,
						Namespace: ctrlcommon.MCONamespace,
					},
				},
				&corev1.Secret{
					ObjectMeta: metav1.ObjectMeta{
						Name:      constants.EtcPkiRpmGpgSecretName,
						Namespace: ctrlcommon.MCONamespace,
					},
				},
				&corev1.Secret{
					ObjectMeta: metav1.ObjectMeta{
						Name:      constants.EtcPkiEntitlementSecretName + "-" + ctrlcommon.MachineConfigPoolWorker,
						Namespace: ctrlcommon.MCONamespace,
					},
				},
			},
			addlAsserts: func(t *testing.T, brOpts BuildRequestOpts) {
				assert.True(t, brOpts.HasEtcPkiRpmGpgKeys)
				assert.True(t, brOpts.HasEtcYumReposDConfigs)
				assert.True(t, brOpts.HasEtcPkiEntitlementKeys)
				assert.False(t, brOpts.hasUserDefinedBaseImagePullSecret)
			},
		},
		{
			name: "with user defined base image pull secret",
			addlObjectSetup: func(t *testing.T, lobj *fixtures.ObjectsForTest) {
				lobj.MachineOSConfig.Spec.BaseImagePullSecret = &mcfgv1.ImageSecretObjectReference{Name: fixtures.BaseImagePullSecretName}
			},
			addlAsserts: func(t *testing.T, brOpts BuildRequestOpts) {
				assert.True(t, brOpts.hasUserDefinedBaseImagePullSecret)
			},
		},
	}

	// Verify that when BaseImagePullSecret is nil and the fallback global
	// pull secret is also missing, the error message uses the extracted
	// variable name instead of dereferencing the nil pointer.
	t.Run("nil BaseImagePullSecret does not panic on error path", func(t *testing.T) {
		t.Parallel()

		ctx, cancel := context.WithCancel(context.Background())
		t.Cleanup(cancel)

		_, _, lobj, _ := fixtures.GetClientsForTest(t)

		// Ensure BaseImagePullSecret is nil on the MOSC.
		lobj.MachineOSConfig.Spec.BaseImagePullSecret = nil

		// Build listers from the default objects but WITHOUT the global
		// pull secret so that getValidatedSecret fails.
		kubeObjs, mcfgObjs := fixtures.DefaultObjectsForListers()
		var filtered []runtime.Object
		for _, obj := range kubeObjs {
			if s, ok := obj.(*corev1.Secret); ok && s.Name == ctrlcommon.GlobalPullSecretCopyName {
				continue
			}
			filtered = append(filtered, obj)
		}
		l := newTestListers(filtered, mcfgObjs)

		// This must not panic; it should return an error referencing
		// the fallback secret name.
		_, err := newBuildRequestOptsFromAPI(ctx, l, lobj.MachineOSBuild, lobj.MachineOSConfig)
		assert.Error(t, err, "expected error when pull secret is missing")
		assert.Contains(t, err.Error(), ctrlcommon.GlobalPullSecretCopyName)
	})

	for _, testCase := range testCases {
		testCase := testCase
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()

			ctx, cancel := context.WithCancel(context.Background())
			t.Cleanup(cancel)

			_, _, lobj, _ := fixtures.GetClientsForTestWithAdditionalObjects(t, testCase.addlObjects, []runtime.Object{})

			if testCase.addlObjectSetup != nil {
				testCase.addlObjectSetup(t, lobj)
			}

			kubeObjs, mcfgObjs := fixtures.DefaultObjectsForListers()
			kubeObjs = append(kubeObjs, testCase.addlObjects...)
			l := newTestListers(kubeObjs, mcfgObjs)

			brOpts, err := newBuildRequestOptsFromAPI(ctx, l, lobj.MachineOSBuild, lobj.MachineOSConfig)
			assert.NoError(t, err)

			if testCase.addlAsserts != nil {
				assert.NoError(t, err)
				testCase.addlAsserts(t, *brOpts)
			}

			assert.NotNil(t, brOpts.MachineConfig)
			assert.NotNil(t, brOpts.MachineOSConfig)
			assert.NotNil(t, brOpts.MachineOSBuild)
			assert.NotNil(t, brOpts.Images)
			assert.NotNil(t, brOpts.BaseImagePullSecret)
			assert.NotNil(t, brOpts.FinalImagePushSecret)
		})
	}
}
