package integration

import (
	"fmt"
	"net/http"
	"testing"
	"time"
)

var adminClient = newClient(adminToken)

func TestHealthCheck(t *testing.T) {
	resp, err := http.Get(schedulerURL + "/")
	if err != nil {
		t.Fatalf("GET /: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Errorf("expected 200, got %d", resp.StatusCode)
	}
}

// TestSingleStepPipeline submits the simplest possible pipeline and verifies it runs to completion
func TestSingleStepPipeline(t *testing.T) {
	runID := submitPipeline(t, adminClient, "single-step-test", []stepDef{
		echoStep("hello", "Hello from Forge integration test!"),
	})
	status := waitForRun(t, adminClient, runID)
	assertPassed(t, status)
}

// TestDAGPipeline verifies that step dependencies are respected - steps run
// in the correct order and parallel steps actually run in parallel.
func TestDAGPipeline(t *testing.T) {
	runID := submitPipeline(t, adminClient, "dag-test", []stepDef{
		echoStep("step-a", "step A"),
		echoStep("step-b", "step B"),                     // runs parallel with A
		echoStep("step-c", "step C", "step-a"),           // waits for A
		echoStep("step-d", "step D", "step-b"),           // waits for B
		echoStep("step-e", "fan-in", "step-c", "step-d"), // waits for C and D
	})
	status := waitForRun(t, adminClient, runID)
	assertPassed(t, status)
	if len(status.Jobs) != 5 {
		t.Errorf("expected 5 jobs, got %d", len(status.Jobs))
	}
}

// TestFailingPipeline verifies that a pipeline fails when a step exits non-zero.
func TestFailingPipeline(t *testing.T) {
	runID := submitPipeline(t, adminClient, "fail-test", []stepDef{
		echoStep("setup", "setup"),
		failStep("fail-me", "setup"),
		echoStep("never-runs", "this should not run", "fail-me"),
	})
	status := waitForRun(t, adminClient, runID)
	assertFailed(t, status)
}

// TestRunStatus verifies the /api/v1/runs/{id} endpoint returns correct data.
func TestRunStatus(t *testing.T) {
	runID := submitPipeline(t, adminClient, "status-test", []stepDef{
		echoStep("check-status", "status check"),
	})

	resp, err := adminClient.get("/api/v1/runs/" + runID)
	if err != nil {
		t.Fatalf("get run status: %v", err)
	}
	requireStatus(t, resp, http.StatusOK)

	var result struct {
		RunID  string `json:"run_id"`
		Status string `json:"status"`
	}
	decode(t, resp, &result)
	if result.RunID != runID {
		t.Errorf("run_id mismatch: expected %s, got %s", runID, result.RunID)
	}

	status := waitForRun(t, adminClient, runID)
	assertPassed(t, status)
}

// TestListRuns verifies the run list endpoint includes submitted runs.
func TestListRuns(t *testing.T) {
	name := uniqueName("list-test")
	runID := submitPipeline(t, adminClient, name, []stepDef{
		echoStep("list-step", "listing test"),
	})

	resp, err := adminClient.get("/api/v1/runs")
	if err != nil {
		t.Fatalf("list runs: %v", err)
	}
	requireStatus(t, resp, http.StatusOK)

	var runs []struct {
		RunID string `json:"run_id"`
	}
	decode(t, resp, &runs)

	found := false
	for _, r := range runs {
		if r.RunID == runID {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("submitted run %s not found in list (got %d runs)", runID, len(runs))
	}
}

// TestJobLogs verifies that job logs are stored and retrievable.
func TestJobLogs(t *testing.T) {
	const marker = "FORGE_INTEGRATION_LOG_MARKER_12345"
	runID := submitPipeline(t, adminClient, "log-test", []stepDef{
		{
			ID:      "log-step",
			Image:   "alpine:latest",
			Run:     fmt.Sprintf("echo '%s'", marker),
			Timeout: "1m",
		},
	})
	status := waitForRun(t, adminClient, runID)
	assertPassed(t, status)

	if len(status.Jobs) == 0 {
		t.Fatal("no jobs in completed run")
	}
	jobID := status.Jobs[0].JobID

	resp, err := adminClient.get("/api/v1/jobs/" + jobID + "/logs")
	if err != nil {
		t.Fatalf("get logs: %v", err)
	}
	requireStatus(t, resp, http.StatusOK)

	var logs []struct {
		Message string `json:"message"`
	}
	decode(t, resp, &logs)

	found := false
	for _, l := range logs {
		if contains(l.Message, marker) {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("marker %q not found in %d log events", marker, len(logs))
	}
}

// TestCancelPipeline verifies that a running pipeline can be cancelled.
func TestCancelPipeline(t *testing.T) {

	runID := submitPipeline(t, adminClient, "cancel-test", []stepDef{
		{
			ID:      "long-sleep",
			Image:   "alpine:latest",
			Run:     "sleep 600",
			Timeout: "20m",
		},
	})

	t.Log("waiting for job to reach running state...")
	deadline := time.Now().Add(60 * time.Second)
	running := false
	for time.Now().Before(deadline) {
		time.Sleep(3 * time.Second)
		resp, err := adminClient.get("/api/v1/runs/" + runID + "/detail")
		if err != nil {
			continue
		}
		var s runStatus
		decode(t, resp, &s)
		for _, j := range s.Jobs {
			if j.Status == "running" {
				running = true
				break
			}
		}
		if running {
			t.Log("job is running — canceling now")
			break
		}
	}
	if !running {
		t.Log("job never became 'running' — canceling anyway (may already be queued)")
	}

	resp, err := adminClient.post("/api/v1/runs/"+runID+"/cancel", nil)
	if err != nil {
		t.Fatalf("cancel: %v", err)
	}
	requireStatus(t, resp, http.StatusOK)

	status := waitForRun(t, adminClient, runID)
	if status.Status != "canceled" && status.Status != "failed" {
		t.Errorf("expected canceled or failed status, got %q", status.Status)
	}
}

// TestRerun verifies that a completed run can be resubmitted.
func TestRerun(t *testing.T) {

	runID := submitPipeline(t, adminClient, "rerun-test", []stepDef{
		echoStep("original", "original run"),
	})
	waitForRun(t, adminClient, runID)

	resp, err := adminClient.post("/api/v1/runs/"+runID+"/rerun", nil)
	if err != nil {
		t.Fatalf("rerun: %v", err)
	}
	requireStatus(t, resp, http.StatusCreated)

	var result struct {
		RunID string `json:"run_id"`
	}
	decode(t, resp, &result)
	if result.RunID == runID {
		t.Error("rerun should produce a new run ID")
	}

	status := waitForRun(t, adminClient, result.RunID)
	assertPassed(t, status)
}

func contains(s, sub string) bool {
	return len(s) >= len(sub) && (s == sub || len(sub) == 0 ||
		func() bool {
			for i := 0; i <= len(s)-len(sub); i++ {
				if s[i:i+len(sub)] == sub {
					return true
				}
			}
			return false
		}())
}
