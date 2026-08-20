package services

import (
	"fmt"
	"sync"
	"time"

	"github.com/prometheus/client_golang/prometheus"
)

// Build state constants for consistent metric labeling.
const (
	StateNone        = "none"
	StatePending     = "pending"
	StateBuilding    = "building"
	StateSucceeded   = "succeeded"
	StateFailed      = "failed"
	StateInterrupted = "interrupted"
	StatePushing     = "pushing"
)

// MetricsRecorder defines the interface for recording OCL metrics.
type MetricsRecorder interface {
	RecordBuildStarted(pool string)
	RecordBuildBuilding(pool string)
	RecordBuildCompleted(pool string, startTime time.Time)
	RecordBuildFailed(pool string, startTime time.Time)
	RecordBuildInterrupted(pool string)
	RecordBuildJobState(pool, state string)
	RecordConfigChange(pool string)
	RecordBuildRetry(pool string)
	UpdateLayeredNodesCount(pool string, count int)
	RecordImagePushStarted(pool string)
	RecordImagePushCompleted(pool string)
	RecordImagePushFailed(pool string)
	RecordBuildQueueDuration(pool string, queuedAt time.Time)
	UpdateOCLRolloutCounts(pool string, updatedNodes, totalNodes int32)
	UpdateMOSCCount(count float64)
}

// metricsRecorder implements MetricsRecorder with injected prometheus.Registerer.
// All metric collectors are struct fields — no global mutable state.
type metricsRecorder struct {
	buildState        *prometheus.GaugeVec
	buildDuration     *prometheus.HistogramVec
	buildStartTime    *prometheus.GaugeVec
	buildEndTime      *prometheus.GaugeVec
	buildTotal        *prometheus.CounterVec
	buildJobState     *prometheus.GaugeVec
	configChangeTotal *prometheus.CounterVec
	buildRetries      *prometheus.CounterVec
	layeredNodesCount *prometheus.GaugeVec
	imagePushState    *prometheus.GaugeVec
	imagePushTotal    *prometheus.CounterVec
	rolloutUpdated    *prometheus.GaugeVec
	rolloutTotal      *prometheus.GaugeVec
	buildQueueDur     *prometheus.HistogramVec
	imagePushDur      *prometheus.HistogramVec
	activeBuilds      *prometheus.GaugeVec
	moscCount         prometheus.Gauge

	// pushStartTimes replaces the former global sync.Map.
	pushStartTimes sync.Map
}

