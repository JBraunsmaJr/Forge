package scheduler

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/JBraunsmaJr/forge/internal/api"
	"github.com/JBraunsmaJr/forge/internal/compiler"
	policyengine "github.com/JBraunsmaJr/forge/internal/policy"
	"github.com/JBraunsmaJr/forge/internal/scm"
)

type githubPushPayload struct {
	Ref        string `json:"ref"`   // e.g. "refs/heads/main"
	After      string `json:"after"` // commit SHA
	HeadCommit struct {
		Message string `json:"message"`
		Author  struct {
			Name string `json:"name"`
		} `json:"author"`
	} `json:"head_commit"`
	Repository struct {
		CloneURL string `json:"clone_url"`
		FullName string `json:"full_name"` // e.g. "JBraunsmaJr/forge"
	} `json:"repository"`
}

type githubPRPayload struct {
	Action      string `json:"action"` // "opened", "synchronize", "reopened"
	PullRequest struct {
		Number int `json:"number"`
		Head   struct {
			Ref string `json:"ref"`
			SHA string `json:"sha"`
		} `json:"head"`
		Base struct {
			Ref string `json:"ref"`
		} `json:"base"`
		Title string `json:"title"`
		User  struct {
			Login string `json:"login"`
		} `json:"user"`
	} `json:"pull_request"`
	Repository struct {
		CloneURL string `json:"clone_url"`
		FullName string `json:"full_name"`
	} `json:"repository"`
}

func (s *Server) handleGitHubWebhook(w http.ResponseWriter, r *http.Request) {
	projectID := r.PathValue("id")

	proj, secret, scmToken, ok := s.projects.GetProject(projectID)
	if !ok {
		writeError(w, http.StatusNotFound, "project not found")
		return
	}

	body, err := io.ReadAll(io.LimitReader(r.Body, 4<<20))
	if err != nil {
		writeError(w, http.StatusBadRequest, "reading body")
		return
	}

	sig := r.Header.Get("X-Hub-Signature-256")
	if !verifyGitHubSig(body, secret, sig) {
		writeError(w, http.StatusForbidden, "invalid signature")
		return
	}

	event := r.Header.Get("X-GitHub-Event")
	if event != "push" && event != "pull_request" {
		w.WriteHeader(http.StatusNoContent)
		return
	}

	var branch, ref, commitSHA, repoURL, repoFullName, commitMsg, author string
	var prNumber int

	if event == "push" {
		branch, ref, commitSHA, repoURL, repoFullName, commitMsg, author, err = parseGitHubPush(body)
	} else {
		branch, ref, commitSHA, repoURL, repoFullName, commitMsg, author, prNumber, err = parseGitHubPR(body)
		if err == nil {
			// Only trigger on opened or updated PRs.
			var payload githubPRPayload
			json.Unmarshal(body, &payload)
			if payload.Action != "opened" && payload.Action != "synchronize" && payload.Action != "reopened" {
				w.WriteHeader(http.StatusNoContent)
				return
			}
		}
	}
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON")
		return
	}

	if len(proj.BranchFilter) > 0 && !matchesBranchFilter(branch, proj.BranchFilter) {
		fmt.Printf("[webhook] skipping %s to branch %q (not in filter %v)\n",
			event, branch, proj.BranchFilter)
		w.WriteHeader(http.StatusNoContent)
		return
	}

	rawURL := fmt.Sprintf("https://raw.githubusercontent.com/%s/%s/%s",
		repoFullName, commitSHA, proj.PipelinePath)

	meta := api.WebhookRunMeta{
		Provider:  "github",
		Event:     event,
		RepoURL:   repoURL,
		RepoName:  repoFullName,
		Branch:    branch,
		Ref:       ref,
		CommitSHA: commitSHA,
		CommitMsg: commitMsg,
		Author:    author,
		PRNumber:  prNumber,
	}

	runID, err := s.triggerWebhookRun(proj, repoURL, branch, commitSHA, rawURL, scmToken, meta, false)
	if err != nil {
		fmt.Printf("[webhook] github trigger failed: %v\n", err)
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}

	fmt.Printf("[webhook] github %s → run %s (branch: %s commit: %.8s)\n",
		event, runID[:8], branch, commitSHA)
	writeJSON(w, http.StatusCreated, map[string]string{"run_id": runID})
}

