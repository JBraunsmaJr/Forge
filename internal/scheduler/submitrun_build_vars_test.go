package scheduler

import (
	"encoding/json"
	"testing"

	"github.com/JBraunsmaJr/forge/internal/api"
)

// insertTestProject inserts a minimal org + project row so SubmitRun's
// project_id foreign key is satisfiable, and — more importantly for
// these tests — so build-number assignment actually mints a real,
// counter-backed number instead of falling back to "local" (which only
// happens for a run with no ProjectID at all; see assignBuildNumber).
func insertTestProject(t *testing.T, st *Store, orgID, projectID string) {
	t.Helper()
	if _, err := st.db.Exec(`INSERT INTO orgs (id, name) VALUES ($1, $2)`, orgID, "Test Org"); err != nil {
		t.Fatalf("insert test org: %v", err)
	}
	if _, err := st.db.Exec(
		`INSERT INTO projects (id, org_id, name, repo_url, webhook_secret) VALUES ($1, $2, $3, $4, $5)`,
		projectID, orgID, "Test Project", "https://example.com/"+projectID+".git", "secret",
	); err != nil {
		t.Fatalf("insert test project: %v", err)
	}
}

// buildEnvOf returns the FORGE_BUILD_NUMBER/FORGE_BUILD_COUNTER stamped
// into stepID's job env within runID, failing the test if the job or
// either variable can't be found. Reading it back from the jobs table
// (rather than from whatever StepDef was passed in) is deliberate:
// it's the only way to confirm SubmitRun actually persisted the
// stamped values, as opposed to merely computing them and discarding
// them, or stamping a copy that never reached the INSERT.
func buildEnvOf(t *testing.T, st *Store, runID, stepID string) (number, counter string) {
	t.Helper()
	var envJSON string
	if err := st.db.QueryRow(`SELECT env FROM jobs WHERE run_id = $1 AND step_id = $2`, runID, stepID).Scan(&envJSON); err != nil {
		t.Fatalf("query job env for step %q: %v", stepID, err)
	}
	var env map[string]string
	if err := json.Unmarshal([]byte(envJSON), &env); err != nil {
		t.Fatalf("unmarshal job env for step %q: %v", stepID, err)
	}
	number, ok := env["FORGE_BUILD_NUMBER"]
	if !ok {
		t.Fatalf("step %q: FORGE_BUILD_NUMBER not set in job env (got %v)", stepID, env)
	}
	counter, ok = env["FORGE_BUILD_COUNTER"]
	if !ok {
		t.Fatalf("step %q: FORGE_BUILD_COUNTER not set in job env (got %v)", stepID, env)
	}
	return number, counter
}

// TestSubmitRun_BuildVariables_Webhook covers the common top-level
// submission path (no ParentRunID, a real ProjectID — what a webhook
// push triggers): the step's job env must carry a real, non-"local"
// build number and a nonzero counter by the time it's persisted.
func TestSubmitRun_BuildVariables_Webhook(t *testing.T) {
	st := openTestDB(t)
	insertTestProject(t, st, "org-webhook", "proj-webhook")

	runID, err := st.SubmitRun(SubmitRunParams{
		Name:         "webhook-run",
		OrgID:        "org-webhook",
		ProjectID:    "proj-webhook",
		PipelineName: "ci",
		Ref:          "refs/heads/main",
		CommitSHA:    "deadbeef",
		SCMProvider:  "github",
		Steps:        []api.StepDef{{ID: "build", Image: "alpine"}},
	})
	if err != nil {
		t.Fatalf("SubmitRun failed: %v", err)
	}

	number, counter := buildEnvOf(t, st, runID, "build")
	if number == "" || number == "local" {
		t.Errorf("expected a real build number for a project-scoped run, got %q", number)
	}
	if counter == "" || counter == "0" {
		t.Errorf("expected a nonzero build counter, got %q", counter)
	}
}

// TestSubmitRun_BuildVariables_Matrix covers a multi-step run — the
// shape a matrix: block compiles to (each combination is its own
// api.StepDef by the time it reaches SubmitRun; the actual ${{ matrix.* }}
// interpolation is compiler-side and already covered in
// internal/compiler). Every step in one run must share the exact same
// build number and counter: they're all part of the one run SubmitRun
// assigned exactly one build number to.
func TestSubmitRun_BuildVariables_Matrix(t *testing.T) {
	st := openTestDB(t)
	insertTestProject(t, st, "org-matrix", "proj-matrix")

	runID, err := st.SubmitRun(SubmitRunParams{
		Name:         "matrix-run",
		OrgID:        "org-matrix",
		ProjectID:    "proj-matrix",
		PipelineName: "ci",
		Ref:          "refs/heads/main",
		CommitSHA:    "deadbeef",
		SCMProvider:  "github",
		Steps: []api.StepDef{
			{ID: "build-linux", Image: "alpine"},
			{ID: "build-windows", Image: "alpine"},
		},
	})
	if err != nil {
		t.Fatalf("SubmitRun failed: %v", err)
	}

	numberLinux, counterLinux := buildEnvOf(t, st, runID, "build-linux")
	numberWindows, counterWindows := buildEnvOf(t, st, runID, "build-windows")

	if numberLinux != numberWindows {
		t.Errorf("matrix combinations got different build numbers: %q vs %q", numberLinux, numberWindows)
	}
	if counterLinux != counterWindows {
		t.Errorf("matrix combinations got different build counters: %q vs %q", counterLinux, counterWindows)
	}
}

