package build

import (
	"context"
	"fmt"
	"time"

	ctrlcommon "github.com/openshift/machine-config-operator/pkg/controller/common"

	mcfgclientset "github.com/openshift/client-go/machineconfiguration/clientset/versioned"
	"github.com/openshift/client-go/machineconfiguration/clientset/versioned/scheme"
	"github.com/openshift/machine-config-operator/pkg/controller/build/imagepruner"
	"github.com/openshift/machine-config-operator/pkg/controller/build/reconcile"
	"github.com/openshift/machine-config-operator/pkg/controller/build/services"
	corev1 "k8s.io/api/core/v1"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	"k8s.io/apimachinery/pkg/util/wait"
	clientset "k8s.io/client-go/kubernetes"
	coreclientsetv1 "k8s.io/client-go/kubernetes/typed/core/v1"
	"k8s.io/client-go/tools/cache"
	"k8s.io/client-go/tools/record"
	"k8s.io/client-go/util/workqueue"
	"k8s.io/klog/v2"
	clockutil "k8s.io/utils/clock"

	"github.com/prometheus/client_golang/prometheus"
)

const (
	moscQueueName = "mosc"
	mosbQueueName = "mosb"
	mcpQueueName  = "mcp"
	jobQueueName  = "job"
)

// OSBuildController is the key-based, multi-queue controller for
// On-Cluster Layering builds.
//
// Design:
//   - Four name-keyed workqueues (one per resource type) instead of one closure queue.
//   - Informer handlers enqueue string keys via cache.MetaNamespaceKeyFunc.
//   - MCP DeleteFunc is wired (the old controller was missing it).
//   - Workers dispatch to a key-based Reconciler interface.
type OSBuildController struct {
	moscQueue workqueue.TypedRateLimitingInterface[string]
	mosbQueue workqueue.TypedRateLimitingInterface[string]
	mcpQueue  workqueue.TypedRateLimitingInterface[string]
	jobQueue  workqueue.TypedRateLimitingInterface[string]

	reconciler reconcile.Reconciler

	informers *informers
	listers   *listers

	config Config

	shutdownDelayHandler *shutdownDelayHandler
	shutdownChan         chan struct{}

	clock clockutil.Clock
}

// ControllerOption is a functional option for OSBuildController.
type ControllerOption func(*OSBuildController)

// WithClock overrides the default real clock (useful for testing).
func WithClock(c clockutil.Clock) ControllerOption {
	return func(ctrl *OSBuildController) {
		ctrl.clock = c
	}
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

// newStringQueue creates a typed rate-limiting workqueue for string keys.
func newStringQueue(name string) workqueue.TypedRateLimitingInterface[string] {
	rl := workqueue.DefaultTypedControllerRateLimiter[string]()
	cfg := workqueue.TypedRateLimitingQueueConfig[string]{Name: name}
	return workqueue.NewTypedRateLimitingQueueWithConfig[string](rl, cfg)
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
		imagepruner.NewImagePruner(),
	)
}

func newOSBuildController(
	ctrlConfig Config,
	mcfgclient mcfgclientset.Interface,
	kubeclient clientset.Interface,
	pruner imagepruner.ImagePruner,
) *OSBuildController {
	return newOSBuildControllerWithServices(ctrlConfig, mcfgclient, kubeclient, pruner, nil, nil)
}

