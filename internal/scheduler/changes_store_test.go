package scheduler

import (
	"strings"
	"testing"

	"github.com/JBraunsmaJr/forge/internal/api"
	"github.com/JBraunsmaJr/forge/internal/changes"
)

// A step skipped by its changes: patterns must be stored as skipped with
// its reason, must never be handed to an agent, and must not stop the steps
// that depend on it.
func TestSubmitRun_StepSkippedByChanges(t *testing.T) {
	s := openTestDB(t)

	build := makeStep("build-api", "checkout")
	build.Changes = []string{"services/api/**"}
	steps, skipped := changes.Apply(
		[]api.StepDef{makeStep("checkout"), build, makeStep("release", "build-api")},
		changes.Of([]string{"docs/guide.md"}),
	)
	if len(skipped) != 1 || skipped[0] != "build-api" {
		t.Fatalf("skipped = %v; want [build-api]", skipped)
	}

	runID, err := s.SubmitRun(SubmitRunParams{Name: "changes", WorkspaceDir: "/ws", Steps: steps})
	if err != nil {
		t.Fatalf("SubmitRun: %v", err)
	}

	for _, want := range []string{"checkout", "release"} {
		spec, ok := s.LeaseNext("agent-1")
		if !ok {
			t.Fatalf("expected %s to be available, queue was empty", want)
		}
		if spec.StepID != want {
			t.Fatalf("leased %q; want %q", spec.StepID, want)
		}
		if _, err := s.Complete(spec.JobID, spec.LeaseID, 0, 0, nil, nil, false, false); err != nil {
			t.Fatalf("Complete(%s): %v", want, err)
		}
	}
	if spec, ok := s.LeaseNext("agent-1"); ok {
		t.Fatalf("leased %q; the skipped step must never reach an agent", spec.StepID)
	}

	status, ok := s.RunStatus(runID)
	if !ok {
		t.Fatal("RunStatus: run not found")
	}
	if status.Status != api.JobStatusPassed {
		t.Errorf("run status = %q; want passed", status.Status)
	}

	skippedJobs := 0
	for i, jobStatus := range status.Jobs {
		if jobStatus != api.JobStatusSkipped {
			continue
		}
		skippedJobs++
		logs, _ := s.GetJobLogs(status.JobIDs[i])
		if len(logs) != 1 || !strings.Contains(logs[0].Message, "services/api/**") {
			t.Errorf("skipped job log = %+v; want one line naming the patterns", logs)
		}
	}
	if skippedJobs != 1 {
		t.Errorf("%d skipped job(s) in %v; want exactly one", skippedJobs, status.Jobs)
	}

	// A rerun loads the steps back from the database. The marker must come
	// back with them so the rerun skips the same step again.
	info, err := s.RerunSteps(runID)
	if err != nil {
		t.Fatalf("RerunSteps: %v", err)
	}
	for i := range info.Steps {
		info.Steps[i].Status = ""
	}
	changes.PreserveSkips(info.Steps)
	for _, step := range info.Steps {
		wantSkipped := step.ID == "build-api"
		if (step.Status == api.JobStatusSkipped) != wantSkipped {
			t.Errorf("rerun: step %s has status %q; skipped should be %v", step.ID, step.Status, wantSkipped)
		}
	}
}
