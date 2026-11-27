package scheduler

import (
	"testing"

	"github.com/JBraunsmaJr/forge/internal/api"
)

func TestRunStatusRef(t *testing.T) {
	s := openTestDB(t)

	// A merge request run: the ref is the request's head ref, the branch is
	// in the steps' environment.
	withBranch := makeStep("build")
	withBranch.Env = map[string]string{"FORGE_BRANCH": "feature/login"}
	mrRun, err := s.SubmitRun(SubmitRunParams{Name: "mr", WorkspaceDir: "/ws", Ref: "refs/merge-requests/7/head",
		Steps: []api.StepDef{makeStep("policy-step"), withBranch}})
	if err != nil {
		t.Fatalf("SubmitRun(mr): %v", err)
	}
	if got := s.RunStatusRef(mrRun); got != "feature/login" {
		t.Errorf("RunStatusRef(merge request run) = %q; want the source branch", got)
	}

	// A run with no branch recorded falls back to its ref.
	tagRun, err := s.SubmitRun(SubmitRunParams{Name: "tag", WorkspaceDir: "/ws", Ref: "refs/tags/v1.0.0",
		Steps: []api.StepDef{makeStep("build")}})
	if err != nil {
		t.Fatalf("SubmitRun(tag): %v", err)
	}
	if got := s.RunStatusRef(tagRun); got != "refs/tags/v1.0.0" {
		t.Errorf("RunStatusRef(tag run) = %q; want the run's ref", got)
	}

	if got := s.RunStatusRef("no-such-run"); got != "" {
		t.Errorf("RunStatusRef(unknown run) = %q; want empty", got)
	}
}