// newOSBuildControllerWithServices constructs the controller with optional
// service overrides.  When reuseChecker or seeder is nil the production
// implementation is used.  This keeps newOSBuildController unchanged
// while allowing tests to inject fakes.
func newOSBuildControllerWithServices(
	ctrlConfig Config,
	mcfgclient mcfgclientset.Interface,
	kubeclient clientset.Interface,
	pruner imagepruner.ImagePruner,
	reuseChecker services.ImageReuseChecker,
	seeder services.Seeder,
) *OSBuildController {
	eventBroadcaster := record.NewBroadcaster()
	eventBroadcaster.StartLogging(klog.Infof)
	eventBroadcaster.StartRecordingToSink(&coreclientsetv1.EventSinkImpl{Interface: kubeclient.CoreV1().Events("")})

	inf := newInformers(mcfgclient, kubeclient)
	l := inf.listers()

	// Build event recorder.
	eventRecorder := eventBroadcaster.NewRecorder(scheme.Scheme, corev1.EventSource{Component: "machineosbuilder"})
	events := services.NewEventRecorder(eventRecorder)

	// Build metrics recorder -- no-op at construction; RegisterOCLMetrics registers the real one.
	metrics := services.NewNoopMetricsRecorder()

	// Construct services, using injected overrides when present.
	degraded := services.NewDegradedHandler(mcfgclient, l.machineOSBuildLister)
	if reuseChecker == nil {
		reuseChecker = services.NewImageReuseChecker(pruner, kubeclient, l.controllerConfigLister)
	}
	if seeder == nil {
		seeder = services.NewSeeder(mcfgclient, kubeclient, l.machineConfigPoolLister, l.machineConfigLister)
	}

	// Unified data-access and service dependency container.
	deps := reconcile.Deps{
		Accessors:    l.accessors(kubeclient, mcfgclient),
		Events:       events,
		Metrics:      metrics,
		Degraded:     degraded,
		Seeder:       seeder,
		ReuseChecker: reuseChecker,
	}

	// Construct the composite reconciler from the shared deps.
	r := reconcile.NewCompositeReconciler(deps)

	// Construct the controller directly with all fields.
	ctrl := &OSBuildController{
		moscQueue:    newStringQueue(moscQueueName),
		mosbQueue:    newStringQueue(mosbQueueName),
		mcpQueue:     newStringQueue(mcpQueueName),
		jobQueue:     newStringQueue(jobQueueName),
		reconciler:   r,
		informers:    inf,
		listers:      l,
		config:       ctrlConfig,
		shutdownChan: make(chan struct{}),
		clock:        clockutil.RealClock{},
	}

	ctrl.shutdownDelayHandler = &shutdownDelayHandler{
		listers: l,
		clock:   ctrl.clock,
	}

	// Wire informer event handlers when informers are provided.
	if inf != nil && inf.machineOSConfigInformer != nil {
		ctrl.addInformerHandlers()
	}

	return ctrl
}

// addInformerHandlers registers Add/Update/Delete handlers on every watched informer.
func (ctrl *OSBuildController) addInformerHandlers() {
	// MachineOSConfig
	ctrl.informers.machineOSConfigInformer.Informer().AddEventHandler(cache.ResourceEventHandlerFuncs{
		AddFunc:    ctrl.enqueueMOSC,
		UpdateFunc: func(_, newObj interface{}) { ctrl.enqueueMOSC(newObj) },
		DeleteFunc: ctrl.handleDeleteMOSC,
	})

	// MachineOSBuild
	ctrl.informers.machineOSBuildInformer.Informer().AddEventHandler(cache.ResourceEventHandlerFuncs{
		AddFunc:    ctrl.enqueueMOSB,
		UpdateFunc: func(_, newObj interface{}) { ctrl.enqueueMOSB(newObj) },
		DeleteFunc: ctrl.handleDeleteMOSB,
	})

	// MachineConfigPool -- includes DeleteFunc (missing in old controller)
	ctrl.informers.machineConfigPoolInformer.Informer().AddEventHandler(cache.ResourceEventHandlerFuncs{
		AddFunc:    ctrl.enqueueMCP,
		UpdateFunc: func(_, newObj interface{}) { ctrl.enqueueMCP(newObj) },
		DeleteFunc: ctrl.handleDeleteMCP,
	})

	// Job
	ctrl.informers.jobInformer.Informer().AddEventHandler(cache.ResourceEventHandlerFuncs{
		AddFunc:    ctrl.enqueueJob,
		UpdateFunc: func(_, newObj interface{}) { ctrl.enqueueJob(newObj) },
		DeleteFunc: ctrl.handleDeleteJob,
	})
}

// --- Enqueue helpers ---

