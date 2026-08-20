package services

import (
	"context"
	"fmt"
	"testing"

	"github.com/containers/image/v5/types"
	"github.com/opencontainers/go-digest"
	mcfgv1 "github.com/openshift/api/machineconfiguration/v1"
	"github.com/openshift/machine-config-operator/pkg/controller/build/constants"
	"github.com/openshift/machine-config-operator/pkg/controller/build/imagepruner"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/runtime"
	fake "k8s.io/client-go/kubernetes/fake"
)

// fakeImagePruner implements imagepruner.ImagePruner for testing.
type fakeImagePruner struct {
	inspectResult *types.ImageInspectInfo
	inspectErr    error
	deleteErr     error
}

func (f *fakeImagePruner) InspectImage(_ context.Context, _ string, _ *corev1.Secret, _ *mcfgv1.ControllerConfig) (*types.ImageInspectInfo, *digest.Digest, error) {
	return f.inspectResult, nil, f.inspectErr
}

func (f *fakeImagePruner) DeleteImage(_ context.Context, _ string, _ *corev1.Secret, _ *mcfgv1.ControllerConfig) error {
	return f.deleteErr
}

// fakeCCLister implements mcfglistersv1.ControllerConfigLister for testing.
type fakeCCLister struct {
	items []*mcfgv1.ControllerConfig
	err   error
}

func (f *fakeCCLister) List(_ labels.Selector) (ret []*mcfgv1.ControllerConfig, err error) {
	return f.items, f.err
}

func (f *fakeCCLister) Get(name string) (*mcfgv1.ControllerConfig, error) {
	for _, item := range f.items {
		if item.Name == name {
			return item, nil
		}
	}
	return nil, fmt.Errorf("not found: %s", name)
}

// TestImageReuseCheckerInterface verifies compile-time interface satisfaction.
func TestImageReuseCheckerInterface(t *testing.T) {
	var _ ImageReuseChecker = &imageReuseChecker{}
}

// TestNewImageReuseChecker verifies construction.
func TestNewImageReuseChecker(t *testing.T) {
	checker := NewImageReuseChecker(&fakeImagePruner{}, nil, nil)
	if checker == nil {
		t.Fatal("NewImageReuseChecker returned nil")
	}
}

// newTestChecker creates a checker with a fake kubeclient that has the
// required push secret, a fake CC lister, and the given pruner.
func newTestChecker(pruner *fakeImagePruner) *imageReuseChecker {
	secret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "push-secret",
			Namespace: "openshift-machine-config-operator",
		},
	}
	cc := &mcfgv1.ControllerConfig{
		ObjectMeta: metav1.ObjectMeta{Name: "machine-config-controller"},
	}

	kubeclient := fake.NewSimpleClientset([]runtime.Object{secret}...)
	ccLister := &fakeCCLister{items: []*mcfgv1.ControllerConfig{cc}}

	return &imageReuseChecker{
		imagePruner: pruner,
		kubeclient:  kubeclient,
		ccLister:    ccLister,
	}
}

// TestEvaluateReuse_SuccessImageExists verifies reuse when the image still exists.
func TestEvaluateReuse_SuccessImageExists(t *testing.T) {
	checker := newTestChecker(&fakeImagePruner{
		inspectResult: &types.ImageInspectInfo{},
	})

	mosc := &mcfgv1.MachineOSConfig{
		ObjectMeta: metav1.ObjectMeta{Name: "test-mosc"},
	}

	mosb := &mcfgv1.MachineOSBuild{
		ObjectMeta: metav1.ObjectMeta{
			Name: "test-mosb",
			Annotations: map[string]string{
				constants.RenderedImagePushSecretAnnotationKey: "push-secret",
			},
		},
		Spec: mcfgv1.MachineOSBuildSpec{
			RenderedImagePushSpec: "registry.example.com/image:latest",
		},
		Status: mcfgv1.MachineOSBuildStatus{
			DigestedImagePushSpec: "registry.example.com/image@sha256:abc",
			Conditions: []metav1.Condition{
				{Type: string(mcfgv1.MachineOSBuildSucceeded), Status: metav1.ConditionTrue},
			},
		},
	}

	result, err := checker.EvaluateReuse(context.Background(), mosc, mosb)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !result.CanReuse {
		t.Error("expected CanReuse=true")
	}
	if result.NeedsRebuild {
		t.Error("expected NeedsRebuild=false")
	}
}

