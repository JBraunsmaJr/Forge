package scheduler

import (
	"testing"

	"github.com/JBraunsmaJr/forge/internal/api"
)

// We use a real DB because the Store is tightly coupled to it.
func TestSubmitWebhookRun_EmptyWorkspace(t *testing.T) {
	st := openTestDB(t)

	proj := &api.ProjectInfo{
		ID:    "proj-123",
		OrgID: "org-456",
	}
	steps := []api.StepDef{
		{ID: "step1", Image: "alpine"},
	}

	runID := "run-webhook-1"

	st.db.Exec(`INSERT INTO orgs (id, name) VALUES ($1, $2)`, proj.OrgID, "Test Org")

	id, err := st.SubmitRun(SubmitRunParams{
		RunID:       runID,
		Name:        "Test Run",
		OrgID:       proj.OrgID,
		ProjectID:   proj.ID,
		Ref:         "refs/heads/main",
		CommitSHA:   "deadbeef",
		SCMProvider: "github",
		Steps:       steps,
	})
	if err != nil {
		t.Fatalf("SubmitRun failed: %v", err)
	}

	// Verify the workspace in the DB
	var workspace string
	err = st.db.QueryRow(`SELECT workspace_dir FROM runs WHERE id = $1`, id).Scan(&workspace)
	if err != nil {
		t.Fatalf("failed to query workspace_dir: %v", err)
	}

	if workspace != "" {
		t.Errorf("expected empty workspace_dir for webhook run, got %q", workspace)
	}
}
