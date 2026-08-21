package reconcile

import (
	"github.com/openshift/machine-config-operator/pkg/controller/build/internal/access"
	"github.com/openshift/machine-config-operator/pkg/controller/build/services"
)

// Deps bundles all shared dependencies needed by the build-controller
// reconcilers into a single value. Individual reconcilers extract the
// subset they need rather than accepting a long parameter list.
type Deps struct {
	// Accessors bundles API clients and informer-backed listers.
	Accessors *access.Accessors

	// Service interfaces
	Events       services.EventRecorder
	Metrics      services.MetricsRecorder
	Degraded     services.DegradedHandler
	Seeder       services.Seeder
	ReuseChecker services.ImageReuseChecker
}
