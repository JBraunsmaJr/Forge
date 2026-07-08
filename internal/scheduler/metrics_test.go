package scheduler

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/prometheus/client_golang/prometheus/promhttp"
)

func TestMetricsEndpoint(t *testing.T) {

	runsTotal.WithLabelValues("org1", "proj1", "cli").Inc()
	jobsSubmittedTotal.WithLabelValues("org1", "proj1").Add(5)
	jobsCompletedTotal.WithLabelValues("org1", "proj1", "success").Inc()
	jobDurationSeconds.WithLabelValues("org1", "proj1").Observe(10.5)
	agentsActive.Set(3)

	req := httptest.NewRequest("GET", "/metrics", nil)
	rr := httptest.NewRecorder()
	handler := promhttp.Handler()
	handler.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Errorf("expected status 200, got %d", rr.Code)
	}

	body, _ := io.ReadAll(rr.Body)
	metrics := string(body)

	expectedMetrics := []string{
		"forge_runs_total",
		"forge_jobs_submitted_total",
		"forge_jobs_completed_total",
		"forge_job_duration_seconds",
		"forge_agents_active",
	}

	for _, m := range expectedMetrics {
		if !strings.Contains(metrics, m) {
			t.Errorf("expected metric %s to be present in output", m)
		}
	}

	if !strings.Contains(metrics, `forge_runs_total{org_id="org1",project_id="proj1",trigger="cli"} 1`) {
		t.Errorf("expected forge_runs_total value 1 not found")
	}
	if !strings.Contains(metrics, `forge_jobs_submitted_total{org_id="org1",project_id="proj1"} 5`) {
		t.Errorf("expected forge_jobs_submitted_total value 5 not found")
	}
}