func (ctrl *OSBuildController) enqueueMOSC(obj interface{}) {
	key, err := cache.MetaNamespaceKeyFunc(obj)
	if err != nil {
		utilruntime.HandleError(fmt.Errorf("could not get key for MachineOSConfig %+v: %v", obj, err))
		return
	}
	ctrl.moscQueue.Add(key)
}

func (ctrl *OSBuildController) enqueueMOSB(obj interface{}) {
	key, err := cache.MetaNamespaceKeyFunc(obj)
	if err != nil {
		utilruntime.HandleError(fmt.Errorf("could not get key for MachineOSBuild %+v: %v", obj, err))
		return
	}
	ctrl.mosbQueue.Add(key)
}

func (ctrl *OSBuildController) enqueueMCP(obj interface{}) {
	key, err := cache.MetaNamespaceKeyFunc(obj)
	if err != nil {
		utilruntime.HandleError(fmt.Errorf("could not get key for MachineConfigPool %+v: %v", obj, err))
		return
	}
	ctrl.mcpQueue.Add(key)
}

func (ctrl *OSBuildController) enqueueJob(obj interface{}) {
	key, err := cache.MetaNamespaceKeyFunc(obj)
	if err != nil {
		utilruntime.HandleError(fmt.Errorf("could not get key for Job %+v: %v", obj, err))
		return
	}
	ctrl.jobQueue.Add(key)
}

// --- Delete handlers with tombstone support ---

func (ctrl *OSBuildController) handleDeleteMOSC(obj interface{}) {
	key, err := cache.DeletionHandlingMetaNamespaceKeyFunc(obj)
	if err != nil {
		utilruntime.HandleError(fmt.Errorf("could not get key for deleted MachineOSConfig: %v", err))
		return
	}
	ctrl.moscQueue.Add(key)
}

func (ctrl *OSBuildController) handleDeleteMOSB(obj interface{}) {
	key, err := cache.DeletionHandlingMetaNamespaceKeyFunc(obj)
	if err != nil {
		utilruntime.HandleError(fmt.Errorf("could not get key for deleted MachineOSBuild: %v", err))
		return
	}
	ctrl.mosbQueue.Add(key)
}

func (ctrl *OSBuildController) handleDeleteMCP(obj interface{}) {
	key, err := cache.DeletionHandlingMetaNamespaceKeyFunc(obj)
	if err != nil {
		utilruntime.HandleError(fmt.Errorf("could not get key for deleted MachineConfigPool: %v", err))
		return
	}
	ctrl.mcpQueue.Add(key)
}

func (ctrl *OSBuildController) handleDeleteJob(obj interface{}) {
	key, err := cache.DeletionHandlingMetaNamespaceKeyFunc(obj)
	if err != nil {
		utilruntime.HandleError(fmt.Errorf("could not get key for deleted Job: %v", err))
		return
	}
	ctrl.jobQueue.Add(key)
}

// --- Run / Shutdown ---

// Run starts the controller's informers and per-queue worker goroutines.
// It blocks until the parent context is cancelled, then performs a graceful
// shutdown using the same detached-context pattern as the old controller.
func (ctrl *OSBuildController) Run(parentCtx context.Context, workers int) {
	klog.Infof("Starting Controller")

	// Detached context -- we control shutdown timing independently of the
	// parent's cancellation so we can drain gracefully.
	ctrlCtx, ctrlCancel := context.WithCancel(context.Background())
	defer func() {
		klog.Infof("Shutting down Controller")
		ctrl.shutdownController()
		ctrlCancel()
	}()

	ctrl.informers.start(ctrlCtx)

	if !cache.WaitForCacheSync(ctrlCtx.Done(), ctrl.informers.hasSynced...) {
		klog.Errorf("Controller: caches failed to sync")
		return
	}

	// Start workers per queue.
	for i := 0; i < workers; i++ {
		go wait.Until(ctrl.moscWorker(ctrlCtx), time.Second, ctrlCtx.Done())
		go wait.Until(ctrl.mosbWorker(ctrlCtx), time.Second, ctrlCtx.Done())
		go wait.Until(ctrl.mcpWorker(ctrlCtx), time.Second, ctrlCtx.Done())
		go wait.Until(ctrl.jobWorker(ctrlCtx), time.Second, ctrlCtx.Done())
	}

	klog.Infof("Controller started with %d workers per queue", workers)

	// Block until the parent signals shutdown.
	<-parentCtx.Done()
}

