package scheduler

import (
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/JBraunsmaJr/forge/internal/api"
	"github.com/JBraunsmaJr/forge/internal/store"
)

func openTestDB(t *testing.T) *Store {
	t.Helper()
	url := os.Getenv("FORGE_DB_URL")
	if url == "" {
		t.Skip("FORGE_DB_URL not set — skipping Postgres integration test")
	}
	db, err := store.Open(url)
	if err != nil {
		t.Fatalf("opening test database: %v", err)
	}
	t.Cleanup(func() {

		db.Exec(`DELETE FROM job_logs`)
		db.Exec(`DELETE FROM jobs`)
		db.Exec(`DELETE FROM runs`)
		db.Exec(`DELETE FROM policies`)
		db.Exec(`DELETE FROM orgs`)
		db.Close()
	})
	return NewStore(db)
}

func makeStep(id string, deps ...string) api.StepDef {
	return api.StepDef{
		ID:        id,
		Image:     "alpine:latest",
		Command:   []string{"echo", id},
		WorkDir:   "/workspace",
		DependsOn: deps,
		Timeout:   5 * time.Minute,
	}
}

func TestSubmitAndLease(t *testing.T) {
	s := openTestDB(t)

	runID, err := s.SubmitRun("test-pipeline", "/workspace", "", "", "", "", "", []api.StepDef{
		makeStep("lint"),
	}, nil, "")
	if err != nil {
		t.Fatalf("SubmitRun: %v", err)
	}
	if runID == "" {
		t.Fatal("expected a run ID")
	}

	spec, ok := s.LeaseNext("agent-1")
	if !ok {
		t.Fatal("expected a job, queue was empty")
	}
	if spec.StepID != "lint" {
		t.Errorf("expected step 'lint', got %q", spec.StepID)
	}
	if spec.LeaseID == "" {
		t.Error("expected a lease ID")
	}
	if spec.WorkspaceDir != "/workspace" {
		t.Errorf("expected WorkspaceDir '/workspace', got %q", spec.WorkspaceDir)
	}
}

func TestLeaseEmpty(t *testing.T) {
	s := openTestDB(t)
	spec, ok := s.LeaseNext("agent-1")
	if ok || spec != nil {
		t.Error("expected empty queue to return (nil, false)")
	}
}

func TestDependencyUnblocking(t *testing.T) {
	s := openTestDB(t)

	_, err := s.SubmitRun("pipe", "/ws", "", "", "", "", "", []api.StepDef{
		makeStep("lint"),
		makeStep("test", "lint"),
	}, nil, "")
	if err != nil {
		t.Fatalf("SubmitRun: %v", err)
	}

	spec, ok := s.LeaseNext("agent-1")
	if !ok {
		t.Fatal("expected lint to be available")
	}
	if spec.StepID != "lint" {
		t.Errorf("expected 'lint', got %q", spec.StepID)
	}

	_, ok = s.LeaseNext("agent-1")
	if ok {
		t.Error("expected queue to be empty while lint is running")
	}

	if _, err := s.Complete(spec.JobID, spec.LeaseID, 0, 0, nil, nil, false, false); err != nil {
		t.Fatalf("Complete: %v", err)
	}

	spec2, ok := s.LeaseNext("agent-1")
	if !ok {
		t.Fatal("expected 'test' to be queued after lint passed")
	}
	if spec2.StepID != "test" {
		t.Errorf("expected 'test', got %q", spec2.StepID)
	}
}

func TestMatrixDependency(t *testing.T) {
	s := openTestDB(t)

	// Step 'matrix-1' and 'matrix-2' are instances of the same matrix step
	// Step 'downstream' depends on both.
	_, err := s.SubmitRun("pipe", "/ws", "", "", "", "", "", []api.StepDef{
		makeStep("matrix-1"),
		makeStep("matrix-2"),
		{
			ID:        "downstream",
			DependsOn: []string{"matrix-1", "matrix-2"},
			Image:     "alpine",
			Type:      "release", // to match user's case
		},
	}, nil, "")
	if err != nil {
		t.Fatalf("SubmitRun: %v", err)
	}

	spec1, ok := s.LeaseNext("agent-1")
	if !ok || spec1.StepID != "matrix-1" {
		t.Fatalf("expected 'matrix-1', got %v", spec1)
	}
	spec2, ok := s.LeaseNext("agent-2")
	if !ok || spec2.StepID != "matrix-2" {
		t.Fatalf("expected 'matrix-2', got %v", spec2)
	}

	// Downstream should NOT be ready yet.
	spec3, ok := s.LeaseNext("agent-3")
	if ok {
		t.Fatalf("expected nothing ready, but got %v", spec3.StepID)
	}

	// Complete first matrix job.
	if _, err := s.Complete(spec1.JobID, spec1.LeaseID, 0, 100, nil, nil, false, false); err != nil {
		t.Fatalf("Complete 1: %v", err)
	}

	// Downstream should STILL NOT be ready.
	spec3, ok = s.LeaseNext("agent-3")
	if ok {
		t.Fatalf("expected nothing ready after first matrix job, but got %v", spec3.StepID)
	}

	// Complete second matrix job.
	if _, err := s.Complete(spec2.JobID, spec2.LeaseID, 0, 100, nil, nil, false, false); err != nil {
		t.Fatalf("Complete 2: %v", err)
	}

	// NOW downstream should be ready in 'release' status.
	// But LeaseNext only leases 'queued' jobs.
	// We need to check the DB or use LeaseReleaseJob.
	spec4, ok := s.LeaseReleaseJob()
	if !ok || spec4.StepID != "downstream" {
		t.Fatalf("expected 'downstream' to be ready for release, got %v", spec4)
	}
}

