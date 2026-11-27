package scheduler

import (
	"errors"
	"testing"

	"github.com/JBraunsmaJr/forge/internal/api"
)

func TestChildRunParent(t *testing.T) {
	s := openTestDB(t)

	pipelineStep := func(id string) api.StepDef {
		step := makeStep(id)
		step.Type = "pipeline"
		return step
	}

	// A pipeline step that an agent is executing can be a parent.
	parentRunID, err := s.SubmitRun(SubmitRunParams{Name: "parent", WorkspaceDir: "/ws", Ref: "refs/heads/release/1.2",
		Steps: []api.StepDef{pipelineStep("start-sdk")}})
	if err != nil {
		t.Fatalf("SubmitRun(parent): %v", err)
	}
	parentJob, ok := s.LeaseNext("agent-1")
	if !ok {
		t.Fatal("expected the parent's pipeline step to be available")
	}

	parent, err := s.ChildRunParent(parentJob.JobID)
	if err != nil {
		t.Fatalf("ChildRunParent(running pipeline step): %v", err)
	}
	if parent.RunID != parentRunID || parent.Ref != "refs/heads/release/1.2" || parent.Depth != 0 {
		t.Errorf("parent = %+v; want run %s, ref refs/heads/release/1.2, depth 0", parent, parentRunID)
	}

	// A child run of that step is one level deep.
	if _, err := s.SubmitRun(SubmitRunParams{Name: "child", WorkspaceDir: "/ws",
		ParentRunID: parentRunID, ParentJobID: parentJob.JobID,
		Steps: []api.StepDef{pipelineStep("start-docs")}}); err != nil {
		t.Fatalf("SubmitRun(child): %v", err)
	}
	childJob, ok := s.LeaseNext("agent-1")
	if !ok {
		t.Fatal("expected the child's pipeline step to be available")
	}
	child, err := s.ChildRunParent(childJob.JobID)
	if err != nil {
		t.Fatalf("ChildRunParent(child's pipeline step): %v", err)
	}
	if child.Depth != 1 {
		t.Errorf("child depth = %d; want 1", child.Depth)
	}

	// Anything else is refused.
	if _, err := s.ChildRunParent("no-such-job"); !errors.Is(err, errChildRunParent) {
		t.Errorf("ChildRunParent(unknown job) = %v; want errChildRunParent", err)
	}

	if _, err := s.Complete(childJob.JobID, childJob.LeaseID, 0, 0, nil, nil, false, false); err != nil {
		t.Fatalf("Complete: %v", err)
	}
	if _, err := s.ChildRunParent(childJob.JobID); !errors.Is(err, errChildRunParent) {
		t.Errorf("ChildRunParent(finished job) = %v; want errChildRunParent", err)
	}

	if _, err := s.SubmitRun(SubmitRunParams{Name: "plain", WorkspaceDir: "/ws",
		Steps: []api.StepDef{makeStep("lint")}}); err != nil {
		t.Fatalf("SubmitRun(plain): %v", err)
	}
	taskJob, ok := s.LeaseNext("agent-1")
	if !ok {
		t.Fatal("expected the task step to be available")
	}
	if _, err := s.ChildRunParent(taskJob.JobID); !errors.Is(err, errChildRunParent) {
		t.Errorf("ChildRunParent(task step) = %v; want errChildRunParent", err)
	}
}
