package scheduler

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/JBraunsmaJr/forge/internal/api"
)

// jobStatus reads a step's job status straight from the database.
func jobStatus(t *testing.T, s *Store, runID, stepID string) string {
	t.Helper()
	var status string
	if err := s.db.QueryRow(`SELECT status FROM jobs WHERE run_id = $1 AND step_id = $2`, runID, stepID).Scan(&status); err != nil {
		t.Fatalf("reading the status of step %s: %v", stepID, err)
	}
	return status
}

// loseAgent simulates an agent that leased the next job and then vanished:
// its heartbeats stop and the scheduler's monitor runs.
func loseAgent(t *testing.T, s *Store, agentID string) *api.JobSpec {
	t.Helper()
	spec, ok := s.LeaseNext(agentID)
	if !ok {
		t.Fatalf("agent %s: expected a job to lease", agentID)
	}
	ageHeartbeat(t, s, spec.JobID)
	if n := s.ReclaimStaleJobs(); n != 1 {
		t.Fatalf("ReclaimStaleJobs() = %d; want 1", n)
	}
	return spec
}

// The situation this exists for: an agent dies in the middle of a step.
// Another agent must get the step, the run must still pass, and the dead
// agent must not be able to interfere if it turns out to be alive after all.
func TestAgentLoss_StepMovesToAnotherAgentAndTheRunPasses(t *testing.T) {
	s := openTestDB(t)
	runID, err := s.SubmitRun(SubmitRunParams{Name: "agent-loss", WorkspaceDir: "/ws",
		Steps: []api.StepDef{makeStep("build"), makeStep("test", "build")}})
	if err != nil {
		t.Fatalf("SubmitRun: %v", err)
	}

	lost := loseAgent(t, s, "agent-1")
	if lost.StepID != "build" {
		t.Fatalf("leased %q; want build", lost.StepID)
	}

	// The step's log says what happened.
	logs, _ := s.GetJobLogs(lost.JobID)
	if len(logs) != 1 || logs[0].Level != "WARN" || !strings.Contains(logs[0].Message, "stopped responding") ||
		!strings.Contains(logs[0].Message, "attempt 2 of 3") {
		t.Errorf("log after losing the agent = %+v; want one warning announcing attempt 2 of 3", logs)
	}

	// Another agent picks the step up under a new lease.
	retry, ok := s.LeaseNext("agent-2")
	if !ok || retry.JobID != lost.JobID {
		t.Fatalf("agent-2 leased %+v; want the reclaimed build step", retry)
	}
	if retry.LeaseID == lost.LeaseID {
		t.Fatal("the reclaimed job kept its old lease")
	}

	// The first agent was only cut off, and now reports in. It is told it
	// no longer holds the job, and its result is not accepted.
	if err := s.Heartbeat(lost.JobID, lost.LeaseID, "agent-1"); !errors.Is(err, errLeaseLost) {
		t.Errorf("heartbeat from the lost agent = %v; want errLeaseLost", err)
	}
	if _, err := s.Complete(lost.JobID, lost.LeaseID, 1, 0, nil, nil, false, false); err == nil {
		t.Error("a result from the lost agent was accepted")
	}
	if got := jobStatus(t, s, runID, "build"); got != "running" {
		t.Errorf("build is %q after the lost agent reported; want it still running on agent-2", got)
	}

	// The second agent finishes the run.
	if err := s.Heartbeat(retry.JobID, retry.LeaseID, "agent-2"); err != nil {
		t.Errorf("heartbeat from the new agent: %v", err)
	}
	if _, err := s.Complete(retry.JobID, retry.LeaseID, 0, 0, nil, nil, false, false); err != nil {
		t.Fatalf("Complete(build): %v", err)
	}
	test, ok := s.LeaseNext("agent-2")
	if !ok || test.StepID != "test" {
		t.Fatalf("leased %+v; want the test step unlocked by build", test)
	}
	if _, err := s.Complete(test.JobID, test.LeaseID, 0, 0, nil, nil, false, false); err != nil {
		t.Fatalf("Complete(test): %v", err)
	}
	if status, ok := s.RunStatus(runID); !ok || status.Status != api.JobStatusPassed {
		t.Errorf("run status = %+v; want passed", status.Status)
	}
}