// TestEvaluateReuse_SuccessImageGone verifies rebuild when image is gone.
func TestEvaluateReuse_SuccessImageGone(t *testing.T) {
	checker := newTestChecker(&fakeImagePruner{
		inspectErr: fmt.Errorf("image not found"),
	})

	mosc := &mcfgv1.MachineOSConfig{
		ObjectMeta: metav1.ObjectMeta{Name: "test-mosc"},
	}

	mosb := &mcfgv1.MachineOSBuild{
		ObjectMeta: metav1.ObjectMeta{
			Name: "test-mosb",
			Annotations: map[string]string{
				constants.RenderedImagePushSecretAnnotationKey: "push-secret",
			},
		},
		Spec: mcfgv1.MachineOSBuildSpec{
			RenderedImagePushSpec: "registry.example.com/image:latest",
		},
		Status: mcfgv1.MachineOSBuildStatus{
			DigestedImagePushSpec: "registry.example.com/image@sha256:abc",
			Conditions: []metav1.Condition{
				{Type: string(mcfgv1.MachineOSBuildSucceeded), Status: metav1.ConditionTrue},
			},
		},
	}

	result, err := checker.EvaluateReuse(context.Background(), mosc, mosb)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.CanReuse {
		t.Error("expected CanReuse=false")
	}
	if !result.NeedsRebuild {
		t.Error("expected NeedsRebuild=true")
	}
}

// TestEvaluateReuse_TransientState verifies reuse for transient builds.
func TestEvaluateReuse_TransientState(t *testing.T) {
	checker := newTestChecker(&fakeImagePruner{})

	mosc := &mcfgv1.MachineOSConfig{
		ObjectMeta: metav1.ObjectMeta{Name: "test-mosc"},
	}

	// A build in "Prepared" state (transient — no success condition, but prepared=true).
	mosb := &mcfgv1.MachineOSBuild{
		ObjectMeta: metav1.ObjectMeta{Name: "transient-mosb"},
		Status: mcfgv1.MachineOSBuildStatus{
			Conditions: []metav1.Condition{
				{Type: string(mcfgv1.MachineOSBuildPrepared), Status: metav1.ConditionTrue},
			},
		},
	}

	result, err := checker.EvaluateReuse(context.Background(), mosc, mosb)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !result.CanReuse {
		t.Error("expected CanReuse=true for transient state build")
	}
}

// TestEvaluateReuse_NoReuse verifies neither reuse nor rebuild for other states.
func TestEvaluateReuse_NoReuse(t *testing.T) {
	checker := newTestChecker(&fakeImagePruner{})

	mosc := &mcfgv1.MachineOSConfig{
		ObjectMeta: metav1.ObjectMeta{Name: "test-mosc"},
	}

	// A failed build — cannot reuse and doesn't need rebuild via this path.
	mosb := &mcfgv1.MachineOSBuild{
		ObjectMeta: metav1.ObjectMeta{Name: "failed-mosb"},
		Status: mcfgv1.MachineOSBuildStatus{
			Conditions: []metav1.Condition{
				{Type: string(mcfgv1.MachineOSBuildFailed), Status: metav1.ConditionTrue},
			},
		},
	}

	result, err := checker.EvaluateReuse(context.Background(), mosc, mosb)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.CanReuse {
		t.Error("expected CanReuse=false for failed build")
	}
	if result.NeedsRebuild {
		t.Error("expected NeedsRebuild=false for failed build")
	}
}

// TestImageReuseResult_ZeroValue verifies default zero-value semantics.
func TestImageReuseResult_ZeroValue(t *testing.T) {
	var r ImageReuseResult
	if r.CanReuse {
		t.Error("zero-value CanReuse should be false")
	}
	if r.NeedsRebuild {
		t.Error("zero-value NeedsRebuild should be false")
	}
}

