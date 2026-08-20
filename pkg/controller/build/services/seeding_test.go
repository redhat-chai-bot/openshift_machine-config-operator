package services

import (
	"testing"

	mcfgv1 "github.com/openshift/api/machineconfiguration/v1"
	"github.com/openshift/machine-config-operator/pkg/controller/build/constants"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
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
