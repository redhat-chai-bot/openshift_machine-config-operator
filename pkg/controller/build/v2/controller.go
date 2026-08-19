// Package v2 implements the level-triggered V2 build controller.
package v2

import (
	"context"
	"fmt"
	"time"

	mcfgv1 "github.com/openshift/api/machineconfiguration/v1"
	imagev1clientset "github.com/openshift/client-go/image/clientset/versioned"
	mcfgclientset "github.com/openshift/client-go/machineconfiguration/clientset/versioned"
	"github.com/openshift/client-go/machineconfiguration/clientset/versioned/scheme"
	routeclientset "github.com/openshift/client-go/route/clientset/versioned"
	"github.com/openshift/machine-config-operator/pkg/controller/build/constants"
	"github.com/openshift/machine-config-operator/pkg/controller/build/events"
	"github.com/openshift/machine-config-operator/pkg/controller/build/imagepruner"
	"github.com/openshift/machine-config-operator/pkg/controller/build/shutdown"
	ctrlcommon "github.com/openshift/machine-config-operator/pkg/controller/common"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	"k8s.io/apimachinery/pkg/util/wait"
	clientset "k8s.io/client-go/kubernetes"
	coreclientsetv1 "k8s.io/client-go/kubernetes/typed/core/v1"
	"k8s.io/client-go/tools/cache"
	"k8s.io/client-go/tools/record"
	"k8s.io/client-go/util/workqueue"
	"k8s.io/klog/v2"
	"k8s.io/utils/clock"
)

const (
	// maxRetries is the number of times a key will be retried before it is dropped.
	maxRetries = 15
)

// Config holds configurable parameters for the OSBuildController.
type Config struct {
	// UpdateDelay is a pause to deal with churn in MachineConfigs.
	// Default: 5 seconds
	UpdateDelay time.Duration
	// ShutdownPollInterval is how often to check for pending objects during shutdown.
	// Default: 1 second
	ShutdownPollInterval time.Duration
	// MaxShutdownDelay is the maximum time to wait for pending objects during shutdown.
	// Default: 10 seconds
	MaxShutdownDelay time.Duration
}

// defaultConfig returns a Config with sensible production defaults.
func defaultConfig() Config {
	return Config{
		UpdateDelay:          time.Second * 5,
		ShutdownPollInterval: time.Second,
		MaxShutdownDelay:     time.Second * 10,
	}
}

// OSBuildController implements the V2 level-triggered build controller with
// three separate workqueues: one for MachineOSConfigs, one for MachineOSBuilds,
// and one for MachineConfigPools.
type OSBuildController struct {
	mcfgclient  mcfgclientset.Interface
	kubeclient  clientset.Interface
	imageclient imagev1clientset.Interface
	routeclient routeclientset.Interface

	moscQueue workqueue.TypedRateLimitingInterface[string]
	mosbQueue workqueue.TypedRateLimitingInterface[string]
	mcpQueue  workqueue.TypedRateLimitingInterface[string]

	*listers
	*informers
	eventRecorder *events.OCLEventRecorder
	imagepruner   imagepruner.ImagePruner
	config        Config

	shutdownHandler *shutdown.ShutdownDelayHandler
	shutdownChan    chan struct{}

	// Sync handlers — function pointers for testability.
	syncMOSC func(ctx context.Context, key string) error
	syncMOSB func(ctx context.Context, key string) error
	syncMCP  func(ctx context.Context, key string) error
}

// NewOSBuildControllerFromControllerContext creates a new V2 OSBuildController
// from the given ControllerContext with default configuration.
func NewOSBuildControllerFromControllerContext(ctrlCtx *ctrlcommon.ControllerContext) *OSBuildController {
	return NewOSBuildControllerFromControllerContextWithConfig(ctrlCtx, defaultConfig())
}

// NewOSBuildControllerFromControllerContextWithConfig creates a new V2
// OSBuildController from the given ControllerContext with the provided config.
func NewOSBuildControllerFromControllerContextWithConfig(ctrlCtx *ctrlcommon.ControllerContext, cfg Config) *OSBuildController {
	mcfgclient := ctrlCtx.ClientBuilder.MachineConfigClientOrDie("machine-os-builder")
	kubeclient := ctrlCtx.ClientBuilder.KubeClientOrDie("machine-os-builder")
	imageclient := ctrlCtx.ClientBuilder.ImageClientOrDie("machine-os-builder")
	routeclient := ctrlCtx.ClientBuilder.RouteClientOrDie("machine-os-builder")
	return newOSBuildController(cfg, mcfgclient, kubeclient, imageclient, routeclient, nil)
}

