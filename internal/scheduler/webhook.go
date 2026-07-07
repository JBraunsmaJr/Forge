// Package scheduler — SCM webhook handlers.
//
// Webhook-triggered runs flow:
//
//  1. GitHub/GitLab sends a push event to /api/v1/webhook/{provider}/{projectID}
//  2. Forge verifies the HMAC signature using the project's webhook_secret
//  3. Forge fetches .forge/pipeline.json from the commit (via raw CDN URL)
//  4. Forge compiles the pipeline and injects a git checkout step first
//  5. A run is submitted to the scheduler — the pipeline runs normally from here
//
// # Workspace for webhook runs
//
// The scheduler creates a temp directory at submission time. The injected
// checkout step clones the repo into it. Subsequent steps mount the same
// directory. This works when scheduler and agent run on the same host.
// Distributed setups need a shared filesystem (NFS, Docker volume, etc.).
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
)

// ── GitHub handler ────────────────────────────────────────────────────────────

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

func (s *Server) handleGitHubWebhook(w http.ResponseWriter, r *http.Request) {
	projectID := r.PathValue("id")

	proj, secret, scmToken, ok := s.projects.GetProject(projectID)
	if !ok {
		writeError(w, http.StatusNotFound, "project not found")
		return
	}

	// Read body first (needed for signature verification).
	body, err := io.ReadAll(io.LimitReader(r.Body, 4<<20))
	if err != nil {
		writeError(w, http.StatusBadRequest, "reading body")
		return
	}

	// Verify HMAC-SHA256 signature.
	sig := r.Header.Get("X-Hub-Signature-256")
	if !verifyGitHubSig(body, secret, sig) {
		writeError(w, http.StatusForbidden, "invalid signature")
		return
	}

	// Only act on push events.
	if r.Header.Get("X-GitHub-Event") != "push" {
		w.WriteHeader(http.StatusNoContent)
		return
	}

	var payload githubPushPayload
	if err := json.Unmarshal(body, &payload); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON")
		return
	}

	branch := strings.TrimPrefix(payload.Ref, "refs/heads/")
	commitSHA := payload.After
	repoURL := payload.Repository.CloneURL

	// Branch filter: skip if project has filters and this branch doesn't match.
	if len(proj.BranchFilter) > 0 && !matchesBranchFilter(branch, proj.BranchFilter) {
		fmt.Printf("[webhook] skipping push to branch %q (not in filter %v)\n",
			branch, proj.BranchFilter)
		w.WriteHeader(http.StatusNoContent)
		return
	}

	// Fetch the pipeline file from the commit using GitHub's raw CDN.
	rawURL := fmt.Sprintf("https://raw.githubusercontent.com/%s/%s/%s",
		payload.Repository.FullName, commitSHA, proj.PipelinePath)

	meta := api.WebhookRunMeta{
		Provider:  "github",
		RepoURL:   repoURL,
		RepoName:  payload.Repository.FullName,
		Branch:    branch,
		CommitSHA: commitSHA,
		CommitMsg: payload.HeadCommit.Message,
		Author:    payload.HeadCommit.Author.Name,
	}

	runID, err := s.triggerWebhookRun(proj, repoURL, branch, commitSHA, rawURL, scmToken, meta)
	if err != nil {
		fmt.Printf("[webhook] github trigger failed: %v\n", err)
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}

	fmt.Printf("[webhook] github push → run %s (branch: %s commit: %.8s)\n",
		runID[:8], branch, commitSHA)
	writeJSON(w, http.StatusCreated, map[string]string{"run_id": runID})
}

func verifyGitHubSig(body []byte, secret, signature string) bool {
	if !strings.HasPrefix(signature, "sha256=") {
		return false
	}
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write(body)
	expected := "sha256=" + hex.EncodeToString(mac.Sum(nil))
	// constant-time comparison prevents timing attacks
	return hmac.Equal([]byte(expected), []byte(signature))
}

// ── GitLab handler ────────────────────────────────────────────────────────────

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

