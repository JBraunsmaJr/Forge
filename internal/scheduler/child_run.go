package scheduler

import (
	"errors"
	"fmt"
	"net/http"
	"strings"

	"github.com/JBraunsmaJr/forge/internal/api"
	"github.com/JBraunsmaJr/forge/internal/childrun"
	"github.com/JBraunsmaJr/forge/internal/gitcache"
)

// handleStartChildRun starts the pipeline of another project as a child of
// the pipeline step named in the URL. The agent executing that step calls
// it.
//
// What the caller controls is deliberately narrow. The parent run, and with
// it the organization, come from the job in the database. The target must
// be a project of that same organization. The child runs the pipeline file
// that project has configured, from a branch or tag of its own repository,
// with its own secrets and policies; the parent can only choose the branch
// or tag and pass variables that are not reserved for Forge.
func (s *Server) handleStartChildRun(w http.ResponseWriter, r *http.Request) {
	jobID := r.PathValue("id")

	var req api.ChildRunRequest
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON")
		return
	}
	target := childrun.Target{Project: req.Project, Ref: req.Ref, FallbackRef: req.FallbackRef}
	if err := target.Validate(false); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if err := childrun.ValidateVariables(req.Variables); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}

	parent, err := s.store.ChildRunParent(jobID)
	if errors.Is(err, errChildRunParent) {
		writeError(w, http.StatusConflict, err.Error())
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if parent.OrgID == "" {
		writeError(w, http.StatusForbidden, "the parent run belongs to no organization, so it cannot start another project's pipeline")
		return
	}
	if parent.Depth >= maxChildRunDepth {
		writeError(w, http.StatusBadRequest, fmt.Sprintf("child runs are nested more than %d deep; check for projects that start each other", maxChildRunDepth))
		return
	}

	// One answer for "does not exist" and "exists in another organization",
	// so the endpoint cannot be used to probe for other teams' projects.
	proj, _, scmToken, ok := s.projects.GetProject(target.Project)
	if !ok || proj.OrgID != parent.OrgID {
		writeError(w, http.StatusNotFound, fmt.Sprintf("project %q not found in the parent run's organization", target.Project))
		return
	}

	if err := s.gitCache.Sync(proj.RepoURL, scmToken); err != nil {
		fmt.Printf("[child-run] syncing %s failed: %v\n", proj.Name, err)
		writeError(w, http.StatusBadGateway, fmt.Sprintf("could not update the repository of project %q", proj.Name))
		return
	}

	ref, commitSHA, err := s.resolveChildRef(proj.RepoURL, target, parent.Ref)
	if err != nil {
		writeError(w, http.StatusUnprocessableEntity, fmt.Sprintf("project %q: %v", proj.Name, err))
		return
	}
	branch := strings.TrimPrefix(strings.TrimPrefix(ref, "refs/heads/"), "refs/tags/")

	meta := api.WebhookRunMeta{
		Provider:  "manual",
		Event:     "pipeline",
		RepoURL:   proj.RepoURL,
		RepoName:  proj.Name,
		Branch:    branch,
		Ref:       ref,
		CommitSHA: commitSHA,
		CommitMsg: "Started by a pipeline step",
		Author:    "Forge",
		Parent: &api.ParentRunLink{
			RunID:         parent.RunID,
			JobID:         jobID,
			Variables:     req.Variables,
			ArtifactsSend: req.ArtifactsSend,
		},
	}

	runID, err := s.triggerWebhookRun(proj, proj.RepoURL, branch, commitSHA, "", scmToken, meta, true)
	if err != nil {
		writeError(w, http.StatusUnprocessableEntity, fmt.Sprintf("project %q: %v", proj.Name, err))
		return
	}

	s.AuditLog(r, "run.child", "project", proj.ID, map[string]string{
		"parent_run_id": parent.RunID,
		"parent_job_id": jobID,
		"child_run_id":  runID,
		"ref":           ref,
	})
	writeJSON(w, http.StatusCreated, api.ChildRunResponse{
		RunID:     runID,
		Project:   proj.Name,
		Ref:       ref,
		CommitSHA: commitSHA,
	})
}

// resolveChildRef picks the branch or tag of the child repository to run:
// the requested one, or the parent run's own when none was requested, or
// the fallback when that does not exist in the child repository.
func (s *Server) resolveChildRef(repoURL string, target childrun.Target, parentRef string) (ref, commit string, err error) {
	wanted := target.Ref
	if wanted == "" {
		wanted = parentRef
	}

	if wanted != "" && childrun.ValidRefName(wanted) {
		ref, commit, err = s.gitCache.ResolveBranchOrTag(repoURL, wanted)
		if err == nil {
			return ref, commit, nil
		}
		if !errors.Is(err, gitcache.ErrRefNotFound) {
			return "", "", err
		}
	}

	if target.FallbackRef != "" {
		ref, commit, err = s.gitCache.ResolveBranchOrTag(repoURL, target.FallbackRef)
		if err == nil {
			return ref, commit, nil
		}
		if !errors.Is(err, gitcache.ErrRefNotFound) {
			return "", "", err
		}
		return "", "", fmt.Errorf("neither %q nor the fallback %q exists in its repository", shortRef(wanted), target.FallbackRef)
	}
	if wanted == "" {
		return "", "", errors.New("the parent run has no branch or tag, so the step must set 'ref'")
	}
	return "", "", fmt.Errorf("%q does not exist in its repository; set 'fallback_ref' to run another branch in that case", shortRef(wanted))
}

// shortRef turns "refs/heads/main" into "main" for messages.
func shortRef(ref string) string {
	return strings.TrimPrefix(strings.TrimPrefix(ref, "refs/heads/"), "refs/tags/")
}
