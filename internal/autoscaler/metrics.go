package autoscaler

import (
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
)

var (
	poolSize = promauto.NewGaugeVec(prometheus.GaugeOpts{
		Name: "forge_autoscaler_pool_size",
		Help: "Current number of instances in each pool",
	}, []string{"pool"})

	maxPoolSize = promauto.NewGaugeVec(prometheus.GaugeOpts{
		Name: "forge_autoscaler_max_pool_size",
		Help: "Maximum allowed number of instances in each pool",
	}, []string{"pool"})

	scaleEvents = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "forge_autoscaler_scale_events_total",
		Help: "Total number of scale-up/down events",
	}, []string{"pool", "direction"})

	observedQueueDepth = promauto.NewGauge(prometheus.GaugeOpts{
		Name: "forge_autoscaler_observed_queue_depth",
		Help: "Queue depth observed by the autoscaler",
	})

	provisionerErrors = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "forge_autoscaler_provisioner_errors_total",
		Help: "Total number of errors from the cloud provisioner",
	}, []string{"operation"})
)