func (s *Server) handleGitLabWebhook(w http.ResponseWriter, r *http.Request) {
	projectID := r.PathValue("id")

	proj, secret, scmToken, ok := s.projects.GetProject(projectID)
	if !ok {
		writeError(w, http.StatusNotFound, "project not found")
		return
	}

	// GitLab uses a simple token header, not HMAC.
	if r.Header.Get("X-Gitlab-Token") != secret {
		writeError(w, http.StatusForbidden, "invalid token")
		return
	}

	if r.Header.Get("X-Gitlab-Event") != "Push Hook" {
		w.WriteHeader(http.StatusNoContent)
		return
	}

	body, _ := io.ReadAll(io.LimitReader(r.Body, 4<<20))
	var payload gitlabPushPayload
	if err := json.Unmarshal(body, &payload); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON")
		return
	}

	branch := strings.TrimPrefix(payload.Ref, "refs/heads/")
	commitSHA := payload.After
	repoURL := payload.Project.HTTPURLToRepo

	// GitLab raw file URL.
	rawURL := fmt.Sprintf("https://gitlab.com/%s/-/raw/%s/%s",
		payload.Project.PathWithNamespace, commitSHA, proj.PipelinePath)

	var commitMsg, author string
	if len(payload.Commits) > 0 {
		commitMsg = payload.Commits[0].Message
		author = payload.Commits[0].Author.Name
	}

	meta := api.WebhookRunMeta{
		Provider:  "gitlab",
		RepoURL:   repoURL,
		RepoName:  payload.Project.PathWithNamespace,
		Branch:    branch,
		CommitSHA: commitSHA,
		CommitMsg: commitMsg,
		Author:    author,
	}

	runID, err := s.triggerWebhookRun(proj, repoURL, branch, commitSHA, rawURL, scmToken, meta)
	if err != nil {
		fmt.Printf("[webhook] gitlab trigger failed: %v\n", err)
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}

	fmt.Printf("[webhook] gitlab push → run %s (branch: %s commit: %.8s)\n",
		runID[:8], branch, commitSHA)
	writeJSON(w, http.StatusCreated, map[string]string{"run_id": runID})
}

// ── Generic webhook (simple HTTP trigger, no signature) ───────────────────────

func (s *Server) handleGenericWebhook(w http.ResponseWriter, r *http.Request) {
	projectID := r.PathValue("id")

	proj, _, scmToken, ok := s.projects.GetProject(projectID)
	if !ok {
		writeError(w, http.StatusNotFound, "project not found")
		return
	}

	// Generic webhooks accept branch/commit as query params.
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

	runID, err := s.triggerWebhookRun(proj, proj.RepoURL, branch, commitSHA, rawURL, scmToken, meta)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusCreated, map[string]string{"run_id": runID})
}

// ── Core trigger logic ────────────────────────────────────────────────────────