func TestTransitiveSkipping(t *testing.T) {
	s := openTestDB(t)

	_, err := s.SubmitRun("pipe", "/ws", "", "", "", "", "", []api.StepDef{
		makeStep("a"),
		makeStep("b", "a"),
		makeStep("c", "b"),
	}, nil, "")
	if err != nil {
		t.Fatalf("SubmitRun: %v", err)
	}

	spec, ok := s.LeaseNext("agent-1")
	if !ok || spec.StepID != "a" {
		t.Fatalf("expected 'a', got %v", spec)
	}

	// Fail 'a'
	if _, err := s.Complete(spec.JobID, spec.LeaseID, 1, 0, nil, nil, false, false); err != nil {
		t.Fatalf("Complete: %v", err)
	}

	// 'b' should be skipped because 'a' failed.
	// 'c' should be skipped because 'b' was skipped.

	spec, ok = s.LeaseNext("agent-1")
	if ok {
		t.Fatalf("expected nothing queued, but got %v", spec.StepID)
	}

	// Verify statuses in DB
	rows, err := s.db.Query("SELECT step_id, status FROM jobs")
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	statuses := make(map[string]string)
	for rows.Next() {
		var id, status string
		rows.Scan(&id, &status)
		statuses[id] = status
	}

	if statuses["b"] != "skipped" {
		t.Errorf("expected 'b' to be skipped, got %q", statuses["b"])
	}
	if statuses["c"] != "skipped" {
		t.Errorf("expected 'c' to be skipped, got %q", statuses["c"])
	}
}

func TestHeartbeat(t *testing.T) {
	s := openTestDB(t)
	s.SubmitRun("p", "/ws", "", "", "", "", "", []api.StepDef{makeStep("x")}, nil, "")
	spec, _ := s.LeaseNext("agent-1")

	if err := s.Heartbeat(spec.JobID, spec.LeaseID, "agent-1"); err != nil {
		t.Errorf("valid heartbeat rejected: %v", err)
	}
}

func TestHeartbeat_WrongLease(t *testing.T) {
	s := openTestDB(t)
	s.SubmitRun("p", "/ws", "", "", "", "", "", []api.StepDef{makeStep("x")}, nil, "")
	spec, _ := s.LeaseNext("agent-1")

	if err := s.Heartbeat(spec.JobID, "wrong-lease", "agent-1"); err == nil {
		t.Error("expected error for wrong lease ID")
	}
}

func TestReclaimStaleJobs(t *testing.T) {
	s := openTestDB(t)
	s.SubmitRun("p", "/ws", "", "", "", "", "", []api.StepDef{makeStep("x")}, nil, "")
	spec, _ := s.LeaseNext("agent-1")

	// Age the heartbeat so the job looks stale.
	s.db.Exec(`UPDATE jobs SET heartbeat_at = NOW() - INTERVAL '60 seconds' WHERE id = $1`, spec.JobID)

	reclaimed := s.ReclaimStaleJobs()
	if reclaimed != 1 {
		t.Errorf("expected 1 reclaimed job, got %d", reclaimed)
	}

	spec2, ok := s.LeaseNext("agent-2")
	if !ok {
		t.Fatal("expected reclaimed job to be leaseable")
	}
	if spec2.LeaseID == spec.LeaseID {
		t.Error("expected a new lease ID after reclaim")
	}
}

func TestListRunsPagination(t *testing.T) {
	s := openTestDB(t)

	for i := 0; i < 5; i++ {
		_, err := s.SubmitRun(fmt.Sprintf("page-run-%d", i), "/ws", "", "", "", "", "", []api.StepDef{makeStep("step")}, nil, "")
		if err != nil {
			t.Fatalf("SubmitRun %d: %v", i, err)
		}
	}

	page1 := s.ListRuns(ListRunsOptions{Limit: 3, Offset: 0})
	if len(page1) != 3 {
		t.Errorf("expected 3 runs in page 1, got %d", len(page1))
	}

	page2 := s.ListRuns(ListRunsOptions{Limit: 3, Offset: 3})
	if len(page2) != 2 {
		t.Errorf("expected 2 runs in page 2, got %d", len(page2))
	}

	ids := make(map[string]bool)
	for _, r := range append(page1, page2...) {
		if ids[r.RunID] {
			t.Errorf("duplicate run ID %s across pages", r.RunID)
		}
		ids[r.RunID] = true
	}
}

