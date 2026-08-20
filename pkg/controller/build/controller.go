package build

import (
	"context"
	"fmt"
	"time"

	"github.com/openshift/machine-config-operator/pkg/controller/build/reconcile"

	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/client-go/tools/cache"
	"k8s.io/client-go/util/workqueue"
	"k8s.io/klog/v2"
	clockutil "k8s.io/utils/clock"
)

const (
	moscQueueName = "mosc"
	mosbQueueName = "mosb"
	mcpQueueName  = "mcp"
	jobQueueName  = "job"
)

// BuildController is the new key-based, multi-queue controller shell for
// On-Cluster Layering. It runs alongside the existing OSBuildController until
// the WS6 cutover, when it will replace it.
//
// Differences from the old controller:
//   - Four name-keyed workqueues (one per resource type) instead of one closure queue.
//   - Informer handlers enqueue string keys via cache.MetaNamespaceKeyFunc.
//   - MCP DeleteFunc is wired (the old controller was missing it).
//   - Workers dispatch to a key-based Reconciler interface.
type BuildController struct {
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

// BuildControllerOption is a functional option for BuildController.
type BuildControllerOption func(*BuildController)

// WithClock overrides the default real clock (useful for testing).
func WithClock(c clockutil.Clock) BuildControllerOption {
	return func(bc *BuildController) {
		bc.clock = c
	}
}

// newStringQueue creates a typed rate-limiting workqueue for string keys.
func newStringQueue(name string) workqueue.TypedRateLimitingInterface[string] {
	rl := workqueue.DefaultTypedControllerRateLimiter[string]()
	cfg := workqueue.TypedRateLimitingQueueConfig[string]{Name: name}
	return workqueue.NewTypedRateLimitingQueueWithConfig[string](rl, cfg)
}

// NewBuildController constructs a BuildController and wires informer event
// handlers. The controller is inert until Run() is called.
func NewBuildController(
	r reconcile.Reconciler,
	inf *informers,
	l *listers,
	cfg Config,
	opts ...BuildControllerOption,
) *BuildController {
	bc := &BuildController{
		moscQueue:    newStringQueue(moscQueueName),
		mosbQueue:    newStringQueue(mosbQueueName),
		mcpQueue:     newStringQueue(mcpQueueName),
		jobQueue:     newStringQueue(jobQueueName),
		reconciler:   r,
		informers:    inf,
		listers:      l,
		config:       cfg,
		shutdownChan: make(chan struct{}),
		clock:        clockutil.RealClock{},
	}

	for _, o := range opts {
		o(bc)
	}

	bc.shutdownDelayHandler = &shutdownDelayHandler{
		listers: l,
		clock:   bc.clock,
	}

	// Wire informer event handlers when informers are provided.
	if inf != nil && inf.machineOSConfigInformer != nil {
		bc.addInformerHandlers()
	}

	return bc
}

// addInformerHandlers registers Add/Update/Delete handlers on every watched informer.
func (bc *BuildController) addInformerHandlers() {
	// MachineOSConfig
	bc.informers.machineOSConfigInformer.Informer().AddEventHandler(cache.ResourceEventHandlerFuncs{
		AddFunc:    bc.enqueueMOSC,
		UpdateFunc: func(_, newObj interface{}) { bc.enqueueMOSC(newObj) },
		DeleteFunc: bc.handleDeleteMOSC,
	})

	// MachineOSBuild
	bc.informers.machineOSBuildInformer.Informer().AddEventHandler(cache.ResourceEventHandlerFuncs{
		AddFunc:    bc.enqueueMOSB,
		UpdateFunc: func(_, newObj interface{}) { bc.enqueueMOSB(newObj) },
		DeleteFunc: bc.handleDeleteMOSB,
	})

	// MachineConfigPool — includes DeleteFunc (missing in old controller)
	bc.informers.machineConfigPoolInformer.Informer().AddEventHandler(cache.ResourceEventHandlerFuncs{
		AddFunc:    bc.enqueueMCP,
		UpdateFunc: func(_, newObj interface{}) { bc.enqueueMCP(newObj) },
		DeleteFunc: bc.handleDeleteMCP,
	})

	// Job
	bc.informers.jobInformer.Informer().AddEventHandler(cache.ResourceEventHandlerFuncs{
		AddFunc:    bc.enqueueJob,
		UpdateFunc: func(_, newObj interface{}) { bc.enqueueJob(newObj) },
		DeleteFunc: bc.handleDeleteJob,
	})
}

// --- Enqueue helpers ---

func (bc *BuildController) enqueueMOSC(obj interface{}) {
	key, err := cache.MetaNamespaceKeyFunc(obj)
	if err != nil {
		utilruntime.HandleError(fmt.Errorf("could not get key for MachineOSConfig %+v: %v", obj, err))
		return
	}
	bc.moscQueue.Add(key)
}

func (bc *BuildController) enqueueMOSB(obj interface{}) {
	key, err := cache.MetaNamespaceKeyFunc(obj)
	if err != nil {
		utilruntime.HandleError(fmt.Errorf("could not get key for MachineOSBuild %+v: %v", obj, err))
		return
	}
	bc.mosbQueue.Add(key)
}

func (bc *BuildController) enqueueMCP(obj interface{}) {
	key, err := cache.MetaNamespaceKeyFunc(obj)
	if err != nil {
		utilruntime.HandleError(fmt.Errorf("could not get key for MachineConfigPool %+v: %v", obj, err))
		return
	}
	bc.mcpQueue.Add(key)
}

func (bc *BuildController) enqueueJob(obj interface{}) {
	key, err := cache.MetaNamespaceKeyFunc(obj)
	if err != nil {
		utilruntime.HandleError(fmt.Errorf("could not get key for Job %+v: %v", obj, err))
		return
	}
	bc.jobQueue.Add(key)
}

// --- Delete handlers with tombstone support ---

func (bc *BuildController) handleDeleteMOSC(obj interface{}) {
	key, err := cache.DeletionHandlingMetaNamespaceKeyFunc(obj)
	if err != nil {
		utilruntime.HandleError(fmt.Errorf("could not get key for deleted MachineOSConfig: %v", err))
		return
	}
	bc.moscQueue.Add(key)
}

func (bc *BuildController) handleDeleteMOSB(obj interface{}) {
	key, err := cache.DeletionHandlingMetaNamespaceKeyFunc(obj)
	if err != nil {
		utilruntime.HandleError(fmt.Errorf("could not get key for deleted MachineOSBuild: %v", err))
		return
	}
	bc.mosbQueue.Add(key)
}

func (bc *BuildController) handleDeleteMCP(obj interface{}) {
	key, err := cache.DeletionHandlingMetaNamespaceKeyFunc(obj)
	if err != nil {
		utilruntime.HandleError(fmt.Errorf("could not get key for deleted MachineConfigPool: %v", err))
		return
	}
	bc.mcpQueue.Add(key)
}

func (bc *BuildController) handleDeleteJob(obj interface{}) {
	key, err := cache.DeletionHandlingMetaNamespaceKeyFunc(obj)
	if err != nil {
		utilruntime.HandleError(fmt.Errorf("could not get key for deleted Job: %v", err))
		return
	}
	bc.jobQueue.Add(key)
}

// --- Run / Shutdown ---

// Run starts the controller's informers and per-queue worker goroutines.
// It blocks until the parent context is cancelled, then performs a graceful
// shutdown using the same detached-context pattern as the old controller.
func (bc *BuildController) Run(parentCtx context.Context, workers int) {
	klog.Infof("Starting BuildController")

	// Detached context — we control shutdown timing independently of the
	// parent's cancellation so we can drain gracefully.
	ctrlCtx, ctrlCancel := context.WithCancel(context.Background())
	defer func() {
		klog.Infof("Shutting down BuildController")
		bc.shutdownController()
		ctrlCancel()
	}()

	bc.informers.start(ctrlCtx)

	if !cache.WaitForCacheSync(ctrlCtx.Done(), bc.informers.hasSynced...) {
		klog.Errorf("BuildController: caches failed to sync")
		return
	}

	// Start workers per queue.
	for i := 0; i < workers; i++ {
		go wait.Until(bc.moscWorker(ctrlCtx), time.Second, ctrlCtx.Done())
		go wait.Until(bc.mosbWorker(ctrlCtx), time.Second, ctrlCtx.Done())
		go wait.Until(bc.mcpWorker(ctrlCtx), time.Second, ctrlCtx.Done())
		go wait.Until(bc.jobWorker(ctrlCtx), time.Second, ctrlCtx.Done())
	}

	klog.Infof("BuildController started with %d workers per queue", workers)

	// Block until the parent signals shutdown.
	<-parentCtx.Done()
}

// ShutdownChan returns a channel that is closed when the controller
// finishes its graceful shutdown.
func (bc *BuildController) ShutdownChan() <-chan struct{} {
	return bc.shutdownChan
}

func (bc *BuildController) shutdownController() {
	shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), bc.config.MaxShutdownDelay)
	defer shutdownCancel()

	klog.Infof("BuildController: determining if a shutdown delay up to %s is required", bc.config.MaxShutdownDelay)
	if err := bc.shutdownDelayHandler.handleShutdown(shutdownCtx, bc.config.ShutdownPollInterval); err != nil {
		klog.Warningf("BuildController: error during graceful shutdown: %s. Some objects may be orphaned.", err)
	}

	bc.moscQueue.ShutDown()
	bc.mosbQueue.ShutDown()
	bc.mcpQueue.ShutDown()
	bc.jobQueue.ShutDown()

	utilruntime.HandleCrash()
	klog.Infof("BuildController has shut down")
	close(bc.shutdownChan)
}

