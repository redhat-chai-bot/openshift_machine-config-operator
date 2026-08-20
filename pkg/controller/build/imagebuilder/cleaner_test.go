package imagebuilder

import (
	"context"
	"testing"

	mcfgv1 "github.com/openshift/api/machineconfiguration/v1"
	fakeclientmachineconfigv1 "github.com/openshift/client-go/machineconfiguration/clientset/versioned/fake"
	"github.com/openshift/machine-config-operator/pkg/controller/build/constants"
	ctrlcommon "github.com/openshift/machine-config-operator/pkg/controller/common"
	"github.com/stretchr/testify/assert"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	fakecorev1client "k8s.io/client-go/kubernetes/fake"
)

// TestCleanerToleratesNotFound verifies that the cleaner silently skips
// deletion of ConfigMaps and Secrets that have already been removed (i.e.
// the API server returns NotFound). Before the fix, a generic error return
// preceded the IsNotFound check, making it unreachable.
func TestCleanerToleratesNotFound(t *testing.T) {
	t.Parallel()

	ctx := context.Background()

	mosb := &mcfgv1.MachineOSBuild{
		ObjectMeta: metav1.ObjectMeta{
			Name: "test-mosb",
			Annotations: map[string]string{
				constants.JobUIDAnnotationKey: "fake-uid-123",
			},
			Labels: map[string]string{
				constants.TargetMachineConfigPoolLabelKey: "worker",
				constants.RenderedMachineConfigLabelKey:   "rendered-worker-1",
				constants.MachineOSConfigNameLabelKey:     "test-mosc",
			},
		},
	}

	mosc := &mcfgv1.MachineOSConfig{
		ObjectMeta: metav1.ObjectMeta{
			Name: "test-mosc",
		},
		Spec: mcfgv1.MachineOSConfigSpec{
			MachineConfigPool: mcfgv1.MachineConfigPoolReference{Name: "worker"},
		},
	}

	// Create kubeclient without any ConfigMaps or Secrets — all Get calls
	// will return NotFound.
	kubeclient := fakecorev1client.NewSimpleClientset()
	mcfgclient := fakeclientmachineconfigv1.NewSimpleClientset()

	c := &cleanerImpl{
		baseImageBuilder: newBaseImageBuilder(kubeclient, mcfgclient, mosb, mosc, nil, mcfgv1.JobBuilder),
	}

	// deleteConfigMap should tolerate NotFound.
	err := c.deleteConfigMap(ctx, "nonexistent-cm", mosb.Name, "fake-uid-123")
	assert.NoError(t, err, "deleteConfigMap should tolerate NotFound")

	// deleteSecret should tolerate NotFound.
	err = c.deleteSecret(ctx, "nonexistent-secret", mosb.Name, "fake-uid-123")
	assert.NoError(t, err, "deleteSecret should tolerate NotFound")
}

// TestCleanerDeletesExistingConfigMap verifies that the cleaner actually
// deletes a ConfigMap that exists and has the matching owner ref UID.
func TestCleanerDeletesExistingConfigMap(t *testing.T) {
	t.Parallel()

	ctx := context.Background()

	cm := &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "build-cm",
			Namespace: ctrlcommon.MCONamespace,
			OwnerReferences: []metav1.OwnerReference{
				{UID: "job-uid-456"},
			},
		},
	}

	kubeclient := fakecorev1client.NewSimpleClientset(cm)
	mcfgclient := fakeclientmachineconfigv1.NewSimpleClientset()

	mosb := &mcfgv1.MachineOSBuild{
		ObjectMeta: metav1.ObjectMeta{
			Name: "test-mosb",
			Annotations: map[string]string{
				constants.JobUIDAnnotationKey: "job-uid-456",
			},
		},
	}

	c := &cleanerImpl{
		baseImageBuilder: newBaseImageBuilder(kubeclient, mcfgclient, mosb, nil, nil, mcfgv1.JobBuilder),
	}

	err := c.deleteConfigMap(ctx, "build-cm", mosb.Name, "job-uid-456")
	assert.NoError(t, err)

	// Verify the ConfigMap was deleted.
	_, err = kubeclient.CoreV1().ConfigMaps(ctrlcommon.MCONamespace).Get(ctx, "build-cm", metav1.GetOptions{})
	assert.Error(t, err, "ConfigMap should have been deleted")
}