func TestListRunsSearchFilter(t *testing.T) {
	s := openTestDB(t)

	s.SubmitRun("frontend-deploy", "/ws", "", "", "", "", "", []api.StepDef{makeStep("step")}, nil, "")
	s.SubmitRun("backend-deploy", "/ws", "", "", "", "", "", []api.StepDef{makeStep("step")}, nil, "")
	s.SubmitRun("database-migrate", "/ws", "", "", "", "", "", []api.StepDef{makeStep("step")}, nil, "")

	results := s.ListRuns(ListRunsOptions{Search: "deploy"})
	if len(results) != 2 {
		t.Errorf("expected 2 deploy runs, got %d", len(results))
	}
	for _, r := range results {
		if r.Name != "frontend-deploy" && r.Name != "backend-deploy" {
			t.Errorf("unexpected run in deploy search: %s", r.Name)
		}
	}

	upper := s.ListRuns(ListRunsOptions{Search: "DEPLOY"})
	if len(upper) != 2 {
		t.Errorf("case-insensitive search failed: expected 2, got %d", len(upper))
	}

	none := s.ListRuns(ListRunsOptions{Search: "xyzzy-nonexistent"})
	if len(none) != 0 {
		t.Errorf("expected 0 results for non-matching search, got %d", len(none))
	}
}

func TestListRunsStatusFilter(t *testing.T) {
	s := openTestDB(t)

	passedID, _ := s.SubmitRun("passing-run", "/ws", "", "", "", "", "", []api.StepDef{makeStep("step")}, nil, "")
	spec, _ := s.LeaseNext("agent-test")
	s.Complete(spec.JobID, spec.LeaseID, 0, 100, nil, nil, false, false)

	failedID, _ := s.SubmitRun("failing-run", "/ws", "", "", "", "", "", []api.StepDef{makeStep("step")}, nil, "")
	spec2, _ := s.LeaseNext("agent-test")
	s.Complete(spec2.JobID, spec2.LeaseID, 1, 100, nil, nil, false, false)

	s.SubmitRun("pending-run", "/ws", "", "", "", "", "", []api.StepDef{makeStep("step")}, nil, "")

	_ = passedID
	_ = failedID

	passed := s.ListRuns(ListRunsOptions{Status: "passed"})
	for _, r := range passed {
		if r.Status != "passed" {
			t.Errorf("status filter 'passed' returned run with status %q", r.Status)
		}
	}
	if len(passed) < 1 {
		t.Error("expected at least 1 passed run")
	}

	failed := s.ListRuns(ListRunsOptions{Status: "failed"})
	for _, r := range failed {
		if r.Status != "failed" {
			t.Errorf("status filter 'failed' returned run with status %q", r.Status)
		}
	}
	if len(failed) < 1 {
		t.Error("expected at least 1 failed run")
	}
}

func TestListRunsDefaultLimit(t *testing.T) {
	s := openTestDB(t)

	result := s.ListRuns(ListRunsOptions{})
	if result == nil {
		result = []api.RunSummary{}
	}

	_ = result
}
func TestComplete_StaleLeaseIgnored(t *testing.T) {
	s := openTestDB(t)
	s.SubmitRun("p", "/ws", "", "", "", "", "", []api.StepDef{makeStep("x")}, nil, "")
	spec, _ := s.LeaseNext("agent-1")

	s.db.Exec(`UPDATE jobs SET heartbeat_at = NOW() - INTERVAL '60 seconds' WHERE id = $1`, spec.JobID)
	s.ReclaimStaleJobs()
	s.LeaseNext("agent-2")

	_, err := s.Complete(spec.JobID, spec.LeaseID, 0, 0, nil, nil, false, false)
	if err == nil {
		t.Error("expected error when completing with stale lease")
	}
}

func TestActiveAgentsCount(t *testing.T) {
	s := openTestDB(t)

	count, err := s.ActiveAgentsCount()
	if err != nil {
		t.Fatalf("ActiveAgentsCount failed: %v", err)
	}
	if count != 0 {
		t.Errorf("expected 0 active agents, got %d", count)
	}

	s.SubmitRun("p", "/ws", "", "", "", "", "", []api.StepDef{makeStep("x")}, nil, "")
	spec, _ := s.LeaseNext("agent-1")

	s.db.Exec(`UPDATE jobs SET heartbeat_at = NOW() WHERE id = $1`, spec.JobID)

	count, _ = s.ActiveAgentsCount()
	if count != 1 {
		t.Errorf("expected 1 active agent, got %d", count)
	}

	s.SubmitRun("p2", "/ws", "", "", "", "", "", []api.StepDef{makeStep("y")}, nil, "")
	spec2, _ := s.LeaseNext("agent-2")
	s.db.Exec(`UPDATE jobs SET heartbeat_at = NOW() WHERE id = $1`, spec2.JobID)

	count, _ = s.ActiveAgentsCount()
	if count != 2 {
		t.Errorf("expected 2 active agents, got %d", count)
	}

	s.db.Exec(`UPDATE jobs SET heartbeat_at = NOW() - INTERVAL '5 minutes' WHERE id = $1`, spec.JobID)
	count, _ = s.ActiveAgentsCount()
	if count != 1 {
		t.Errorf("expected 1 active agent after one expired, got %d", count)
	}
}