func newOSBuildController(
	cfg Config,
	mcfgclient mcfgclientset.Interface,
	kubeclient clientset.Interface,
	imageclient imagev1clientset.Interface,
	routeclient routeclientset.Interface,
	imgpruner imagepruner.ImagePruner,
) *OSBuildController {
	inf := newInformers(mcfgclient, kubeclient)

	eventBroadcaster := record.NewBroadcaster()
	eventBroadcaster.StartLogging(klog.Infof)
	eventBroadcaster.StartRecordingToSink(&coreclientsetv1.EventSinkImpl{Interface: kubeclient.CoreV1().Events("")})

	ctrl := &OSBuildController{
		mcfgclient:  mcfgclient,
		kubeclient:  kubeclient,
		imageclient: imageclient,
		routeclient: routeclient,
		informers:   inf,
		listers:     inf.listers(),
		config:      cfg,
		imagepruner: imgpruner,
		eventRecorder: events.NewOCLEventRecorder(
			eventBroadcaster.NewRecorder(scheme.Scheme, corev1.EventSource{Component: "machineosbuilder-v2"}),
		),
		moscQueue: workqueue.NewTypedRateLimitingQueueWithConfig(
			workqueue.DefaultTypedControllerRateLimiter[string](),
			workqueue.TypedRateLimitingQueueConfig[string]{Name: "machineosbuilder-mosc"},
		),
		mosbQueue: workqueue.NewTypedRateLimitingQueueWithConfig(
			workqueue.DefaultTypedControllerRateLimiter[string](),
			workqueue.TypedRateLimitingQueueConfig[string]{Name: "machineosbuilder-mosb"},
		),
		mcpQueue: workqueue.NewTypedRateLimitingQueueWithConfig(
			workqueue.DefaultTypedControllerRateLimiter[string](),
			workqueue.TypedRateLimitingQueueConfig[string]{Name: "machineosbuilder-mcp"},
		),
		shutdownChan: make(chan struct{}),
	}

	// Create reconcilers and wire them as the default sync handlers.
	statusMgr := NewMCPStatusManager(mcfgclient, ctrl.listers)
	seeder := NewSeedManager(mcfgclient, kubeclient, ctrl.listers)

	moscRec := newMOSCReconciler(mcfgclient, kubeclient, ctrl.listers, statusMgr, seeder, ctrl.eventRecorder)
	mosbRec := newMOSBReconciler(mcfgclient, kubeclient, ctrl.listers, statusMgr, ctrl.eventRecorder)
	mcpRec := newMCPReconciler(mcfgclient, kubeclient, ctrl.listers, statusMgr, ctrl.eventRecorder, ctrl.moscQueue)

	ctrl.syncMOSC = moscRec.Sync
	ctrl.syncMOSB = mosbRec.Sync
	ctrl.syncMCP = mcpRec.Sync

	// Register event handlers.
	inf.machineOSConfigInformer.Informer().AddEventHandler(cache.ResourceEventHandlerFuncs{
		AddFunc:    ctrl.addMachineOSConfig,
		UpdateFunc: ctrl.updateMachineOSConfig,
		DeleteFunc: ctrl.deleteMachineOSConfig,
	})

	inf.machineOSBuildInformer.Informer().AddEventHandler(cache.ResourceEventHandlerFuncs{
		AddFunc:    ctrl.addMachineOSBuild,
		UpdateFunc: ctrl.updateMachineOSBuild,
		DeleteFunc: ctrl.deleteMachineOSBuild,
	})

	inf.jobInformer.Informer().AddEventHandler(cache.ResourceEventHandlerFuncs{
		AddFunc:    ctrl.addJob,
		UpdateFunc: ctrl.updateJob,
		DeleteFunc: ctrl.deleteJob,
	})

	inf.machineConfigPoolInformer.Informer().AddEventHandler(cache.ResourceEventHandlerFuncs{
		AddFunc:    ctrl.addMachineConfigPool,
		UpdateFunc: ctrl.updateMachineConfigPool,
	})

	// Set up shutdown handler.
	ctrl.shutdownHandler = shutdown.NewShutdownDelayHandler(shutdown.Listers{
		MachineOSConfigLister: ctrl.machineOSConfigLister,
		MachineOSBuildLister:  ctrl.machineOSBuildLister,
		JobLister:             ctrl.jobLister,
		ConfigMapLister:       ctrl.configmapLister,
		SecretLister:          ctrl.secretLister,
	}, clock.RealClock{})

	return ctrl
}

