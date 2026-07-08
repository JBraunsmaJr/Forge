package integration

import (
	"fmt"
	"net/http"
	"testing"
	"time"
)

// TestRunListPagination verifies that the ?liit and ?offset query params work.
func TestRunListPagination(t *testing.T) {

	names := make([]string, 5)
	for i := 0; i < 5; i++ {
		names[i] = fmt.Sprintf("paginate-test-%d-%d", i, time.Now().UnixNano())
		submitPipeline(t, adminClient, names[i], []stepDef{
			echoStep("step", "pagination run"),
		})
	}

	resp, err := adminClient.get("/api/v1/runs?limit=3&offset=0")
	if err != nil {
		t.Fatalf("list runs page 1: %v", err)
	}
	requireStatus(t, resp, http.StatusOK)
	var page1 []struct {
		RunID string `json:"run_id"`
	}
	decode(t, resp, &page1)
	if len(page1) != 3 {
		t.Errorf("page 1: expected 3 runs, got %d", len(page1))
	}

	resp2, err := adminClient.get("/api/v1/runs?limit=3&offset=3")
	if err != nil {
		t.Fatalf("list runs page 2: %v", err)
	}
	requireStatus(t, resp2, http.StatusOK)
	var page2 []struct {
		RunID string `json:"run_id"`
	}
	decode(t, resp2, &page2)
	if len(page2) == 0 {
		t.Error("page 2: expected at least 1 run at offset 3")
	}

	seen := make(map[string]bool)
	for _, r := range page1 {
		seen[r.RunID] = true
	}
	for _, r := range page2 {
		if seen[r.RunID] {
			t.Errorf("run %s appears in both page 1 and page 2", r.RunID)
		}
	}
}

// TestRunListSearchFilter verifies the ?search= query param
func TestRunListSearchFilter(t *testing.T) {
	marker := fmt.Sprintf("search-marker-%d", time.Now().UnixNano())

	submitPipeline(t, adminClient, marker+"-alpha", []stepDef{echoStep("s", "a")})
	submitPipeline(t, adminClient, marker+"-beta", []stepDef{echoStep("s", "b")})
	submitPipeline(t, adminClient, "completely-different-pipeline", []stepDef{echoStep("s", "c")})

	resp, err := adminClient.get("/api/v1/runs?search=" + marker)
	if err != nil {
		t.Fatalf("search runs: %v", err)
	}
	requireStatus(t, resp, http.StatusOK)

	var results []struct {
		RunID string `json:"run_id"`
		Name  string `json:"name"`
	}
	decode(t, resp, &results)

	if len(results) < 2 {
		t.Errorf("expected at least 2 runs matching %q, got %d", marker, len(results))
	}
	for _, r := range results {
		if !contains(r.Name, marker) {
			t.Errorf("search returned run %q that doesn't match %q", r.Name, marker)
		}
	}
}

// TestRunListStatusFilter verifies the ?status= query param.
func TestRunListStatusFilter(t *testing.T) {

	failRunID := submitPipeline(t, adminClient, "status-filter-fail-test", []stepDef{
		failStep("intentional-fail"),
	})
	waitForRun(t, adminClient, failRunID)

	passRunID := submitPipeline(t, adminClient, "status-filter-pass-test", []stepDef{
		echoStep("success", "passing"),
	})
	waitForRun(t, adminClient, passRunID)

	resp, err := adminClient.get("/api/v1/runs?status=passed")
	if err != nil {
		t.Fatalf("list passed runs: %v", err)
	}
	requireStatus(t, resp, http.StatusOK)
	var passed []struct {
		RunID  string `json:"run_id"`
		Status string `json:"status"`
	}
	decode(t, resp, &passed)
	for _, r := range passed {
		if r.Status != "passed" {
			t.Errorf("status filter 'passed' returned run %s with status %q", r.RunID[:8], r.Status)
		}
	}

	found := false
	for _, r := range passed {
		if r.RunID == passRunID {
			found = true
		}
	}
	if !found {
		t.Errorf("passing run %s not found in ?status=passed results", passRunID[:8])
	}

	resp2, err := adminClient.get("/api/v1/runs?status=failed")
	if err != nil {
		t.Fatalf("list failed runs: %v", err)
	}
	requireStatus(t, resp2, http.StatusOK)
	var failed []struct {
		RunID  string `json:"run_id"`
		Status string `json:"status"`
	}
	decode(t, resp2, &failed)
	for _, r := range failed {
		if r.Status != "failed" {
			t.Errorf("status filter 'failed' returned run %s with status %q", r.RunID[:8], r.Status)
		}
	}
}

// TestRunListEmptySearchReturnsAll verifies that an empty search returns all runs.
func TestRunListEmptySearchReturnsAll(t *testing.T) {
	resp, err := adminClient.get("/api/v1/runs?search=&limit=50")
	if err != nil {
		t.Fatalf("list runs: %v", err)
	}
	requireStatus(t, resp, http.StatusOK)
	var runs []struct {
		RunID string `json:"run_id"`
	}
	decode(t, resp, &runs)

	t.Logf("empty search returned %d runs", len(runs))
}

// TestArtifactsListEndpoint verifies GET /api/v1/runs/{id}/artifacts works even for runs with no artifacts
// 200 empty array, not 404s
func TestArtifactsListEndpointEmpty(t *testing.T) {
	runID := submitPipeline(t, adminClient, "artifacts-list-empty", []stepDef{
		echoStep("no-artifact-step", "no artifacts here"),
	})
	waitForRun(t, adminClient, runID)

	resp, err := adminClient.get("/api/v1/runs/" + runID + "/artifacts")
	if err != nil {
		t.Fatalf("list artifacts: %v", err)
	}
	requireStatus(t, resp, http.StatusOK)

	var arts []interface{}
	decode(t, resp, &arts)

	t.Logf("run with no artifacts returned %d artifact(s)", len(arts))
}

// TestFlakyEndpointAccessible verifies GET /api/v1/flaky returns 200.
func TestFlakyEndpointAccessible(t *testing.T) {
	resp, err := adminClient.get("/api/v1/flaky?days=1&min_runs=1")
	if err != nil {
		t.Fatalf("flaky endpoint: %v", err)
	}
	requireStatus(t, resp, http.StatusOK)
	// Result is a JSON array (may be empty at this point in the test run).
	var steps []interface{}
	decode(t, resp, &steps)
	t.Logf("flaky endpoint returned %d steps", len(steps))
}

// TestProjectBranchFilter verifies that creating a project with branch_filter stores and returns the filter.
func TestProjectBranchFilter(t *testing.T) {
	resp, err := adminClient.post("/api/v1/projects", map[string]interface{}{
		"name":          fmt.Sprintf("branch-filter-test-%d", time.Now().UnixNano()),
		"repo_url":      fmt.Sprintf("https://github.com/test/repo-%d.git", time.Now().UnixNano()),
		"branch_filter": []string{"main", "release/*"},
	})
	if err != nil {
		t.Fatalf("create project: %v", err)
	}
	requireStatus(t, resp, http.StatusCreated)

	var proj struct {
		ID           string   `json:"id"`
		BranchFilter []string `json:"branch_filter"`
	}
	decode(t, resp, &proj)

	if len(proj.BranchFilter) != 2 {
		t.Errorf("expected 2 branch filters, got %d: %v", len(proj.BranchFilter), proj.BranchFilter)
	}
	if proj.BranchFilter[0] != "main" || proj.BranchFilter[1] != "release/*" {
		t.Errorf("unexpected branch filters: %v", proj.BranchFilter)
	}
}