func verifyGitHubSig(body []byte, secret, signature string) bool {
	if !strings.HasPrefix(signature, "sha256=") {
		return false
	}
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write(body)
	expected := "sha256=" + hex.EncodeToString(mac.Sum(nil))

	return hmac.Equal([]byte(expected), []byte(signature))
}

type gitlabPushPayload struct {
	Ref     string `json:"ref"`
	After   string `json:"after"`
	Commits []struct {
		Message string `json:"message"`
		Author  struct {
			Name string `json:"name"`
		} `json:"author"`
	} `json:"commits"`
	Project struct {
		HTTPURLToRepo     string `json:"http_url_to_repo"`
		PathWithNamespace string `json:"path_with_namespace"` // e.g. "user/repo"
	} `json:"project"`
}

type gitlabMRPayload struct {
	ObjectKind       string `json:"object_kind"` // "merge_request"
	ObjectAttributes struct {
		SourceBranch    string `json:"source_branch"`
		SourceProjectID int    `json:"source_project_id"`
		LastCommit      struct {
			ID      string `json:"id"`
			Message string `json:"message"`
			Author  struct {
				Name string `json:"name"`
			} `json:"author"`
		} `json:"last_commit"`
		WorkInProgress bool   `json:"work_in_progress"`
		Action         string `json:"action"` // "open", "update", "reopen"
		Title          string `json:"title"`
		IID            int    `json:"iid"`
	} `json:"object_attributes"`
	Project struct {
		HTTPURLToRepo     string `json:"http_url_to_repo"`
		PathWithNamespace string `json:"path_with_namespace"`
	} `json:"project"`
	User struct {
		Name string `json:"name"`
	} `json:"user"`
}

func (s *Server) handleGitLabWebhook(w http.ResponseWriter, r *http.Request) {
	projectID := r.PathValue("id")

	proj, secret, scmToken, ok := s.projects.GetProject(projectID)
	if !ok {
		writeError(w, http.StatusNotFound, "project not found")
		return
	}

	if r.Header.Get("X-Gitlab-Token") != secret {
		writeError(w, http.StatusForbidden, "invalid token")
		return
	}

	event := r.Header.Get("X-Gitlab-Event")
	if event != "Push Hook" && event != "Merge Request Hook" {
		w.WriteHeader(http.StatusNoContent)
		return
	}

	body, _ := io.ReadAll(io.LimitReader(r.Body, 4<<20))

	var branch, ref, commitSHA, repoURL, repoFullName, commitMsg, author, eventType string
	var prNumber int
	var err error

	if event == "Push Hook" {
		branch, ref, commitSHA, repoURL, repoFullName, commitMsg, author, err = parseGitLabPush(body)
		eventType = "push"
	} else {
		branch, ref, commitSHA, repoURL, repoFullName, commitMsg, author, prNumber, err = parseGitLabMR(body)
		if err == nil {
			var payload gitlabMRPayload
			json.Unmarshal(body, &payload)

			action := payload.ObjectAttributes.Action
			if action != "open" && action != "update" && action != "reopen" {
				w.WriteHeader(http.StatusNoContent)
				return
			}
			if payload.ObjectAttributes.WorkInProgress {
				w.WriteHeader(http.StatusNoContent)
				return
			}
		}
		eventType = "merge_request"
	}
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON")
		return
	}

	rawURL := fmt.Sprintf("https://gitlab.com/%s/-/raw/%s/%s",
		repoFullName, commitSHA, proj.PipelinePath)

	meta := api.WebhookRunMeta{
		Provider:  "gitlab",
		Event:     eventType,
		RepoURL:   repoURL,
		RepoName:  repoFullName,
		Branch:    branch,
		Ref:       ref,
		CommitSHA: commitSHA,
		CommitMsg: commitMsg,
		Author:    author,
		PRNumber:  prNumber,
	}

	runID, err := s.triggerWebhookRun(proj, repoURL, branch, commitSHA, rawURL, scmToken, meta, false)
	if err != nil {
		fmt.Printf("[webhook] gitlab trigger failed: %v\n", err)
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}

	fmt.Printf("[webhook] gitlab %s → run %s (branch: %s commit: %.8s)\n",
		eventType, runID[:8], branch, commitSHA)
	writeJSON(w, http.StatusCreated, map[string]string{"run_id": runID})
}

