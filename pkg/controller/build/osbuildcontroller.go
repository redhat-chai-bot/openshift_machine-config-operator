package build

import (
	"context"
	"time"

	ctrlcommon "github.com/openshift/machine-config-operator/pkg/controller/common"

	imagev1clientset "github.com/openshift/client-go/image/clientset/versioned"
	mcfgclientset "github.com/openshift/client-go/machineconfiguration/clientset/versioned"
	"github.com/openshift/client-go/machineconfiguration/clientset/versioned/scheme"
	routeclientset "github.com/openshift/client-go/route/clientset/versioned"
	"github.com/openshift/machine-config-operator/pkg/controller/build/imagepruner"
	"github.com/openshift/machine-config-operator/pkg/controller/build/reconcile"
	"github.com/openshift/machine-config-operator/pkg/controller/build/services"
	"github.com/openshift/machine-config-operator/pkg/controller/build/utils"
	corev1 "k8s.io/api/core/v1"
	clientset "k8s.io/client-go/kubernetes"
	coreclientsetv1 "k8s.io/client-go/kubernetes/typed/core/v1"
	"k8s.io/client-go/tools/record"
	"k8s.io/klog/v2"

	"github.com/prometheus/client_golang/prometheus"
)

// OSBuildController is the public entry point for the On-Cluster Layering
// build controller. It delegates to Controller and the new key-based
// reconcilers introduced in WS2-WS4.
type OSBuildController struct {
	inner *Controller

	// shutdownChan is closed when the controller finishes shutting down.
	shutdownChan <-chan struct{}
}

// Config holds controller configuration. Unchanged from the original.
type Config struct {
	// updateDelay is a pause to deal with churn in MachineConfigs; see
	// https://github.com/openshift/machine-config-operator/issues/301
	// Default: 5 seconds
	UpdateDelay time.Duration

	// maxRetries is the number of times a machineconfig pool will be retried before it is dropped out of the queue.
	// With the current rate-limiter in use (5ms*2^(maxRetries-1)) the following numbers represent the times
	// a machineconfig pool is going to be requeued:
	//
	// 5ms, 10ms, 20ms, 40ms, 80ms, 160ms, 320ms, 640ms, 1.3s, 2.6s, 5.1s, 10.2s, 20.4s, 41s, 82s
	// Default: 5
	MaxRetries int

	MaxShutdownDelay     time.Duration
	ShutdownPollInterval time.Duration
}

// Creates a Config with sensible production defaults.
func defaultConfig() Config {
	return Config{
		MaxRetries:           5,
		UpdateDelay:          time.Second * 5,
		MaxShutdownDelay:     time.Second * 10,
		ShutdownPollInterval: time.Millisecond * 100,
	}
}

// NewOSBuildControllerFromControllerContext creates the build controller
// from the shared controller context. This is the public entry point
// called by cmd/machine-os-builder/start.go.
func NewOSBuildControllerFromControllerContext(ctrlCtx *ctrlcommon.ControllerContext) *OSBuildController {
	return NewOSBuildControllerFromControllerContextWithConfig(ctrlCtx, defaultConfig())
}

// NewOSBuildControllerFromControllerContextWithConfig creates the controller with explicit config.
func NewOSBuildControllerFromControllerContextWithConfig(ctrlCtx *ctrlcommon.ControllerContext, cfg Config) *OSBuildController {
	return newOSBuildController(
		cfg,
		ctrlCtx.ClientBuilder.MachineConfigClientOrDie("machine-os-builder"),
		ctrlCtx.ClientBuilder.KubeClientOrDie("machine-os-builder"),
		ctrlCtx.ClientBuilder.ImageClientOrDie("machine-os-builder"),
		ctrlCtx.ClientBuilder.RouteClientOrDie("machine-os-builder"),
		imagepruner.NewImagePruner(),
	)
}

