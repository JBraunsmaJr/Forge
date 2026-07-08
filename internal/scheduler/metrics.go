package scheduler

import (
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
)

var (
	jobsSubmittedTotal = promauto.NewCounterVec(
		prometheus.CounterOpts{
			Name: "forge_jobs_submitted_total",
			Help: "The total number of jobs submitted",
		},
		[]string{"org_id", "project_id"},
	)

	jobsCompletedTotal = promauto.NewCounterVec(
		prometheus.CounterOpts{
			Name: "forge_jobs_completed_total",
			Help: "The total number of jobs completed",
		},
		[]string{"org_id", "project_id", "status"},
	)

	jobDurationSeconds = promauto.NewHistogramVec(
		prometheus.HistogramOpts{
			Name:    "forge_job_duration_seconds",
			Help:    "Duration of completed jobs in seconds",
			Buckets: []float64{1, 5, 10, 30, 60, 300, 600, 1800, 3600},
		},
		[]string{"org_id", "project_id"},
	)

	agentsActive = promauto.NewGauge(
		prometheus.GaugeOpts{
			Name: "forge_agents_active",
			Help: "Number of agents that have heartbeated in the last 2 minutes",
		},
	)

	runsTotal = promauto.NewCounterVec(
		prometheus.CounterOpts{
			Name: "forge_runs_total",
			Help: "The total number of pipeline runs",
		},
		[]string{"org_id", "project_id", "trigger"},
	)
)