// --- Workers ---

func (bc *BuildController) moscWorker(ctx context.Context) func() {
	return func() { bc.processQueue(ctx, bc.moscQueue, bc.reconciler.ReconcileMOSC) }
}

func (bc *BuildController) mosbWorker(ctx context.Context) func() {
	return func() { bc.processQueue(ctx, bc.mosbQueue, bc.reconciler.ReconcileMOSB) }
}

func (bc *BuildController) mcpWorker(ctx context.Context) func() {
	return func() { bc.processQueue(ctx, bc.mcpQueue, bc.reconciler.ReconcilePool) }
}

func (bc *BuildController) jobWorker(ctx context.Context) func() {
	return func() { bc.processQueue(ctx, bc.jobQueue, bc.reconciler.ReconcileJob) }
}

// processQueue dequeues items from the given queue and calls the handler.
// It processes one item and returns — wait.Until calls it in a loop.
func (bc *BuildController) processQueue(
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
		if queue.NumRequeues(key) < bc.config.MaxRetries {
			klog.Warningf("BuildController: error processing %q (retry %d/%d): %v",
				key, queue.NumRequeues(key)+1, bc.config.MaxRetries, err)
			queue.AddRateLimited(key)
			return
		}
		utilruntime.HandleError(fmt.Errorf("BuildController: dropping key %q after %d retries: %v",
			key, bc.config.MaxRetries, err))
		queue.Forget(key)
		return
	}

	queue.Forget(key)
}