// ShutdownChan returns a channel that is closed when the controller
// finishes its graceful shutdown.
func (ctrl *OSBuildController) ShutdownChan() <-chan struct{} {
	return ctrl.shutdownChan
}

// hasSyncedFuncs returns the informer HasSynced functions for use with
// cache.WaitForCacheSync.  This is primarily intended for tests that
// need to wait for informer caches to populate before exercising the
// controller.
func (ctrl *OSBuildController) hasSyncedFuncs() []cache.InformerSynced {
	if ctrl.informers == nil {
		return nil
	}
	return ctrl.informers.hasSynced
}

func (ctrl *OSBuildController) shutdownController() {
	shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), ctrl.config.MaxShutdownDelay)
	defer shutdownCancel()

	klog.Infof("Controller: determining if a shutdown delay up to %s is required", ctrl.config.MaxShutdownDelay)
	if err := ctrl.shutdownDelayHandler.handleShutdown(shutdownCtx, ctrl.config.ShutdownPollInterval); err != nil {
		klog.Warningf("Controller: error during graceful shutdown: %s. Some objects may be orphaned.", err)
	}

	ctrl.moscQueue.ShutDown()
	ctrl.mosbQueue.ShutDown()
	ctrl.mcpQueue.ShutDown()
	ctrl.jobQueue.ShutDown()

	utilruntime.HandleCrash()
	klog.Infof("Controller has shut down")
	close(ctrl.shutdownChan)
}

// --- Workers ---

func (ctrl *OSBuildController) moscWorker(ctx context.Context) func() {
	return func() { ctrl.processQueue(ctx, ctrl.moscQueue, ctrl.reconciler.ReconcileMOSC) }
}

func (ctrl *OSBuildController) mosbWorker(ctx context.Context) func() {
	return func() { ctrl.processQueue(ctx, ctrl.mosbQueue, ctrl.reconciler.ReconcileMOSB) }
}

func (ctrl *OSBuildController) mcpWorker(ctx context.Context) func() {
	return func() { ctrl.processQueue(ctx, ctrl.mcpQueue, ctrl.reconciler.ReconcilePool) }
}

func (ctrl *OSBuildController) jobWorker(ctx context.Context) func() {
	return func() { ctrl.processQueue(ctx, ctrl.jobQueue, ctrl.reconciler.ReconcileJob) }
}

// processQueue dequeues items from the given queue and calls the handler.
// It processes one item and returns -- wait.Until calls it in a loop.
func (ctrl *OSBuildController) processQueue(
	ctx context.Context,
	queue workqueue.TypedRateLimitingInterface[string],
	handler func(context.Context, string) error,
) {
	key, quit := queue.Get()
	if quit {
		return
	}
	defer queue.Done(key)

	if err := handler(ctx, key); err != nil {
		if queue.NumRequeues(key) < ctrl.config.MaxRetries {
			klog.Warningf("Controller: error processing %q (retry %d/%d): %v",
				key, queue.NumRequeues(key)+1, ctrl.config.MaxRetries, err)
			queue.AddRateLimited(key)
			return
		}
		utilruntime.HandleError(fmt.Errorf("Controller: dropping key %q after %d retries: %v",
			key, ctrl.config.MaxRetries, err))
		queue.Forget(key)
		return
	}

	queue.Forget(key)
}

// RegisterOCLMetrics registers all OCL-related Prometheus metrics.
// This preserves the public API signature used by cmd/machine-os-builder/start.go.
func RegisterOCLMetrics() error {
	_, err := services.NewMetricsRecorder(prometheus.DefaultRegisterer)
	return err
}