func (s *Server) handleGenericWebhook(w http.ResponseWriter, r *http.Request) {
	projectID := r.PathValue("id")

	proj, _, scmToken, ok := s.projects.GetProject(projectID)
	if !ok {
		writeError(w, http.StatusNotFound, "project not found")
		return
	}

	branch := r.URL.Query().Get("branch")
	if branch == "" {
		branch = "main"
	}
	commitSHA := r.URL.Query().Get("commit")
	if commitSHA == "" {
		commitSHA = "HEAD"
	}

	rawURL := fmt.Sprintf("%s/raw/%s/%s", strings.TrimSuffix(proj.RepoURL, ".git"), commitSHA, proj.PipelinePath)

	meta := api.WebhookRunMeta{
		Provider:  "generic",
		RepoURL:   proj.RepoURL,
		RepoName:  proj.Name,
		Branch:    branch,
		CommitSHA: commitSHA,
	}

	runID, err := s.triggerWebhookRun(proj, proj.RepoURL, branch, commitSHA, rawURL, scmToken, meta, false)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusCreated, map[string]string{"run_id": runID})
}

// triggerWebhookRun fetches the pipeline file from the SCM, compiles it,
// injects a checkout step, and submits a run.
func (s *Server) triggerWebhookRun(
	proj *api.ProjectInfo,
	repoURL, branch, commitSHA string,
	pipelineRawURL, scmToken string,
	meta api.WebhookRunMeta,
	skipSync bool,
) (string, error) {

	if !skipSync {
		if err := s.gitCache.SyncCommit(repoURL, scmToken, commitSHA); err != nil {
			return "", fmt.Errorf("syncing repo: %w", err)
		}
	}

	pipelinePath := proj.PipelinePath
	var pipelineJSON []byte
	var err error

	// If no path is specified, or if the legacy default path is used, we try multiple possible locations.
	if pipelinePath == "" || pipelinePath == ".forge/pipeline.json" {
		defaults := []string{".forge/pipeline.yml", ".forge/pipeline.yaml", ".forge/pipeline.json"}
		found := false
		var lastErr error
		for _, p := range defaults {
			pipelineJSON, err = s.gitCache.ReadFile(repoURL, commitSHA, p)
			if err == nil {
				pipelinePath = p
				found = true
				break
			}
			lastErr = err
		}
		if !found {
			return "", fmt.Errorf("pipeline file not found at any of the default locations (.forge/pipeline.{yml,yaml,json}). Last error: %w", lastErr)
		}
	} else {
		pipelineJSON, err = s.gitCache.ReadFile(repoURL, commitSHA, pipelinePath)
		if err != nil {
			return "", fmt.Errorf("reading pipeline file %s from cache: %w", pipelinePath, err)
		}
	}

	pipeline, err := compiler.CompileData(pipelineJSON, pipelinePath)
	if err != nil {
		return "", fmt.Errorf("compiling pipeline: %w", err)
	}

	steps := make([]api.StepDef, len(pipeline.Steps))
	for i, s := range pipeline.Steps {
		steps[i] = s.ToAPIStep(nil)
		injectSCMMetadata(steps[i].Env, meta)
	}

	runID := newID()
	workspaceDir := filepath.Join(os.TempDir(), "forge-ws-"+runID[:12])
	if err := os.MkdirAll(workspaceDir, 0755); err != nil {
		return "", fmt.Errorf("creating workspace: %w", err)
	}
	defer os.RemoveAll(workspaceDir)

	if err := s.extractSourceToDir(repoURL, commitSHA, workspaceDir); err != nil {
		return "", fmt.Errorf("populating workspace from cache: %w", err)
	}

	if proj.OrgID != "" {
		steps, err = s.applyWebhookPolicies(steps, proj.OrgID, pipeline.Name, workspaceDir)
		if err != nil {
			return "", fmt.Errorf("policy injection: %w", err)
		}
	}

	checkoutStep := api.StepDef{
		ID:    "_forge_checkout",
		Image: "alpine:3.20",
		Command: []string{"sh", "-c", fmt.Sprintf(
			"apk add --no-cache curl >/dev/null && "+
				"curl -fL -H \"Authorization: Bearer $FORGE_API_TOKEN\" "+
				"\"$FORGE_SCHEDULER_URL/api/v1/source/%s?commit=%s\" > /tmp/repo.tar.gz && "+
				"tar -xzvf /tmp/repo.tar.gz -C /workspace && rm /tmp/repo.tar.gz",
			proj.ID, commitSHA,
		)},
		WorkDir: "/",
		Timeout: 5 * time.Minute,
	}

	for i := range steps {
		if len(steps[i].DependsOn) == 0 {
			steps[i].DependsOn = []string{"_forge_checkout"}
		}
	}
	steps = append([]api.StepDef{checkoutStep}, steps...)

	if err := validateSteps(steps); err != nil {
		return "", fmt.Errorf("invalid pipeline after policy injection: %w", err)
	}

	steps = PruneSteps(steps, meta.Ref)

	runName := fmt.Sprintf("%s @ %.8s [%s]", pipeline.Name, commitSHA, branch)

	var appliedStepIDs []string
	for _, s := range steps {
		appliedStepIDs = append(appliedStepIDs, s.ID)
	}

	submittedID, err := s.store.SubmitRunWithID(runID, runName, "", proj.OrgID, proj.ID, meta.Ref, commitSHA, meta.Provider, "", steps, appliedStepIDs, "", "", nil)
	if err != nil {
		return "", fmt.Errorf("submitting run: %w", err)
	}

	runsTotal.WithLabelValues(proj.OrgID, proj.ID, "webhook").Inc()
	jobsSubmittedTotal.WithLabelValues(proj.OrgID, proj.ID).Add(float64(len(steps)))

	s.publishRunDetail(submittedID)

	// Report pending status to SCM
	targetURL := fmt.Sprintf("%s/runs/%s", s.baseURL, submittedID)
	description := "Forge CI — pipeline started"
	if err := scm.PostStatus(meta.Provider, repoURL, commitSHA, scmToken, "pending", targetURL, description, "forge/ci"); err != nil {
		fmt.Printf("[webhook] failed to post pending status to %s: %v\n", meta.Provider, err)
	}

	return submittedID, nil
}

