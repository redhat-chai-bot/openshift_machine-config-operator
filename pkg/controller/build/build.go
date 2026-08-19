package build

import (
	ctrlcommon "github.com/openshift/machine-config-operator/pkg/controller/common"
	"github.com/openshift/machine-config-operator/pkg/controller/build/metrics"
	v2 "github.com/openshift/machine-config-operator/pkg/controller/build/v2"
)

// Type aliases to preserve the external API surface.
// Switched from v1 to v2 as part of the build controller rewrite.
type OSBuildController = v2.OSBuildController
type Config = v2.Config

// NewOSBuildControllerFromControllerContext creates a new OSBuildController
// from the given ControllerContext with default configuration.
func NewOSBuildControllerFromControllerContext(ctrlCtx *ctrlcommon.ControllerContext) *OSBuildController {
	return v2.NewOSBuildControllerFromControllerContext(ctrlCtx)
}

// NewOSBuildControllerFromControllerContextWithConfig creates a new
// OSBuildController from the given ControllerContext with the provided
// configuration.
func NewOSBuildControllerFromControllerContextWithConfig(ctrlCtx *ctrlcommon.ControllerContext, cfg Config) *OSBuildController {
	return v2.NewOSBuildControllerFromControllerContextWithConfig(ctrlCtx, cfg)
}

// RegisterOCLMetrics registers all OCL-related Prometheus metrics.
func RegisterOCLMetrics() error {
	return metrics.RegisterOCLMetrics()
}