// ShutdownChan returns a channel that is closed when the controller shutdown is complete.
func (ctrl *OSBuildController) ShutdownChan() <-chan struct{} {
	return ctrl.shutdownChan
}

// Run starts the controller workers and blocks until the context is cancelled.
func (ctrl *OSBuildController) Run(ctx context.Context, workers int) {
	defer utilruntime.HandleCrash()
	defer ctrl.shutdownController()

	ctrl.start(ctx)

	klog.Info("Waiting for informer caches to sync for OSBuildController-v2")
	if !cache.WaitForCacheSync(ctx.Done(), ctrl.hasSynced...) {
		klog.Error("Failed to sync informer caches for OSBuildController-v2")
		return
	}

	klog.Info("Starting OSBuildController-v2")
	defer klog.Info("Shutting down OSBuildController-v2")

	for i := 0; i < workers; i++ {
		go wait.UntilWithContext(ctx, ctrl.moscWorker, time.Second)
		go wait.UntilWithContext(ctx, ctrl.mosbWorker, time.Second)
		go wait.UntilWithContext(ctx, ctrl.mcpWorker, time.Second)
	}

	<-ctx.Done()
}

func (ctrl *OSBuildController) shutdownController() {
	defer close(ctrl.shutdownChan)

	shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), ctrl.config.MaxShutdownDelay)
	defer shutdownCancel()

	klog.Infof("Determining if a shutdown delay up to %s is required", ctrl.config.MaxShutdownDelay)

	if err := ctrl.shutdownHandler.HandleShutdown(shutdownCtx, ctrl.config.ShutdownPollInterval); err != nil {
		klog.Warningf("Error occurred during graceful shutdown: %s. Some objects may be orphaned as a result.", err)
	}

	ctrl.moscQueue.ShutDown()
	ctrl.mosbQueue.ShutDown()
	ctrl.mcpQueue.ShutDown()
}

// --- Event handlers: thin key-extraction + enqueue ---

func (ctrl *OSBuildController) addMachineOSConfig(obj interface{}) {
	mosc := obj.(*mcfgv1.MachineOSConfig)
	klog.V(4).Infof("Adding MachineOSConfig %s", mosc.Name)
	ctrl.moscQueue.Add(mosc.Name)
}

func (ctrl *OSBuildController) updateMachineOSConfig(_, cur interface{}) {
	mosc := cur.(*mcfgv1.MachineOSConfig)
	klog.V(4).Infof("Updating MachineOSConfig %s", mosc.Name)
	ctrl.moscQueue.Add(mosc.Name)
}

func (ctrl *OSBuildController) deleteMachineOSConfig(obj interface{}) {
	mosc, ok := obj.(*mcfgv1.MachineOSConfig)
	if !ok {
		tombstone, ok := obj.(cache.DeletedFinalStateUnknown)
		if !ok {
			utilruntime.HandleError(fmt.Errorf("couldn't get object from tombstone %#v", obj))
			return
		}
		mosc, ok = tombstone.Obj.(*mcfgv1.MachineOSConfig)
		if !ok {
			utilruntime.HandleError(fmt.Errorf("tombstone contained object that is not a MachineOSConfig %#v", obj))
			return
		}
	}
	klog.V(4).Infof("Deleting MachineOSConfig %s", mosc.Name)
	ctrl.moscQueue.Add(mosc.Name)
}

func (ctrl *OSBuildController) addMachineOSBuild(obj interface{}) {
	mosb := obj.(*mcfgv1.MachineOSBuild)
	klog.V(4).Infof("Adding MachineOSBuild %s", mosb.Name)
	ctrl.mosbQueue.Add(mosb.Name)
}

func (ctrl *OSBuildController) updateMachineOSBuild(_, cur interface{}) {
	mosb := cur.(*mcfgv1.MachineOSBuild)
	klog.V(4).Infof("Updating MachineOSBuild %s", mosb.Name)
	ctrl.mosbQueue.Add(mosb.Name)
}