func fetchRawFile(url, token string) ([]byte, error) {
	req, err := http.NewRequest("GET", url, nil)
	if err != nil {
		return nil, err
	}
	if token != "" {

		req.Header.Set("Authorization", "Bearer "+token)
		req.Header.Set("PRIVATE-TOKEN", token)
	}

	client := &http.Client{Timeout: 10 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("HTTP %d from %s", resp.StatusCode, url)
	}
	return io.ReadAll(io.LimitReader(resp.Body, 1<<20))
}

// applyPoliciesForWebhook applies org policies before submitting a webhook run.
func (s *Server) applyWebhookPolicies(steps []api.StepDef, orgID, pipelineName, workspaceDir string) ([]api.StepDef, error) {
	if orgID == "" {
		return steps, nil
	}
	policies, ok := s.orgs.GetPolicies(orgID)
	if !ok || len(policies) == 0 {
		return steps, nil
	}
	return policyengine.Apply(steps, policies, pipelineName, workspaceDir, orgID, nil)
}

func (s *Server) extractSourceToDir(repoURL, commit, dir string) error {
	pr, pw := io.Pipe()

	errChan := make(chan error, 1)
	go func() {
		defer pw.Close()
		errChan <- s.gitCache.WriteArchive(repoURL, commit, pw)
	}()

	cmd := exec.Command("tar", "-xz", "-C", dir)
	cmd.Stdin = pr
	if output, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("tar extract failed: %w, output: %s", err, string(output))
	}

	return <-errChan
}