// A step that takes down every agent it is given must not circulate forever.
func TestAgentLoss_StepFailsAfterTheLastAttempt(t *testing.T) {
	s := openTestDB(t)
	runID, err := s.SubmitRun(SubmitRunParams{Name: "agent-killer", WorkspaceDir: "/ws",
		Steps: []api.StepDef{makeStep("build"), makeStep("test", "build")}})
	if err != nil {
		t.Fatalf("SubmitRun: %v", err)
	}

	var jobID string
	for attempt := 1; attempt <= maxJobAttempts; attempt++ {
		if attempt > 1 {
			if got := jobStatus(t, s, runID, "build"); got != "queued" {
				t.Fatalf("before attempt %d build is %q; want queued", attempt, got)
			}
		}
		jobID = loseAgent(t, s, "agent-"+string(rune('0'+attempt))).JobID
	}

	if got := jobStatus(t, s, runID, "build"); got != "failed" {
		t.Errorf("build is %q after %d lost agents; want failed", got, maxJobAttempts)
	}
	if spec, ok := s.LeaseNext("agent-9"); ok {
		t.Errorf("leased %q; nothing should be left to hand out", spec.StepID)
	}
	// The step waiting on it is resolved, so the run can finish.
	if got := jobStatus(t, s, runID, "test"); got != "skipped" {
		t.Errorf("test is %q; want skipped because build failed", got)
	}
	if status, ok := s.RunStatus(runID); !ok || status.Status != api.JobStatusFailed {
		t.Errorf("run status = %+v; want failed", status.Status)
	}

	logs, _ := s.GetJobLogs(jobID)
	if len(logs) != maxJobAttempts {
		t.Fatalf("build has %d log line(s); want one per lost agent", len(logs))
	}
	last := logs[len(logs)-1]
	if last.Level != "ERROR" || !strings.Contains(last.Message, "attempt 3 of 3") {
		t.Errorf("last log line = %+v; want an error naming the last attempt", last)
	}
}

// Jobs whose agents are still reporting are left alone.
func TestReclaimStaleJobs_LeavesLiveJobsAlone(t *testing.T) {
	s := openTestDB(t)
	runID, _ := s.SubmitRun(SubmitRunParams{Name: "live", WorkspaceDir: "/ws", Steps: []api.StepDef{makeStep("build")}})
	if _, ok := s.LeaseNext("agent-1"); !ok {
		t.Fatal("expected a job to lease")
	}
	if n := s.ReclaimStaleJobs(); n != 0 {
		t.Errorf("ReclaimStaleJobs() = %d; want 0 for a job with a fresh heartbeat", n)
	}
	if got := jobStatus(t, s, runID, "build"); got != "running" {
		t.Errorf("build is %q; want running", got)
	}
}

func TestReclaimAllowed(t *testing.T) {
	started := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)
	cases := map[time.Duration]bool{
		0:                          false,
		15 * time.Second:           false,
		staleJobAfter:              false, // an agent needs time for one heartbeat after reconnecting
		startupGrace - time.Second: false,
		startupGrace:               true,
		time.Hour:                  true,
	}
	for elapsed, want := range cases {
		if got := reclaimAllowed(started, started.Add(elapsed)); got != want {
			t.Errorf("reclaimAllowed(%v after start) = %v; want %v", elapsed, got, want)
		}
	}
	if startupGrace < staleJobAfter {
		t.Error("startupGrace must be at least staleJobAfter")
	}
}

func TestReclaimNotice(t *testing.T) {
	level, message := reclaimNotice(reclaimedJob{AgentID: "0123456789abcdef", Attempts: 1})
	if level != "WARN" || !strings.Contains(message, "attempt 2 of 3") || !strings.Contains(message, "returned to the queue") {
		t.Errorf("requeue notice = %s %q", level, message)
	}
	level, message = reclaimNotice(reclaimedJob{AgentID: "0123456789abcdef", Attempts: 3, Failed: true})
	if level != "ERROR" || !strings.Contains(message, "attempt 3 of 3") || !strings.Contains(message, "failed") {
		t.Errorf("failure notice = %s %q", level, message)
	}
	if strings.Contains(message, "0123456789abcdef") {
		t.Errorf("notice contains the full agent ID: %q", message)
	}
}