// TestSubmitRun_BuildVariables_Rerun covers a rerun (ParentRunID set,
// ParentJobID empty): assignBuildNumber must copy the parent run's
// exact build number and counter rather than minting a new one — a
// rerun is supposed to reproduce the original run's build identity,
// not get a build number of its own.
func TestSubmitRun_BuildVariables_Rerun(t *testing.T) {
	st := openTestDB(t)
	insertTestProject(t, st, "org-rerun", "proj-rerun")

	parentRunID, err := st.SubmitRun(SubmitRunParams{
		Name:         "original-run",
		OrgID:        "org-rerun",
		ProjectID:    "proj-rerun",
		PipelineName: "ci",
		Ref:          "refs/heads/main",
		CommitSHA:    "deadbeef",
		SCMProvider:  "github",
		Steps:        []api.StepDef{{ID: "build", Image: "alpine"}},
	})
	if err != nil {
		t.Fatalf("SubmitRun (parent) failed: %v", err)
	}
	parentNumber, parentCounter := buildEnvOf(t, st, parentRunID, "build")

	rerunID, err := st.SubmitRun(SubmitRunParams{
		Name:         "original-run (rerun)",
		OrgID:        "org-rerun",
		ProjectID:    "proj-rerun",
		PipelineName: "ci",
		Ref:          "refs/heads/main",
		CommitSHA:    "deadbeef",
		SCMProvider:  "github",
		ParentRunID:  parentRunID,
		Steps:        []api.StepDef{{ID: "build", Image: "alpine"}},
	})
	if err != nil {
		t.Fatalf("SubmitRun (rerun) failed: %v", err)
	}

	rerunNumber, rerunCounter := buildEnvOf(t, st, rerunID, "build")
	if rerunNumber != parentNumber {
		t.Errorf("rerun got a different build number than its parent: parent=%q rerun=%q", parentNumber, rerunNumber)
	}
	if rerunCounter != parentCounter {
		t.Errorf("rerun got a different build counter than its parent: parent=%q rerun=%q", parentCounter, rerunCounter)
	}
}

// TestSubmitRun_BuildVariables_ChildPipeline covers a type: pipeline
// child run (both ParentRunID and ParentJobID set — see SubmitRun's
// idempotency check on ParentJobID). Like a rerun, a child run must
// inherit its parent's exact build number rather than minting its own,
// so a build number identifies one top-level build across everything
// it triggers, not just the one run that happened to assign it.
func TestSubmitRun_BuildVariables_ChildPipeline(t *testing.T) {
	st := openTestDB(t)
	insertTestProject(t, st, "org-child", "proj-child")

	parentRunID, err := st.SubmitRun(SubmitRunParams{
		Name:         "parent-run",
		OrgID:        "org-child",
		ProjectID:    "proj-child",
		PipelineName: "ci",
		Ref:          "refs/heads/main",
		CommitSHA:    "deadbeef",
		SCMProvider:  "github",
		Steps:        []api.StepDef{{ID: "trigger-child", Image: "alpine"}},
	})
	if err != nil {
		t.Fatalf("SubmitRun (parent) failed: %v", err)
	}
	parentNumber, parentCounter := buildEnvOf(t, st, parentRunID, "trigger-child")

	var parentJobID string
	if err := st.db.QueryRow(
		`SELECT id FROM jobs WHERE run_id = $1 AND step_id = $2`, parentRunID, "trigger-child",
	).Scan(&parentJobID); err != nil {
		t.Fatalf("query parent job id: %v", err)
	}

	childRunID, err := st.SubmitRun(SubmitRunParams{
		Name:         "child-pipeline-run",
		OrgID:        "org-child",
		ProjectID:    "proj-child",
		PipelineName: "child-ci",
		Ref:          "refs/heads/main",
		CommitSHA:    "deadbeef",
		SCMProvider:  "github",
		ParentRunID:  parentRunID,
		ParentJobID:  parentJobID,
		Steps:        []api.StepDef{{ID: "build", Image: "alpine"}},
	})
	if err != nil {
		t.Fatalf("SubmitRun (child pipeline) failed: %v", err)
	}

	childNumber, childCounter := buildEnvOf(t, st, childRunID, "build")
	if childNumber != parentNumber {
		t.Errorf("child pipeline run got a different build number than its parent: parent=%q child=%q", parentNumber, childNumber)
	}
	if childCounter != parentCounter {
		t.Errorf("child pipeline run got a different build counter than its parent: parent=%q child=%q", parentCounter, childCounter)
	}
}