// matchesBranchFilter returns true if the branch matches any pattern in filters.
// Patterns support a single "*" as a suffix wildcard (e.g. "release/*").
func matchesBranchFilter(branch string, filters []string) bool {
	for _, f := range filters {
		if f == branch {
			return true
		}
		if before, ok := strings.CutSuffix(f, "/*"); ok {
			prefix := before
			if strings.HasPrefix(branch, prefix+"/") {
				return true
			}
		}
		if before, ok := strings.CutSuffix(f, "*"); ok {
			prefix := before
			if strings.HasPrefix(branch, prefix) {
				return true
			}
		}
	}
	return false
}

func injectSCMMetadata(env map[string]string, meta api.WebhookRunMeta) {
	env["FORGE_REPO_URL"] = meta.RepoURL
	env["FORGE_REPO_NAME"] = meta.RepoName
	env["FORGE_BRANCH"] = meta.Branch
	env["FORGE_REF"] = meta.Ref
	env["FORGE_COMMIT_SHA"] = meta.CommitSHA
	env["FORGE_EVENT"] = meta.Event
	if after, ok := strings.CutPrefix(meta.Ref, "refs/tags/"); ok {
		env["FORGE_COMMIT_TAG"] = after
	}
	if meta.PRNumber > 0 {
		env["FORGE_PR_NUMBER"] = fmt.Sprintf("%d", meta.PRNumber)
	}
}

func parseGitHubPush(body []byte) (branch, ref, commitSHA, repoURL, repoFullName, commitMsg, author string, err error) {
	var payload githubPushPayload
	if err := json.Unmarshal(body, &payload); err != nil {
		return "", "", "", "", "", "", "", err
	}
	ref = payload.Ref
	branch = strings.TrimPrefix(ref, "refs/heads/")
	branch = strings.TrimPrefix(branch, "refs/tags/")
	commitSHA = payload.After
	repoURL = payload.Repository.CloneURL
	repoFullName = payload.Repository.FullName
	commitMsg = payload.HeadCommit.Message
	author = payload.HeadCommit.Author.Name
	return
}

func parseGitHubPR(body []byte) (branch, ref, commitSHA, repoURL, repoFullName, commitMsg, author string, prNumber int, err error) {
	var payload githubPRPayload
	if err := json.Unmarshal(body, &payload); err != nil {
		return "", "", "", "", "", "", "", 0, err
	}
	branch = payload.PullRequest.Head.Ref
	ref = "refs/pull/" + fmt.Sprintf("%d", payload.PullRequest.Number) + "/head"
	commitSHA = payload.PullRequest.Head.SHA
	repoURL = payload.Repository.CloneURL
	repoFullName = payload.Repository.FullName
	commitMsg = payload.PullRequest.Title
	author = payload.PullRequest.User.Login
	prNumber = payload.PullRequest.Number
	return
}

func parseGitLabPush(body []byte) (branch, ref, commitSHA, repoURL, repoFullName, commitMsg, author string, err error) {
	var payload gitlabPushPayload
	if err := json.Unmarshal(body, &payload); err != nil {
		return "", "", "", "", "", "", "", err
	}
	ref = payload.Ref
	branch = strings.TrimPrefix(ref, "refs/heads/")
	branch = strings.TrimPrefix(branch, "refs/tags/")
	commitSHA = payload.After
	repoURL = payload.Project.HTTPURLToRepo
	repoFullName = payload.Project.PathWithNamespace
	if len(payload.Commits) > 0 {
		commitMsg = payload.Commits[0].Message
		author = payload.Commits[0].Author.Name
	}
	return
}

func parseGitLabMR(body []byte) (branch, ref, commitSHA, repoURL, repoFullName, commitMsg, author string, prNumber int, err error) {
	var payload gitlabMRPayload
	if err := json.Unmarshal(body, &payload); err != nil {
		return "", "", "", "", "", "", "", 0, err
	}
	branch = payload.ObjectAttributes.SourceBranch
	ref = "refs/merge-requests/" + fmt.Sprintf("%d", payload.ObjectAttributes.IID) + "/head"
	commitSHA = payload.ObjectAttributes.LastCommit.ID
	repoURL = payload.Project.HTTPURLToRepo
	repoFullName = payload.Project.PathWithNamespace
	commitMsg = payload.ObjectAttributes.Title
	author = payload.User.Name
	prNumber = payload.ObjectAttributes.IID
	return
}