// NewMetricsRecorder creates a MetricsRecorder and registers all collectors
// on the provided registerer. Returns an error if registration fails.
func NewMetricsRecorder(reg prometheus.Registerer) (MetricsRecorder, error) {
	m := &metricsRecorder{
		buildState: prometheus.NewGaugeVec(prometheus.GaugeOpts{
			Name: "ocl_build_state",
			Help: "Current state of OCL build for a pool; gauge is 1 for the active state label, 0 otherwise",
		}, []string{"pool", "state"}),

		buildDuration: prometheus.NewHistogramVec(prometheus.HistogramOpts{
			Name:    "ocl_build_duration_seconds",
			Help:    "Duration of OCL build processes in seconds",
			Buckets: []float64{60, 180, 300, 600, 900, 1200, 1800, 2400, 3000, 3600},
		}, []string{"pool", "state"}),

		buildStartTime: prometheus.NewGaugeVec(prometheus.GaugeOpts{
			Name: "ocl_build_start_timestamp_seconds",
			Help: "Timestamp when OCL build started",
		}, []string{"pool"}),

		buildEndTime: prometheus.NewGaugeVec(prometheus.GaugeOpts{
			Name: "ocl_build_end_timestamp_seconds",
			Help: "Timestamp when OCL build completed or failed",
		}, []string{"pool", "state"}),

		buildTotal: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "ocl_build_total",
			Help: "Total number of OCL builds by final state",
		}, []string{"pool", "state"}),

		buildJobState: prometheus.NewGaugeVec(prometheus.GaugeOpts{
			Name: "ocl_build_job_state",
			Help: "State of OCL build job; gauge is 1 for the active state label, 0 otherwise",
		}, []string{"pool", "state"}),

		configChangeTotal: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "ocl_config_change_total",
			Help: "Total number of config changes triggering OCL builds",
		}, []string{"pool"}),

		buildRetries: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "ocl_build_retries_total",
			Help: "Total number of OCL build retry attempts",
		}, []string{"pool"}),

		layeredNodesCount: prometheus.NewGaugeVec(prometheus.GaugeOpts{
			Name: "ocl_layered_nodes_count",
			Help: "Number of nodes currently using OCL layered images",
		}, []string{"pool"}),

		imagePushState: prometheus.NewGaugeVec(prometheus.GaugeOpts{
			Name: "ocl_image_push_state",
			Help: "State of OCL image push operations; gauge is 1 for the active state label, 0 otherwise",
		}, []string{"pool", "state"}),

		imagePushTotal: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "ocl_image_push_total",
			Help: "Total number of OCL image push operations by final state",
		}, []string{"pool", "state"}),

		rolloutUpdated: prometheus.NewGaugeVec(prometheus.GaugeOpts{
			Name: "ocl_rollout_updated_nodes",
			Help: "Number of nodes in an OCL pool that have adopted the current layered image",
		}, []string{"pool"}),

		rolloutTotal: prometheus.NewGaugeVec(prometheus.GaugeOpts{
			Name: "ocl_rollout_total_nodes",
			Help: "Total number of nodes in an OCL pool",
		}, []string{"pool"}),

		buildQueueDur: prometheus.NewHistogramVec(prometheus.HistogramOpts{
			Name:    "ocl_build_queue_duration_seconds",
			Help:    "Time in seconds between OCL build creation and the build job becoming active",
			Buckets: []float64{5, 15, 30, 60, 120, 300, 600},
		}, []string{"pool"}),

		imagePushDur: prometheus.NewHistogramVec(prometheus.HistogramOpts{
			Name:    "ocl_image_push_duration_seconds",
			Help:    "Duration of OCL image push operations in seconds",
			Buckets: []float64{10, 30, 60, 120, 180, 300, 600},
		}, []string{"pool", "state"}),

		activeBuilds: prometheus.NewGaugeVec(prometheus.GaugeOpts{
			Name: "ocl_active_builds",
			Help: "Number of OCL builds currently in progress per pool",
		}, []string{"pool"}),

		moscCount: prometheus.NewGauge(prometheus.GaugeOpts{
			Name: "mco_mosc_count",
			Help: "number of MachineOSConfig objects in the cluster; non-zero value indicates on-cluster layering is in use",
		}),
	}

	collectors := []prometheus.Collector{
		m.buildState, m.buildDuration, m.buildStartTime, m.buildEndTime,
		m.buildTotal, m.buildJobState, m.configChangeTotal, m.buildRetries,
		m.layeredNodesCount, m.imagePushState, m.imagePushTotal,
		m.rolloutUpdated, m.rolloutTotal, m.buildQueueDur, m.imagePushDur,
		m.activeBuilds, m.moscCount,
	}

	for _, c := range collectors {
		if err := reg.Register(c); err != nil {
			return nil, fmt.Errorf("could not register OCL metric: %w", err)
		}
	}

	return m, nil
}

func (m *metricsRecorder) RecordBuildStarted(pool string) {
	now := float64(time.Now().Unix())
	m.buildState.DeletePartialMatch(prometheus.Labels{"pool": pool})
	m.buildState.WithLabelValues(pool, StatePending).Set(1)
	m.buildStartTime.WithLabelValues(pool).Set(now)
	m.activeBuilds.WithLabelValues(pool).Inc()
}

func (m *metricsRecorder) RecordBuildBuilding(pool string) {
	m.buildState.DeletePartialMatch(prometheus.Labels{"pool": pool})
	m.buildState.WithLabelValues(pool, StateBuilding).Set(1)
}

func (m *metricsRecorder) RecordBuildCompleted(pool string, startTime time.Time) {
	now := time.Now()
	duration := now.Sub(startTime).Seconds()
	m.buildState.DeletePartialMatch(prometheus.Labels{"pool": pool})
	m.pushStartTimes.Delete(pool)
	m.buildState.WithLabelValues(pool, StateSucceeded).Set(1)
	m.buildEndTime.WithLabelValues(pool, StateSucceeded).Set(float64(now.Unix()))
	m.buildDuration.WithLabelValues(pool, StateSucceeded).Observe(duration)
	m.buildTotal.WithLabelValues(pool, StateSucceeded).Inc()
	m.activeBuilds.WithLabelValues(pool).Dec()
}

func (m *metricsRecorder) RecordBuildFailed(pool string, startTime time.Time) {
	now := time.Now()
	duration := now.Sub(startTime).Seconds()
	m.buildState.DeletePartialMatch(prometheus.Labels{"pool": pool})
	m.pushStartTimes.Delete(pool)
	m.buildState.WithLabelValues(pool, StateFailed).Set(1)
	m.buildEndTime.WithLabelValues(pool, StateFailed).Set(float64(now.Unix()))
	m.buildDuration.WithLabelValues(pool, StateFailed).Observe(duration)
	m.buildTotal.WithLabelValues(pool, StateFailed).Inc()
	m.activeBuilds.WithLabelValues(pool).Dec()
}

