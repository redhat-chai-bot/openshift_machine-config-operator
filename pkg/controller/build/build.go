package build

import (
	ctrlcommon "github.com/openshift/machine-config-operator/pkg/controller/common"
	v1 "github.com/openshift/machine-config-operator/pkg/controller/build/v1"
	"github.com/openshift/machine-config-operator/pkg/controller/build/metrics"
)

// Type aliases to preserve the external API surface.
type OSBuildController = v1.OSBuildController
type Config = v1.Config

// NewOSBuildControllerFromControllerContext creates a new OSBuildController
// from the given ControllerContext with default configuration.
func NewOSBuildControllerFromControllerContext(ctrlCtx *ctrlcommon.ControllerContext) *OSBuildController {
	return v1.NewOSBuildControllerFromControllerContext(ctrlCtx)
}

// NewOSBuildControllerFromControllerContextWithConfig creates a new
// OSBuildController from the given ControllerContext with the provided
// configuration.
func NewOSBuildControllerFromControllerContextWithConfig(ctrlCtx *ctrlcommon.ControllerContext, cfg Config) *OSBuildController {
	return v1.NewOSBuildControllerFromControllerContextWithConfig(ctrlCtx, cfg)
}

// RegisterOCLMetrics registers all OCL-related Prometheus metrics.
func RegisterOCLMetrics() error {
	return metrics.RegisterOCLMetrics()
}
