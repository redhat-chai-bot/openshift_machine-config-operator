package services

import (
	"context"
	"fmt"
	"testing"

	mcfgv1 "github.com/openshift/api/machineconfiguration/v1"
	fakeclientmachineconfigurationv2 "github.com/openshift/client-go/machineconfiguration/clientset/versioned/fake"
	mcfglistersv1 "github.com/openshift/client-go/machineconfiguration/listers/machineconfiguration/v1"
	"github.com/openshift/machine-config-operator/pkg/controller/build/constants"
	ctrlcommon "github.com/openshift/machine-config-operator/pkg/controller/common"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/runtime"
	k8sfake "k8s.io/client-go/kubernetes/fake"
)

// TestSeederInterface verifies compile-time interface satisfaction.
func TestSeederInterface(t *testing.T) {
	var _ Seeder = &seeder{}
}

// TestNewSeeder verifies construction.
func TestNewSeeder(t *testing.T) {
	s := NewSeeder(nil, nil, nil, nil)
	if s == nil {
		t.Fatal("NewSeeder returned nil")
	}
}

func TestGetPreBuiltImage_Present(t *testing.T) {
	s := &seeder{}
	mosc := &mcfgv1.MachineOSConfig{
		ObjectMeta: metav1.ObjectMeta{
			Annotations: map[string]string{
				constants.PreBuiltImageAnnotationKey: "registry.example.com/image:latest",
			},
		},
	}

	img, ok := s.GetPreBuiltImage(mosc)
	if !ok {
		t.Error("expected ok=true")
	}
	if img != "registry.example.com/image:latest" {
		t.Errorf("expected image pullspec, got %q", img)
	}
}

func TestGetPreBuiltImage_Missing(t *testing.T) {
	s := &seeder{}
	mosc := &mcfgv1.MachineOSConfig{}

	_, ok := s.GetPreBuiltImage(mosc)
	if ok {
		t.Error("expected ok=false for missing annotation")
	}
}

func TestGetPreBuiltImage_Empty(t *testing.T) {
	s := &seeder{}
	mosc := &mcfgv1.MachineOSConfig{
		ObjectMeta: metav1.ObjectMeta{
			Annotations: map[string]string{
				constants.PreBuiltImageAnnotationKey: "",
			},
		},
	}

	_, ok := s.GetPreBuiltImage(mosc)
	if ok {
		t.Error("expected ok=false for empty annotation value")
	}
}

func TestShouldSeed_Yes(t *testing.T) {
	s := &seeder{}
	mosc := &mcfgv1.MachineOSConfig{
		ObjectMeta: metav1.ObjectMeta{
			Annotations: map[string]string{
				constants.PreBuiltImageAnnotationKey: "image:latest",
			},
		},
	}

	if !s.ShouldSeed(mosc) {
		t.Error("expected ShouldSeed=true when annotation present and no current build")
	}
}

func TestShouldSeed_No_AlreadySeeded(t *testing.T) {
	s := &seeder{}
	mosc := &mcfgv1.MachineOSConfig{
		ObjectMeta: metav1.ObjectMeta{
			Annotations: map[string]string{
				constants.PreBuiltImageAnnotationKey:         "image:latest",
				constants.CurrentMachineOSBuildAnnotationKey: "build-abc",
			},
		},
	}

	if s.ShouldSeed(mosc) {
		t.Error("expected ShouldSeed=false when current build annotation is present")
	}
}

func TestShouldSeed_No_NoAnnotation(t *testing.T) {
	s := &seeder{}
	mosc := &mcfgv1.MachineOSConfig{}

	if s.ShouldSeed(mosc) {
		t.Error("expected ShouldSeed=false when no pre-built image annotation")
	}
}

func TestNeedsAnnotationCleanup_Yes(t *testing.T) {
	s := &seeder{}
	mosc := &mcfgv1.MachineOSConfig{
		ObjectMeta: metav1.ObjectMeta{
			Annotations: map[string]string{
				constants.PreBuiltImageAnnotationKey:         "image:latest",
				constants.CurrentMachineOSBuildAnnotationKey: "build-abc",
			},
		},
		Status: mcfgv1.MachineOSConfigStatus{
			CurrentImagePullSpec: "image@sha256:abc",
		},
	}

	if !s.NeedsAnnotationCleanup(mosc) {
		t.Error("expected NeedsAnnotationCleanup=true")
	}
}

func TestNeedsAnnotationCleanup_No_NoCurrentBuild(t *testing.T) {
	s := &seeder{}
	mosc := &mcfgv1.MachineOSConfig{
		ObjectMeta: metav1.ObjectMeta{
			Annotations: map[string]string{
				constants.PreBuiltImageAnnotationKey: "image:latest",
			},
		},
	}

	if s.NeedsAnnotationCleanup(mosc) {
		t.Error("expected NeedsAnnotationCleanup=false when no current build annotation")
	}
}

