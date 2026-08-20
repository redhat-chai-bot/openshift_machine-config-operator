package reconcile

import (
	"fmt"

	mcfgv1 "github.com/openshift/api/machineconfiguration/v1"
	corev1 "k8s.io/api/core/v1"
	k8serrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/runtime/schema"
)

// notFound returns a k8s NotFound error for testing.
func notFound(kind, name string) error {
	return k8serrors.NewNotFound(schema.GroupResource{Resource: kind}, name)
}

// mcpWithConfig creates a MachineConfigPool with the given spec.configuration.name.
func mcpWithConfig(name, configName string) *mcfgv1.MachineConfigPool {
	return &mcfgv1.MachineConfigPool{
		Spec: mcfgv1.MachineConfigPoolSpec{
			Configuration: mcfgv1.MachineConfigPoolStatusConfiguration{
				ObjectReference: corev1.ObjectReference{Name: configName},
			},
		},
	}
}

// corev1Ref creates a corev1.ObjectReference with the given name.
func corev1Ref(name string) corev1.ObjectReference {
	return corev1.ObjectReference{Name: name}
}

// --- Label selector adapters ---
// The real listers use labels.Selector, but our fakes use interface{}.
// These adapters satisfy both the test fakes above and the lister interfaces.

// fakeMOSBListerForSelector wraps fakeMOSBLister and satisfies the real
// MachineOSBuildLister interface by accepting labels.Selector.
type fakeMOSBListerForSelector struct {
	items []*mcfgv1.MachineOSBuild
}

func (f *fakeMOSBListerForSelector) List(sel labels.Selector) (ret []*mcfgv1.MachineOSBuild, err error) {
	for _, v := range f.items {
		if sel.Matches(labels.Set(v.Labels)) {
			ret = append(ret, v)
		}
	}
	return ret, nil
}

func (f *fakeMOSBListerForSelector) Get(name string) (*mcfgv1.MachineOSBuild, error) {
	for _, v := range f.items {
		if v.Name == name {
			return v, nil
		}
	}
	return nil, notFound("MachineOSBuild", name)
}

// fakeMOSCListerForSelector wraps for real MachineOSConfigLister interface.
type fakeMOSCListerForSelector struct {
	items []*mcfgv1.MachineOSConfig
}

func (f *fakeMOSCListerForSelector) List(sel labels.Selector) (ret []*mcfgv1.MachineOSConfig, err error) {
	for _, v := range f.items {
		if sel.Matches(labels.Set(v.Labels)) {
			ret = append(ret, v)
		}
	}
	return ret, nil
}

func (f *fakeMOSCListerForSelector) Get(name string) (*mcfgv1.MachineOSConfig, error) {
	for _, v := range f.items {
		if v.Name == name {
			return v, nil
		}
	}
	return nil, notFound("MachineOSConfig", name)
}

// fakeMCPListerForSelector wraps for real MachineConfigPoolLister interface.
type fakeMCPListerForSelector struct {
	items []*mcfgv1.MachineConfigPool
}

func (f *fakeMCPListerForSelector) List(sel labels.Selector) (ret []*mcfgv1.MachineConfigPool, err error) {
	for _, v := range f.items {
		if sel.Matches(labels.Set(v.Labels)) {
			ret = append(ret, v)
		}
	}
	return ret, nil
}

func (f *fakeMCPListerForSelector) Get(name string) (*mcfgv1.MachineConfigPool, error) {
	for _, v := range f.items {
		if v.Name == name {
			return v, nil
		}
	}
	return nil, notFound("MachineConfigPool", name)
}

// fakeMCListerForSelector wraps for real MachineConfigLister interface.
type fakeMCListerForSelector struct {
	items []*mcfgv1.MachineConfig
}

func (f *fakeMCListerForSelector) List(sel labels.Selector) (ret []*mcfgv1.MachineConfig, err error) {
	for _, v := range f.items {
		if sel.Matches(labels.Set(v.Labels)) {
			ret = append(ret, v)
		}
	}
	return ret, nil
}

func (f *fakeMCListerForSelector) Get(name string) (*mcfgv1.MachineConfig, error) {
	for _, v := range f.items {
		if v.Name == name {
			return v, nil
		}
	}
	return nil, notFound("MachineConfig", name)
}

// Ensure fake listers work (compile-time check — these are used in tests)
var _ fmt.Stringer = fmt.Stringer(nil) // dummy to keep fmt imported