func (m *metricsRecorder) RecordBuildInterrupted(pool string) {
	m.buildState.DeletePartialMatch(prometheus.Labels{"pool": pool})
	m.pushStartTimes.Delete(pool)
	m.buildState.WithLabelValues(pool, StateInterrupted).Set(1)
	m.buildEndTime.WithLabelValues(pool, StateInterrupted).Set(float64(time.Now().Unix()))
	m.buildTotal.WithLabelValues(pool, StateInterrupted).Inc()
	m.activeBuilds.WithLabelValues(pool).Dec()
}

func (m *metricsRecorder) RecordBuildJobState(pool, state string) {
	m.buildJobState.DeletePartialMatch(prometheus.Labels{"pool": pool})
	m.buildJobState.WithLabelValues(pool, state).Set(1)
}

func (m *metricsRecorder) RecordConfigChange(pool string) {
	m.configChangeTotal.WithLabelValues(pool).Inc()
}

func (m *metricsRecorder) RecordBuildRetry(pool string) {
	m.buildRetries.WithLabelValues(pool).Inc()
}

func (m *metricsRecorder) UpdateLayeredNodesCount(pool string, count int) {
	m.layeredNodesCount.WithLabelValues(pool).Set(float64(count))
}

func (m *metricsRecorder) RecordImagePushStarted(pool string) {
	m.imagePushState.DeletePartialMatch(prometheus.Labels{"pool": pool})
	m.imagePushState.WithLabelValues(pool, StatePushing).Set(1)
	m.pushStartTimes.Store(pool, time.Now())
}

func (m *metricsRecorder) RecordImagePushCompleted(pool string) {
	m.imagePushState.DeletePartialMatch(prometheus.Labels{"pool": pool})
	m.imagePushState.WithLabelValues(pool, StateSucceeded).Set(1)
	m.imagePushTotal.WithLabelValues(pool, StateSucceeded).Inc()
	if start, ok := m.pushStartTimes.LoadAndDelete(pool); ok {
		m.imagePushDur.WithLabelValues(pool, StateSucceeded).Observe(time.Since(start.(time.Time)).Seconds())
	}
}

func (m *metricsRecorder) RecordImagePushFailed(pool string) {
	m.imagePushState.DeletePartialMatch(prometheus.Labels{"pool": pool})
	m.imagePushState.WithLabelValues(pool, StateFailed).Set(1)
	m.imagePushTotal.WithLabelValues(pool, StateFailed).Inc()
	if start, ok := m.pushStartTimes.LoadAndDelete(pool); ok {
		m.imagePushDur.WithLabelValues(pool, StateFailed).Observe(time.Since(start.(time.Time)).Seconds())
	}
}

func (m *metricsRecorder) RecordBuildQueueDuration(pool string, queuedAt time.Time) {
	m.buildQueueDur.WithLabelValues(pool).Observe(time.Since(queuedAt).Seconds())
}

func (m *metricsRecorder) UpdateOCLRolloutCounts(pool string, updatedNodes, totalNodes int32) {
	m.rolloutUpdated.WithLabelValues(pool).Set(float64(updatedNodes))
	m.rolloutTotal.WithLabelValues(pool).Set(float64(totalNodes))
}

func (m *metricsRecorder) UpdateMOSCCount(count float64) {
	m.moscCount.Set(count)
}

// noopMetricsRecorder is a no-op MetricsRecorder for use in tests.
type noopMetricsRecorder struct{}

// NewNoopMetricsRecorder returns a MetricsRecorder that silently discards all metrics.
func NewNoopMetricsRecorder() MetricsRecorder { return &noopMetricsRecorder{} }

func (*noopMetricsRecorder) RecordBuildStarted(string)                    {}
func (*noopMetricsRecorder) RecordBuildBuilding(string)                   {}
func (*noopMetricsRecorder) RecordBuildCompleted(string, time.Time)       {}
func (*noopMetricsRecorder) RecordBuildFailed(string, time.Time)          {}
func (*noopMetricsRecorder) RecordBuildInterrupted(string)                {}
func (*noopMetricsRecorder) RecordBuildJobState(string, string)           {}
func (*noopMetricsRecorder) RecordConfigChange(string)                    {}
func (*noopMetricsRecorder) RecordBuildRetry(string)                      {}
func (*noopMetricsRecorder) UpdateLayeredNodesCount(string, int)          {}
func (*noopMetricsRecorder) RecordImagePushStarted(string)                {}
func (*noopMetricsRecorder) RecordImagePushCompleted(string)              {}
func (*noopMetricsRecorder) RecordImagePushFailed(string)                 {}
func (*noopMetricsRecorder) RecordBuildQueueDuration(string, time.Time)   {}
func (*noopMetricsRecorder) UpdateOCLRolloutCounts(string, int32, int32)  {}
func (*noopMetricsRecorder) UpdateMOSCCount(float64)                      {}