// triggerWebhookRun fetches the pipeline file from the SCM, compiles it,
// injects a checkout step, and submits a run.
func (s *Server) triggerWebhookRun(
	proj *api.ProjectInfo,
	repoURL, branch, commitSHA string,
	pipelineRawURL, scmToken string,
	meta api.WebhookRunMeta,
) (string, error) {

	// Sync repo cache first.
	if err := s.gitCache.Sync(repoURL, scmToken); err != nil {
		return "", fmt.Errorf("syncing repo: %w", err)
	}

	// Fetch pipeline.json from the local cache.
	pipelinePath := proj.PipelinePath
	if pipelinePath == "" {
		pipelinePath = ".forge/pipeline.json"
	}
	pipelineJSON, err := s.gitCache.ReadFile(repoURL, commitSHA, pipelinePath)
	if err != nil {
		return "", fmt.Errorf("reading pipeline file %s from cache: %w", pipelinePath, err)
	}

	// Write to a temp file so the compiler can read it.
	tmp, err := os.CreateTemp("", "forge-pipeline-*.json")
	if err != nil {
		return "", fmt.Errorf("creating temp file: %w", err)
	}
	defer os.Remove(tmp.Name())
	tmp.Write(pipelineJSON)
	tmp.Close()

	// Compile the pipeline.
	pipeline, err := compiler.Compile(tmp.Name())
	if err != nil {
		return "", fmt.Errorf("compiling pipeline: %w", err)
	}

	// Convert pipeline.Steps → []api.StepDef.
	steps := make([]api.StepDef, len(pipeline.Steps))
	for i, s := range pipeline.Steps {
		env := make(map[string]string)
		for k, v := range s.Env {
			env[k] = v
		}
		// Inject SCM metadata
		env["FORGE_REPO_URL"] = meta.RepoURL
		env["FORGE_REPO_NAME"] = meta.RepoName
		env["FORGE_BRANCH"] = meta.Branch
		env["FORGE_COMMIT_SHA"] = meta.CommitSHA

		steps[i] = api.StepDef{
			ID:          s.ID,
			Image:       s.Image,
			Command:     s.Command,
			WorkDir:     s.WorkDir,
			Env:         env,
			DependsOn:   s.DependsOn,
			Inputs:      s.Inputs,
			Timeout:     s.Timeout,
			SecretNames: s.Secrets,
			Type:        s.Type,
		}
	}

	// Create the temp workspace now so its path can be passed to policy transformers
	// (they may inspect workspace files, e.g. to detect languages).
	runID := newID()
	workspaceDir := filepath.Join(os.TempDir(), "forge-ws-"+runID[:12])
	if err := os.MkdirAll(workspaceDir, 0755); err != nil {
		return "", fmt.Errorf("creating workspace: %w", err)
	}
	defer os.RemoveAll(workspaceDir)

	// Populate workspace from cache for policy evaluation.
	if err := s.extractSourceToDir(repoURL, commitSHA, workspaceDir); err != nil {
		return "", fmt.Errorf("populating workspace from cache: %w", err)
	}

	// Apply org policies if this project has an org.
	var appliedPolicies []string
	if proj.OrgID != "" {
		steps, appliedPolicies, err = s.applyWebhookPolicies(steps, proj.OrgID, pipeline.Name, workspaceDir)
		if err != nil {
			return "", fmt.Errorf("policy injection: %w", err)
		}
	}

	// Inject a source checkout step as the FIRST step.
	// This fetches the repo tarball from the scheduler and extracts it into /workspace.
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

	// Make the first real step depend on _forge_checkout if it has no deps.
	for i := range steps {
		if len(steps[i].DependsOn) == 0 {
			steps[i].DependsOn = []string{"_forge_checkout"}
		}
	}
	steps = append([]api.StepDef{checkoutStep}, steps...)

	// Build run name from branch + short commit.
	runName := fmt.Sprintf("%s @ %.8s [%s]", pipeline.Name, commitSHA, branch)

	// Submit using the pre-allocated run ID.
	submittedID, err := s.store.SubmitRunWithID(runID, runName, workspaceDir, proj.OrgID, proj.ID, steps, appliedPolicies)
	if err != nil {
		return "", fmt.Errorf("submitting run: %w", err)
	}

	s.publishRunDetail(submittedID)
	return submittedID, nil
}

// fetchRawFile downloads a file from a URL (e.g. GitHub/GitLab raw CDN).
func fetchRawFile(url, token string) ([]byte, error) {
	req, err := http.NewRequest("GET", url, nil)
	if err != nil {
		return nil, err
	}
	if token != "" {
		// Works for both GitHub (Bearer token) and GitLab (PRIVATE-TOKEN header).
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
	return io.ReadAll(io.LimitReader(resp.Body, 1<<20)) // 1 MB limit
}

// applyPoliciesForWebhook applies org policies before submitting a webhook run.
func (s *Server) applyWebhookPolicies(steps []api.StepDef, orgID, pipelineName, workspaceDir string) ([]api.StepDef, []string, error) {
	if orgID == "" {
		return steps, nil, nil
	}
	policies, ok := s.orgs.GetPolicies(orgID)
	if !ok || len(policies) == 0 {
		return steps, nil, nil
	}
	return policyengine.Apply(steps, policies, pipelineName, workspaceDir, orgID)
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
		if strings.HasSuffix(f, "/*") {
			prefix := strings.TrimSuffix(f, "/*")
			if strings.HasPrefix(branch, prefix+"/") {
				return true
			}
		}
		if strings.HasSuffix(f, "*") {
			prefix := strings.TrimSuffix(f, "*")
			if strings.HasPrefix(branch, prefix) {
				return true
			}
		}
	}
	return false
}