func (ctrl *OSBuildController) deleteMachineOSBuild(obj interface{}) {
	mosb, ok := obj.(*mcfgv1.MachineOSBuild)
	if !ok {
		tombstone, ok := obj.(cache.DeletedFinalStateUnknown)
		if !ok {
			utilruntime.HandleError(fmt.Errorf("couldn't get object from tombstone %#v", obj))
			return
		}
		mosb, ok = tombstone.Obj.(*mcfgv1.MachineOSBuild)
		if !ok {
			utilruntime.HandleError(fmt.Errorf("tombstone contained object that is not a MachineOSBuild %#v", obj))
			return
		}
	}
	klog.V(4).Infof("Deleting MachineOSBuild %s", mosb.Name)
	ctrl.mosbQueue.Add(mosb.Name)
}

func (ctrl *OSBuildController) addJob(obj interface{}) {
	job := obj.(*batchv1.Job)
	ctrl.enqueueJobAsMOSB(job)
}

func (ctrl *OSBuildController) updateJob(_, cur interface{}) {
	job := cur.(*batchv1.Job)
	ctrl.enqueueJobAsMOSB(job)
}

func (ctrl *OSBuildController) deleteJob(obj interface{}) {
	job, ok := obj.(*batchv1.Job)
	if !ok {
		tombstone, ok := obj.(cache.DeletedFinalStateUnknown)
		if !ok {
			utilruntime.HandleError(fmt.Errorf("couldn't get object from tombstone %#v", obj))
			return
		}
		job, ok = tombstone.Obj.(*batchv1.Job)
		if !ok {
			utilruntime.HandleError(fmt.Errorf("tombstone contained object that is not a Job %#v", obj))
			return
		}
	}
	ctrl.enqueueJobAsMOSB(job)
}

// enqueueJobAsMOSB extracts the MachineOSBuild name from the Job's labels
// and enqueues it into the MOSB queue.
func (ctrl *OSBuildController) enqueueJobAsMOSB(job *batchv1.Job) {
	mosbName, ok := job.Labels[constants.MachineOSBuildNameLabelKey]
	if !ok || mosbName == "" {
		return
	}
	klog.V(4).Infof("Job %s maps to MachineOSBuild %s", job.Name, mosbName)
	ctrl.mosbQueue.Add(mosbName)
}

func (ctrl *OSBuildController) addMachineConfigPool(obj interface{}) {
	mcp := obj.(*mcfgv1.MachineConfigPool)
	klog.V(4).Infof("Adding MachineConfigPool %s", mcp.Name)
	ctrl.mcpQueue.Add(mcp.Name)
}

func (ctrl *OSBuildController) updateMachineConfigPool(_, cur interface{}) {
	mcp := cur.(*mcfgv1.MachineConfigPool)
	klog.V(4).Infof("Updating MachineConfigPool %s", mcp.Name)
	ctrl.mcpQueue.Add(mcp.Name)
}

// --- Workers: standard processNextWorkItem pattern ---

func (ctrl *OSBuildController) moscWorker(ctx context.Context) {
	for ctrl.processNextWorkItem(ctx, ctrl.moscQueue, ctrl.syncMOSC, "MachineOSConfig") {
	}
}

func (ctrl *OSBuildController) mosbWorker(ctx context.Context) {
	for ctrl.processNextWorkItem(ctx, ctrl.mosbQueue, ctrl.syncMOSB, "MachineOSBuild") {
	}
}

func (ctrl *OSBuildController) mcpWorker(ctx context.Context) {
	for ctrl.processNextWorkItem(ctx, ctrl.mcpQueue, ctrl.syncMCP, "MachineConfigPool") {
	}
}

func (ctrl *OSBuildController) processNextWorkItem(
	ctx context.Context,
	queue workqueue.TypedRateLimitingInterface[string],
	syncFn func(ctx context.Context, key string) error,
	kind string,
) bool {
	key, quit := queue.Get()
	if quit {
		return false
	}
	defer queue.Done(key)

	err := syncFn(ctx, key)
	ctrl.handleErr(err, key, queue, kind)
	return true
}

func (ctrl *OSBuildController) handleErr(
	err error,
	key string,
	queue workqueue.TypedRateLimitingInterface[string],
	kind string,
) {
	if err == nil {
		queue.Forget(key)
		return
	}

	if queue.NumRequeues(key) < maxRetries {
		klog.V(2).Infof("Error syncing %s %q: %v", kind, key, err)
		queue.AddRateLimited(key)
		return
	}

	utilruntime.HandleError(err)
	klog.V(2).Infof("Dropping %s %q out of the queue: %v", kind, key, err)
	queue.Forget(key)
	queue.AddAfter(key, 1*time.Minute)
}

// --- Default sync stubs (will be replaced by real reconcilers in later phases) ---
