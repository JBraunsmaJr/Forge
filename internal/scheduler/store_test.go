package scheduler

import (
	"os"
	"testing"
	"time"

	"github.com/JBraunsmaJr/forge/internal/api"
	"github.com/JBraunsmaJr/forge/internal/store"
)

// openTestDB opens a real Postgres DB for testing.
// Tests are skipped if FORGE_DB_URL is not set — this keeps CI
// working without a Postgres dependency unless explicitly configured.
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
		// Clean up test data after each test.
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

	runID, err := s.SubmitRun("test-pipeline", "/workspace", "", "", []api.StepDef{
		makeStep("lint"),
	}, nil)
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

	_, err := s.SubmitRun("pipe", "/ws", "", "", []api.StepDef{
		makeStep("lint"),
		makeStep("test", "lint"),
	}, nil)
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

	if _, err := s.Complete(spec.JobID, spec.LeaseID, 0, 0, nil, nil); err != nil {
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

func TestHeartbeat(t *testing.T) {
	s := openTestDB(t)
	s.SubmitRun("p", "/ws", "", "", []api.StepDef{makeStep("x")}, nil)
	spec, _ := s.LeaseNext("agent-1")

	if err := s.Heartbeat(spec.JobID, spec.LeaseID, "agent-1"); err != nil {
		t.Errorf("valid heartbeat rejected: %v", err)
	}
}

func TestHeartbeat_WrongLease(t *testing.T) {
	s := openTestDB(t)
	s.SubmitRun("p", "/ws", "", "", []api.StepDef{makeStep("x")}, nil)
	spec, _ := s.LeaseNext("agent-1")

	if err := s.Heartbeat(spec.JobID, "wrong-lease", "agent-1"); err == nil {
		t.Error("expected error for wrong lease ID")
	}
}

func TestReclaimStaleJobs(t *testing.T) {
	s := openTestDB(t)
	s.SubmitRun("p", "/ws", "", "", []api.StepDef{makeStep("x")}, nil)
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

func TestComplete_StaleLeaseIgnored(t *testing.T) {
	s := openTestDB(t)
	s.SubmitRun("p", "/ws", "", "", []api.StepDef{makeStep("x")}, nil)
	spec, _ := s.LeaseNext("agent-1")

	// Reclaim via heartbeat expiry.
	s.db.Exec(`UPDATE jobs SET heartbeat_at = NOW() - INTERVAL '60 seconds' WHERE id = $1`, spec.JobID)
	s.ReclaimStaleJobs()
	s.LeaseNext("agent-2")

	_, err := s.Complete(spec.JobID, spec.LeaseID, 0, 0, nil, nil)
	if err == nil {
		t.Error("expected error when completing with stale lease")
	}
}