// TestEvaluateReuse_UsesDigestedPullSpec verifies that the reuse checker
// inspects the digest-pinned DigestedImagePushSpec, not the tag-based
// RenderedImagePushSpec. The tag may have been overwritten by a newer build.
func TestEvaluateReuse_UsesDigestedPullSpec(t *testing.T) {
	digestPullspec := "registry.example.com/image@sha256:specificdigest"
	tagPullspec := "registry.example.com/image:latest"

	// The fake pruner records which pullspec was inspected via
	// the inspectResult being non-nil only when called.
	inspectedPullspec := ""
	pruner := &pullspecCapturingPruner{
		inspectResult: &types.ImageInspectInfo{},
		capture:       &inspectedPullspec,
	}

	checker := newTestCheckerWithPruner(pruner)

	mosc := &mcfgv1.MachineOSConfig{
		ObjectMeta: metav1.ObjectMeta{Name: "test-mosc"},
	}

	mosb := &mcfgv1.MachineOSBuild{
		ObjectMeta: metav1.ObjectMeta{
			Name: "test-mosb",
			Annotations: map[string]string{
				constants.RenderedImagePushSecretAnnotationKey: "push-secret",
			},
		},
		Spec: mcfgv1.MachineOSBuildSpec{
			RenderedImagePushSpec: mcfgv1.ImageTagFormat(tagPullspec),
		},
		Status: mcfgv1.MachineOSBuildStatus{
			DigestedImagePushSpec: mcfgv1.ImageDigestFormat(digestPullspec),
			Conditions: []metav1.Condition{
				{Type: string(mcfgv1.MachineOSBuildSucceeded), Status: metav1.ConditionTrue},
			},
		},
	}

	result, err := checker.EvaluateReuse(context.Background(), mosc, mosb)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !result.CanReuse {
		t.Error("expected CanReuse=true")
	}
	if inspectedPullspec != digestPullspec {
		t.Errorf("expected inspection of digest pullspec %q, but got %q", digestPullspec, inspectedPullspec)
	}
	if inspectedPullspec == tagPullspec {
		t.Error("should NOT have inspected the tag-based pullspec")
	}
}

// pullspecCapturingPruner records the pullspec passed to InspectImage.
type pullspecCapturingPruner struct {
	inspectResult *types.ImageInspectInfo
	inspectErr    error
	capture       *string
}

func (f *pullspecCapturingPruner) InspectImage(_ context.Context, pullspec string, _ *corev1.Secret, _ *mcfgv1.ControllerConfig) (*types.ImageInspectInfo, *digest.Digest, error) {
	*f.capture = pullspec
	return f.inspectResult, nil, f.inspectErr
}

func (f *pullspecCapturingPruner) DeleteImage(_ context.Context, _ string, _ *corev1.Secret, _ *mcfgv1.ControllerConfig) error {
	return nil
}

// newTestCheckerWithPruner creates a checker with a custom pruner.
func newTestCheckerWithPruner(pruner imagepruner.ImagePruner) *imageReuseChecker {
	secret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "push-secret",
			Namespace: "openshift-machine-config-operator",
		},
	}
	cc := &mcfgv1.ControllerConfig{
		ObjectMeta: metav1.ObjectMeta{Name: "machine-config-controller"},
	}

	kubeclient := fake.NewSimpleClientset([]runtime.Object{secret}...)
	ccLister := &fakeCCLister{items: []*mcfgv1.ControllerConfig{cc}}

	return &imageReuseChecker{
		imagePruner: pruner,
		kubeclient:  kubeclient,
		ccLister:    ccLister,
	}
}

// TestInspectImage_MissingAnnotation verifies error when secret annotation is missing.
func TestInspectImage_MissingAnnotation(t *testing.T) {
	checker := newTestChecker(&fakeImagePruner{})

	mosb := &mcfgv1.MachineOSBuild{
		ObjectMeta: metav1.ObjectMeta{Name: "no-annotation"},
	}

	_, err := checker.InspectImage(context.Background(), "some-pullspec", mosb)
	if err == nil {
		t.Fatal("expected error for missing annotation")
	}
}