func TestNeedsAnnotationCleanup_No_NoImagePullSpec(t *testing.T) {
	s := &seeder{}
	mosc := &mcfgv1.MachineOSConfig{
		ObjectMeta: metav1.ObjectMeta{
			Annotations: map[string]string{
				constants.PreBuiltImageAnnotationKey:         "image:latest",
				constants.CurrentMachineOSBuildAnnotationKey: "build-abc",
			},
		},
		// Status.CurrentImagePullSpec is empty.
	}

	if s.NeedsAnnotationCleanup(mosc) {
		t.Error("expected NeedsAnnotationCleanup=false when image pull spec not set")
	}
}

func TestNeedsAnnotationCleanup_No_AnnotationAlreadyRemoved(t *testing.T) {
	s := &seeder{}
	mosc := &mcfgv1.MachineOSConfig{
		ObjectMeta: metav1.ObjectMeta{
			Annotations: map[string]string{
				constants.CurrentMachineOSBuildAnnotationKey: "build-abc",
			},
		},
		Status: mcfgv1.MachineOSConfigStatus{
			CurrentImagePullSpec: "image@sha256:abc",
		},
	}

	if s.NeedsAnnotationCleanup(mosc) {
		t.Error("expected NeedsAnnotationCleanup=false when pre-built annotation already removed")
	}
}

func TestHasCurrentBuildAnnotation(t *testing.T) {
	tests := []struct {
		name        string
		annotations map[string]string
		want        bool
	}{
		{"nil annotations", nil, false},
		{"empty annotations", map[string]string{}, false},
		{"has annotation", map[string]string{constants.CurrentMachineOSBuildAnnotationKey: "build-1"}, true},
		{"other annotations only", map[string]string{"foo": "bar"}, false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			mosc := &mcfgv1.MachineOSConfig{
				ObjectMeta: metav1.ObjectMeta{Annotations: tt.annotations},
			}
			got := hasCurrentBuildAnnotation(mosc)
			if got != tt.want {
				t.Errorf("hasCurrentBuildAnnotation() = %v, want %v", got, tt.want)
			}
		})
	}
}

func newFakeSeedClients(objs ...runtime.Object) (*fakeclientmachineconfigurationv2.Clientset, *k8sfake.Clientset) {
	var mcfgObjs, k8sObjs []runtime.Object
	for _, o := range objs {
		switch o.(type) {
		case *corev1.Secret:
			k8sObjs = append(k8sObjs, o)
		default:
			mcfgObjs = append(mcfgObjs, o)
		}
	}
	return fakeclientmachineconfigurationv2.NewSimpleClientset(mcfgObjs...),
		k8sfake.NewSimpleClientset(k8sObjs...)
}

func TestEnsureSecretExists_Found(t *testing.T) {
	secret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "push-secret",
			Namespace: ctrlcommon.MCONamespace,
		},
	}
	_, kubeclient := newFakeSeedClients(secret)
	s := &seeder{kubeclient: kubeclient}

	err := s.ensureSecretExists(context.Background(), "push-secret")
	if err != nil {
		t.Fatalf("expected no error, got: %v", err)
	}
}

func TestEnsureSecretExists_NotFound(t *testing.T) {
	_, kubeclient := newFakeSeedClients()
	s := &seeder{kubeclient: kubeclient}

	err := s.ensureSecretExists(context.Background(), "missing-secret")
	if err == nil {
		t.Fatal("expected error for missing secret")
	}
}

func TestSeed_MissingPushSecret(t *testing.T) {
	mcfgclient, kubeclient := newFakeSeedClients()
	s := &seeder{mcfgclient: mcfgclient, kubeclient: kubeclient}

	mosc := &mcfgv1.MachineOSConfig{
		ObjectMeta: metav1.ObjectMeta{Name: "mosc-1"},
		Spec:       mcfgv1.MachineOSConfigSpec{},
	}

	err := s.Seed(context.Background(), mosc, "image:latest")
	if err == nil {
		t.Fatal("expected error for missing push secret name")
	}
}

func TestSeed_SecretNotFound(t *testing.T) {
	mcfgclient, kubeclient := newFakeSeedClients()
	s := &seeder{mcfgclient: mcfgclient, kubeclient: kubeclient}

	mosc := &mcfgv1.MachineOSConfig{
		ObjectMeta: metav1.ObjectMeta{Name: "mosc-1"},
		Spec: mcfgv1.MachineOSConfigSpec{
			RenderedImagePushSecret: mcfgv1.ImageSecretObjectReference{Name: "no-such-secret"},
		},
	}

	err := s.Seed(context.Background(), mosc, "image:latest")
	if err == nil {
		t.Fatal("expected error for secret not found")
	}
}

// Compile-time interface satisfaction checks for fake listers.
var (
	_ mcfglistersv1.MachineConfigPoolLister = &fakeMCPListerSeed{}
	_ mcfglistersv1.MachineConfigLister     = &fakeMCListerSeed{}
)