func newOSBuildController(
	ctrlConfig Config,
	mcfgclient mcfgclientset.Interface,
	kubeclient clientset.Interface,
	_ imagev1clientset.Interface, // reserved for future image API usage
	_ routeclientset.Interface, // reserved for future route API usage
	pruner imagepruner.ImagePruner,
) *OSBuildController {
	eventBroadcaster := record.NewBroadcaster()
	eventBroadcaster.StartLogging(klog.Infof)
	eventBroadcaster.StartRecordingToSink(&coreclientsetv1.EventSinkImpl{Interface: kubeclient.CoreV1().Events("")})

	inf := newInformers(mcfgclient, kubeclient)
	l := inf.listers()

	// Build event recorder.
	eventRecorder := eventBroadcaster.NewRecorder(scheme.Scheme, corev1.EventSource{Component: "machineosbuilder"})
	events := services.NewEventRecorder(eventRecorder)

	// Build metrics recorder — no-op at construction; RegisterOCLMetrics registers the real one.
	metrics := services.NewNoopMetricsRecorder()

	// Build utility listers for cross-resource lookups.
	utilListers := &utils.Listers{
		MachineOSBuildLister:    l.machineOSBuildLister,
		MachineOSConfigLister:   l.machineOSConfigLister,
		MachineConfigPoolLister: l.machineConfigPoolLister,
		NodeLister:              l.nodeLister,
	}

	// Construct services.
	degraded := services.NewDegradedHandler(mcfgclient, l.machineOSBuildLister)
	reuseChecker := services.NewImageReuseChecker(pruner, kubeclient, l.controllerConfigLister)
	seeder := services.NewSeeder(mcfgclient, kubeclient, l.machineConfigPoolLister, l.machineConfigLister)

	// Construct the 4 reconcilers.
	moscR := reconcile.NewMOSCReconciler(
		mcfgclient, kubeclient, l.machineOSConfigLister, l.machineOSBuildLister,
		l.machineConfigPoolLister, l.machineConfigLister,
		events, metrics, seeder, reuseChecker,
	)

	mosbR := reconcile.NewMOSBReconciler(
		mcfgclient, kubeclient, l.machineOSBuildLister, l.machineOSConfigLister,
		l.machineConfigPoolLister, l.machineConfigLister,
		events, metrics, degraded, utilListers,
	)

	poolR := reconcile.NewPoolReconciler(
		mcfgclient, l.machineConfigPoolLister, l.machineOSConfigLister,
		l.machineOSBuildLister, l.machineConfigLister,
		events, metrics, degraded, utilListers,
	)

	jobR := reconcile.NewJobReconciler(
		mcfgclient, kubeclient, l.jobLister, l.machineOSBuildLister,
		l.machineOSConfigLister, events, metrics, utilListers,
	)

	// Compose into a single Reconciler.
	r := &compositeReconciler{
		mosc: moscR,
		mosb: mosbR,
		pool: poolR,
		job:  jobR,
	}

	// Construct the multi-queue controller.
	bc := NewController(r, inf, l, ctrlConfig)

	return &OSBuildController{
		inner:        bc,
		shutdownChan: bc.ShutdownChan(),
	}
}

// Run starts the controller and blocks until the parent context is cancelled.
func (ctrl *OSBuildController) Run(parentCtx context.Context, workers int) {
	ctrl.inner.Run(parentCtx, workers)
}

// ShutdownChan returns a channel that is closed when the controller
// finishes its graceful shutdown.
func (ctrl *OSBuildController) ShutdownChan() <-chan struct{} {
	return ctrl.shutdownChan
}

// RegisterOCLMetrics registers all OCL-related Prometheus metrics.
// This preserves the public API signature used by cmd/machine-os-builder/start.go.
func RegisterOCLMetrics() error {
	_, err := services.NewMetricsRecorder(prometheus.DefaultRegisterer)
	return err
}

// compositeReconciler delegates each method to the appropriate sub-reconciler.
type compositeReconciler struct {
	mosc *reconcile.MOSCReconciler
	mosb *reconcile.MOSBReconciler
	pool *reconcile.PoolReconciler
	job  *reconcile.JobReconciler
}

func (c *compositeReconciler) ReconcileMOSC(ctx context.Context, key string) error {
	return c.mosc.ReconcileMOSC(ctx, key)
}
func (c *compositeReconciler) ReconcileMOSB(ctx context.Context, key string) error {
	return c.mosb.ReconcileMOSB(ctx, key)
}
func (c *compositeReconciler) ReconcilePool(ctx context.Context, key string) error {
	return c.pool.ReconcilePool(ctx, key)
}
func (c *compositeReconciler) ReconcileJob(ctx context.Context, key string) error {
	return c.job.ReconcileJob(ctx, key)
}