// fakeMCPListerSeed is a simple in-memory lister for MachineConfigPools.
type fakeMCPListerSeed struct {
	items []*mcfgv1.MachineConfigPool
}

func (f *fakeMCPListerSeed) List(_ labels.Selector) ([]*mcfgv1.MachineConfigPool, error) {
	return f.items, nil
}

func (f *fakeMCPListerSeed) Get(name string) (*mcfgv1.MachineConfigPool, error) {
	for _, m := range f.items {
		if m.Name == name {
			return m, nil
		}
	}
	return nil, fmt.Errorf("not found: %s", name)
}

// fakeMCListerSeed is a simple in-memory lister for MachineConfigs.
type fakeMCListerSeed struct {
	items []*mcfgv1.MachineConfig
}

func (f *fakeMCListerSeed) List(_ labels.Selector) ([]*mcfgv1.MachineConfig, error) {
	return f.items, nil
}

func (f *fakeMCListerSeed) Get(name string) (*mcfgv1.MachineConfig, error) {
	for _, m := range f.items {
		if m.Name == name {
			return m, nil
		}
	}
	return nil, fmt.Errorf("not found: %s", name)
}

func TestSeed_FullWorkflow(t *testing.T) {
	secret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "push-secret",
			Namespace: ctrlcommon.MCONamespace,
		},
	}
	mosc := &mcfgv1.MachineOSConfig{
		ObjectMeta: metav1.ObjectMeta{
			Name: "mosc-1",
			Annotations: map[string]string{
				constants.PreBuiltImageAnnotationKey: "registry.example.com/image:v1",
			},
		},
		Spec: mcfgv1.MachineOSConfigSpec{
			MachineConfigPool:       mcfgv1.MachineConfigPoolReference{Name: "worker"},
			RenderedImagePushSpec:   "registry.example.com/image",
			RenderedImagePushSecret: mcfgv1.ImageSecretObjectReference{Name: "push-secret"},
		},
	}
	mc := &mcfgv1.MachineConfig{
		ObjectMeta: metav1.ObjectMeta{
			Name: "rendered-worker-abc",
			Annotations: map[string]string{
				ctrlcommon.ReleaseImageVersionAnnotationKey:          "4.19.0",
				ctrlcommon.GeneratedByControllerVersionAnnotationKey: "4.19.0",
			},
		},
	}
	mcp := &mcfgv1.MachineConfigPool{
		ObjectMeta: metav1.ObjectMeta{Name: "worker"},
		Spec: mcfgv1.MachineConfigPoolSpec{
			Configuration: mcfgv1.MachineConfigPoolStatusConfiguration{
				ObjectReference: corev1.ObjectReference{Name: "rendered-worker-abc"},
			},
		},
	}

	mcfgclient := fakeclientmachineconfigurationv2.NewSimpleClientset(mosc)
	_, kubeclient := newFakeSeedClients(secret)

	s := &seeder{
		mcfgclient: mcfgclient,
		kubeclient: kubeclient,
		mcpLister:  &fakeMCPListerSeed{items: []*mcfgv1.MachineConfigPool{mcp}},
		mcLister:   &fakeMCListerSeed{items: []*mcfgv1.MachineConfig{mc}},
	}

	err := s.Seed(context.Background(), mosc, "registry.example.com/image:v1")
	if err != nil {
		t.Fatalf("Seed error: %v", err)
	}

	// Verify MOSC was updated with current build annotation.
	updatedMOSC, err := mcfgclient.MachineconfigurationV1().MachineOSConfigs().Get(context.Background(), "mosc-1", metav1.GetOptions{})
	if err != nil {
		t.Fatalf("get mosc: %v", err)
	}
	if _, ok := updatedMOSC.Annotations[constants.CurrentMachineOSBuildAnnotationKey]; !ok {
		t.Error("expected current-build annotation to be set after Seed")
	}
}

func TestSeed_MCPNotFound(t *testing.T) {
	secret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "push-secret",
			Namespace: ctrlcommon.MCONamespace,
		},
	}
	mosc := &mcfgv1.MachineOSConfig{
		ObjectMeta: metav1.ObjectMeta{Name: "mosc-1"},
		Spec: mcfgv1.MachineOSConfigSpec{
			MachineConfigPool:       mcfgv1.MachineConfigPoolReference{Name: "missing-pool"},
			RenderedImagePushSecret: mcfgv1.ImageSecretObjectReference{Name: "push-secret"},
		},
	}

	mcfgclient := fakeclientmachineconfigurationv2.NewSimpleClientset(mosc)
	_, kubeclient := newFakeSeedClients(secret)

	s := &seeder{
		mcfgclient: mcfgclient,
		kubeclient: kubeclient,
		mcpLister:  &fakeMCPListerSeed{items: nil},
		mcLister:   &fakeMCListerSeed{items: nil},
	}

	err := s.Seed(context.Background(), mosc, "image:latest")
	if err == nil {
		t.Fatal("expected error for missing MCP")
	}
}
