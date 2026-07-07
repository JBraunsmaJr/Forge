package scheduler

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"strings"
	"time"

	_ "embed"

	"github.com/JBraunsmaJr/forge/internal/api"
	"github.com/JBraunsmaJr/forge/internal/artifacts"
	"github.com/JBraunsmaJr/forge/internal/gitcache"
	policyengine "github.com/JBraunsmaJr/forge/internal/policy"
)

//go:embed web/index.html
var indexHTML []byte

// Server is the HTTP server for the Forge scheduler.
type Server struct {
	store       *Store
	orgs        *OrgStore
	projects    *ProjectStore
	debug       *DebugStore
	tokens      *tokenStore
	artifacts   artifacts.ArtifactStorer
	broker      *SSEBroker
	gitCache    *gitcache.Cache
	internalURL string
	addr        string
	server      *http.Server
}

// NewServer creates a scheduler server backed by the given Postgres database.
func NewServer(addr string, db *sql.DB, baseURL string) *Server {
	artifactDir := getenv("FORGE_ARTIFACT_DIR", "/data/artifacts")
	var artStore artifacts.ArtifactStorer
	if getenv("FORGE_ARTIFACT_STORE", "local") == "s3" {
		cfg := artifacts.ConfigFromEnv(baseURL)
		s3, err := artifacts.NewS3(db, cfg)
		if err != nil {
			fmt.Printf("[scheduler] S3 artifact store failed (%v), falling back to local\n", err)
			artStore, _ = artifacts.NewLocal(db, artifactDir, baseURL)
		} else {
			artStore = s3
			fmt.Printf("[scheduler] artifact store: S3 (%s/%s)\n",
				cfg.S3Endpoint, cfg.S3Bucket)
		}
	} else {
		var err error
		artStore, err = artifacts.NewLocal(db, artifactDir, baseURL)
		if err != nil {
			fmt.Printf("[scheduler] artifact store error: %v\n", err)
		} else {
			fmt.Printf("[scheduler] artifact store: local (%s)\n", artifactDir)
		}
	}
	cacheDir := os.Getenv("FORGE_GIT_CACHE")
	if cacheDir == "" {
		cacheDir = "/tmp/forge-git-cache"
	}
	gc, err := gitcache.New(cacheDir)
	if err != nil {
		fmt.Printf("Warning: failed to initialize git cache: %v\n", err)
	}

	internalURL := os.Getenv("FORGE_INTERNAL_URL")
	if internalURL == "" {
		internalURL = "http://localhost:8080"
	}

	return &Server{
		store:       NewStore(db),
		orgs:        newOrgStore(db),
		projects:    newProjectStore(db),
		debug:       newDebugStore(),
		tokens:      newTokenStore(db),
		broker:      newSSEBroker(),
		artifacts:   artStore,
		gitCache:    gc,
		internalURL: internalURL,
		addr:        addr,
	}
}

func getenv(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

// Start registers all routes and begins serving.
func (s *Server) Start(ctx context.Context) error {
	// Bootstrap: print a root admin token on first ever start.
	s.tokens.bootstrapIfEmpty()

	mux := http.NewServeMux()

	// Web UI — public (must load before the user can authenticate)
	mux.HandleFunc("GET /{$}", s.handleIndex)

	// Token management
	mux.HandleFunc("POST /api/v1/tokens", s.handleCreateToken)
	mux.HandleFunc("GET /api/v1/tokens", s.handleListTokens)
	mux.HandleFunc("DELETE /api/v1/tokens/{id}", s.handleRevokeToken)

	// Project management
	mux.HandleFunc("POST /api/v1/projects", s.handleCreateProject)
	mux.HandleFunc("GET /api/v1/projects", s.handleListProjects)
	mux.HandleFunc("POST /api/v1/projects/{id}/trigger", s.handleManualTrigger)

	// SCM webhooks — HMAC-secured, exempt from token auth
	mux.HandleFunc("POST /api/v1/webhook/github/{id}", s.handleGitHubWebhook)
	mux.HandleFunc("POST /api/v1/webhook/gitlab/{id}", s.handleGitLabWebhook)
	mux.HandleFunc("POST /api/v1/webhook/generic/{id}", s.handleGenericWebhook)

	// Source serving — used by agents
	mux.HandleFunc("GET /api/v1/source/{id}", s.handleServeSource)

	// Org and policy management
	mux.HandleFunc("POST /api/v1/orgs", s.handleCreateOrg)
	mux.HandleFunc("GET /api/v1/orgs", s.handleListOrgs)
	mux.HandleFunc("GET /api/v1/orgs/{id}", s.handleGetOrg)
	mux.HandleFunc("POST /api/v1/orgs/{id}/policies", s.handleCreatePolicy)
	mux.HandleFunc("GET /api/v1/orgs/{id}/policies", s.handleListPolicies)
	mux.HandleFunc("DELETE /api/v1/orgs/{id}/policies/{polID}", s.handleDeletePolicy)

	// Pipeline submission + status (used by CLI)
	mux.HandleFunc("POST /api/v1/runs", s.handleSubmitRun)
	mux.HandleFunc("GET /api/v1/runs/{id}", s.handleRunStatus)
	mux.HandleFunc("POST /api/v1/runs/{id}/cancel", s.handleCancelRun)
	mux.HandleFunc("POST /api/v1/runs/{id}/rerun", s.handleRerun)
	mux.HandleFunc("POST /api/v1/runs/prune", s.handlePruneRuns)

	// Web UI endpoints
	mux.HandleFunc("GET /api/v1/runs", s.handleListRuns)
	mux.HandleFunc("GET /api/v1/runs/{id}/detail", s.handleRunDetail)
	mux.HandleFunc("GET /api/v1/runs/{id}/events", s.handleRunEvents)
	mux.HandleFunc("GET /api/v1/jobs/{id}/logs", s.handleJobLogs)
	mux.HandleFunc("POST /api/v1/jobs/{id}/logs", s.handleAppendJobLogs)
	mux.HandleFunc("GET /api/v1/jobs/{id}/logs/stream", s.handleJobLogStream)

	// Agent protocol
	mux.HandleFunc("POST /api/v1/jobs/lease", s.handleLease)
	mux.HandleFunc("POST /api/v1/jobs/{id}/heartbeat", s.handleHeartbeat)
	mux.HandleFunc("POST /api/v1/jobs/{id}/complete", s.handleComplete)

	// Debug session — browser side
	mux.HandleFunc("POST /api/v1/debug", s.handleCreateDebugSession)
	mux.HandleFunc("GET /api/v1/debug/{id}", s.handleGetDebugSession)
	mux.HandleFunc("GET /api/v1/debug/{id}/stream", s.handleDebugStream)
	mux.HandleFunc("POST /api/v1/debug/{id}/exec", s.handleDebugExec)
	mux.HandleFunc("DELETE /api/v1/debug/{id}", s.handleCloseDebugSession)
	mux.HandleFunc("POST /api/v1/debug/{id}/cancel", s.handleCancelDebugCommand)
	mux.HandleFunc("GET /api/v1/debug/{id}/cancel-check", s.handleDebugCancelCheck)
	// Scheduler proxies debug terminal WebSocket to the agent internally.
	// Browser always connects here — no agent ports exposed publicly.
	mux.HandleFunc("GET /api/v1/debug/{id}/ws", s.handleDebugTerminalProxy)

	// Debug session — agent side
	mux.HandleFunc("POST /api/v1/debug/lease", s.handleDebugLease)
	mux.HandleFunc("POST /api/v1/debug/{id}/container", s.handleDebugRegisterContainer)
	mux.HandleFunc("GET /api/v1/debug/{id}/commands", s.handleDebugPollCommands)
	mux.HandleFunc("POST /api/v1/debug/{id}/output", s.handleDebugSubmitOutput)

	// Flaky test detection
	mux.HandleFunc("GET /api/v1/flaky", s.handleFlakySteps)

	// Artifact Management
	mux.HandleFunc("POST /api/v1/artifacts/presign", s.handlePresignUpload)
	mux.HandleFunc("GET /api/v1/artifacts", s.handleGetArtifact)
	mux.HandleFunc("POST /api/v1/artifacts/{id}/confirm", s.handleConfirmUpload)
	mux.HandleFunc("PUT /api/v1/artifacts/{id}/upload", s.handleArtifactUpload)
	mux.HandleFunc("GET /api/v1/artifacts/{id}/download", s.handleArtifactDownload)
	mux.HandleFunc("DELETE /api/v1/artifacts/{id}", s.handleDeleteArtifact)
	// Artifacts listed as a sub-resource of a run (avoids wildcard conflict).
	mux.HandleFunc("GET /api/v1/runs/{runID}/artifacts", s.handleListArtifacts)

	// Wrap the entire mux with token auth.
	// Public paths (/ and /api/v1/webhook/*) are exempted inside the middleware.
	s.server = &http.Server{Addr: s.addr, Handler: s.authMiddleware(mux)}

	go s.heartbeatMonitor(ctx)
	go s.startRetentionWorker(ctx)

	stopDebug := make(chan struct{})
	go s.debugExpiryMonitor(stopDebug)
	defer close(stopDebug)

	serverErr := make(chan error, 1)
	go func() {
		fmt.Printf("[scheduler] listening on %s\n", s.addr)
		fmt.Printf("[scheduler] web UI → http://localhost%s\n", s.addr)
		if err := s.server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			serverErr <- err
		}
		close(serverErr)
	}()

	select {
	case err := <-serverErr:
		return err
	case <-ctx.Done():
		fmt.Println("[scheduler] draining in-flight requests (up to 5s)...")
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := s.server.Shutdown(shutdownCtx); err != nil {
			return fmt.Errorf("shutdown: %w", err)
		}
		fmt.Println("[scheduler] stopped cleanly")
		return nil
	}
}

func (s *Server) heartbeatMonitor(ctx context.Context) {
	ticker := time.NewTicker(15 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ticker.C:
			if n := s.store.ReclaimStaleJobs(); n > 0 {
				fmt.Printf("[scheduler] reclaimed %d stale job(s)\n", n)
			}
		case <-ctx.Done():
			return
		}
	}
}

// ── Web UI handlers ───────────────────────────────────────────────────────────

func (s *Server) handleIndex(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Write(indexHTML)
}

func (s *Server) handleListRuns(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, s.store.ListRuns())
}

func (s *Server) handleRunDetail(w http.ResponseWriter, r *http.Request) {
	detail, ok := s.store.RunDetail(r.PathValue("id"))
	if !ok {
		writeError(w, http.StatusNotFound, "run not found")
		return
	}
	writeJSON(w, http.StatusOK, detail)
}

// handleRunEvents streams Server-Sent Events to the browser.
// The connection stays open until the client disconnects or the run finishes.
//
// SSE wire format — each event is two lines:
//
//	data: <json>\n
//	\n
//
// The blank line signals the end of one event. The browser's EventSource
// fires an "message" event for each one.
func (s *Server) handleRunEvents(w http.ResponseWriter, r *http.Request) {
	runID := r.PathValue("id")

	// SSE requires these three headers. Without Cache-Control: no-cache,
	// proxies may buffer the stream and the browser sees nothing.
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	// Allow browser to connect from any origin (needed if UI is on a different port).
	w.Header().Set("Access-Control-Allow-Origin", "*")

	// http.Flusher lets us push bytes to the client without waiting for
	// the handler to return. Not all ResponseWriters support this —
	// the type assertion panics if it doesn't, which is fine here since
	// net/http's standard ResponseWriter always implements it.
	flusher, ok := w.(http.Flusher)
	if !ok {
		writeError(w, http.StatusInternalServerError, "streaming not supported")
		return
	}

	// Send the current state immediately so the browser doesn't wait
	// for the first event before rendering anything.
	if detail, ok := s.store.RunDetail(runID); ok {
		data, _ := json.Marshal(detail)
		fmt.Fprintf(w, "data: %s\n\n", data)
		flusher.Flush()
	}

	ch := s.broker.Subscribe(runID)
	defer s.broker.Unsubscribe(runID, ch)

	for {
		select {
		case event, open := <-ch:
			if !open {
				return
			}
			fmt.Fprintf(w, "data: %s\n\n", event)
			flusher.Flush()
		case <-r.Context().Done():
			// Browser closed the tab / navigated away.
			return
		}
	}
}

func (s *Server) handleJobLogs(w http.ResponseWriter, r *http.Request) {
	logs, ok := s.store.GetJobLogs(r.PathValue("id"))
	if !ok {
		writeError(w, http.StatusNotFound, "job not found")
		return
	}
	writeJSON(w, http.StatusOK, logs)
}

// ── CLI / agent handlers ─────────────────────────────────────────────────────

func (s *Server) handleSubmitRun(w http.ResponseWriter, r *http.Request) {
	var req api.SubmitRunRequest
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}

	// ── Policy injection ──────────────────────────────────────────────
	// If an OrgID was provided, load that org's policies and inject
	// mandatory steps before queuing. This happens server-side so users
	// cannot bypass it by omitting steps from their pipeline.json.
	steps := req.Steps
	var appliedPolicies []string

	if req.OrgID != "" {
		policies, ok := s.orgs.GetPolicies(req.OrgID)
		if !ok {
			writeError(w, http.StatusBadRequest, fmt.Sprintf("org %s not found", req.OrgID))
			return
		}
		if len(policies) > 0 {
			var err error
			steps, appliedPolicies, err = policyengine.Apply(
				steps, policies,
				req.PipelineName, req.WorkspaceDir, req.OrgID,
			)
			if err != nil {
				// ForbidOverride conflict — reject the submission.
				writeError(w, http.StatusForbidden, err.Error())
				return
			}
		}
	}

	runID, err := s.store.SubmitRun(req.PipelineName, req.WorkspaceDir, req.OrgID, req.ProjectID, steps, appliedPolicies)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}

	if len(appliedPolicies) > 0 {
		fmt.Printf("[scheduler] run %s: applied policies %v\n", runID[:8], appliedPolicies)
	} else {
		fmt.Printf("[scheduler] run submitted: %s (%s, %d steps)\n",
			runID[:8], req.PipelineName, len(steps))
	}

	s.publishRunDetail(runID)
	writeJSON(w, http.StatusCreated, api.SubmitRunResponse{
		RunID:   runID,
		Message: fmt.Sprintf("run queued with %d steps", len(steps)),
	})
}

func (s *Server) handleRunStatus(w http.ResponseWriter, r *http.Request) {
	status, ok := s.store.RunStatus(r.PathValue("id"))
	if !ok {
		writeError(w, http.StatusNotFound, "run not found")
		return
	}
	writeJSON(w, http.StatusOK, status)
}

func (s *Server) handleLease(w http.ResponseWriter, r *http.Request) {
	var req api.LeaseRequest
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	spec, ok := s.store.LeaseNext(req.AgentID)
	if !ok {
		w.WriteHeader(http.StatusNoContent)
		return
	}
	fmt.Printf("[scheduler] leased job %s to agent %s\n", spec.JobID[:8], req.AgentID[:8])
	s.publishRunDetail(spec.RunID)
	writeJSON(w, http.StatusOK, api.LeaseResponse{Job: spec})
}

func (s *Server) handleHeartbeat(w http.ResponseWriter, r *http.Request) {
	var req api.HeartbeatRequest
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if err := s.store.Heartbeat(r.PathValue("id"), req.LeaseID, req.AgentID); err != nil {
		writeError(w, http.StatusConflict, err.Error())
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) handleComplete(w http.ResponseWriter, r *http.Request) {
	var req api.CompleteRequest
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	runID, err := s.store.Complete(r.PathValue("id"), req.LeaseID, req.ExitCode, req.Duration, req.LogEvents, req.EmittedSteps)
	if err != nil {
		writeError(w, http.StatusConflict, err.Error())
		return
	}
	result := "passed"
	if req.ExitCode != 0 {
		result = "failed"
	}
	fmt.Printf("[scheduler] job %s %s (exit %d)\n", r.PathValue("id")[:8], result, req.ExitCode)

	// Record step outcome using the logical step_id (stable across runs),
	// not the job UUID (unique per run). Flaky detection groups by step_id.
	if detail, ok := s.store.RunDetail(runID); ok {
		stepID := s.store.GetJobStepID(r.PathValue("id"))
		if stepID == "" {
			stepID = r.PathValue("id")
		}
		go s.store.RecordStepResult(runID, detail.Name, stepID, result, req.Duration)
	}

	s.publishRunDetail(runID)
	w.WriteHeader(http.StatusNoContent)
}

// publishRunDetail broadcasts the current run state to all SSE subscribers.
func (s *Server) publishRunDetail(runID string) {
	if detail, ok := s.store.RunDetail(runID); ok {
		data, _ := json.Marshal(detail)
		s.broker.Publish(runID, string(data))
	}
}

// ── Helpers ───────────────────────────────────────────────────────────────────

func decodeJSON(r *http.Request, v any) error {
	defer r.Body.Close()
	if err := json.NewDecoder(r.Body).Decode(v); err != nil {
		return fmt.Errorf("invalid request body: %w", err)
	}
	return nil
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(v)
}

func writeError(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, api.ErrorResponse{Error: msg})
}

func stripMethod(s string) string {
	return strings.TrimSpace(strings.SplitN(s, " ", 2)[1])
}

// ── Org and policy handlers ───────────────────────────────────────────────────

func (s *Server) handleCreateOrg(w http.ResponseWriter, r *http.Request) {
	if !requireAdmin(w, r) {
		return
	}
	var req api.CreateOrgRequest
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if req.Name == "" {
		writeError(w, http.StatusBadRequest, "org name is required")
		return
	}
	org, err := s.orgs.CreateOrg(req.Name)
	if err != nil {
		writeError(w, http.StatusConflict, err.Error())
		return
	}
	fmt.Printf("[scheduler] org created: %s (%s)\n", org.ID, org.Name)
	writeJSON(w, http.StatusCreated, org)
}

func (s *Server) handleListOrgs(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, s.orgs.ListOrgs())
}

func (s *Server) handleGetOrg(w http.ResponseWriter, r *http.Request) {
	org, ok := s.orgs.GetOrg(r.PathValue("id"))
	if !ok {
		writeError(w, http.StatusNotFound, "org not found")
		return
	}
	writeJSON(w, http.StatusOK, org)
}

func (s *Server) handleCreatePolicy(w http.ResponseWriter, r *http.Request) {
	var req api.CreatePolicyRequest
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if req.Name == "" {
		writeError(w, http.StatusBadRequest, "policy name is required")
		return
	}
	pol, err := s.orgs.CreatePolicy(r.PathValue("id"), req)
	if err != nil {
		writeError(w, http.StatusNotFound, err.Error())
		return
	}
	fmt.Printf("[scheduler] policy %q created for org %s\n", pol.Name, r.PathValue("id"))
	writeJSON(w, http.StatusCreated, pol)
}

func (s *Server) handleListPolicies(w http.ResponseWriter, r *http.Request) {
	policies, ok := s.orgs.GetPolicies(r.PathValue("id"))
	if !ok {
		writeError(w, http.StatusNotFound, "org not found")
		return
	}
	writeJSON(w, http.StatusOK, policies)
}

func (s *Server) handleDeletePolicy(w http.ResponseWriter, r *http.Request) {
	if err := s.orgs.DeletePolicy(r.PathValue("id"), r.PathValue("polID")); err != nil {
		writeError(w, http.StatusNotFound, err.Error())
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// ── Project handlers ──────────────────────────────────────────────────────────

func (s *Server) handleCreateProject(w http.ResponseWriter, r *http.Request) {
	var req api.CreateProjectRequest
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if req.Name == "" || req.RepoURL == "" {
		writeError(w, http.StatusBadRequest, "name and repo_url are required")
		return
	}
	orgID := r.URL.Query().Get("org_id")

	proj, err := s.projects.CreateProject(orgID, req)
	if err != nil {
		writeError(w, http.StatusConflict, err.Error())
		return
	}
	fmt.Printf("[scheduler] project created: %s (%s)\n", proj.ID, proj.RepoURL)
	writeJSON(w, http.StatusCreated, proj)
}

func (s *Server) handleListProjects(w http.ResponseWriter, r *http.Request) {
	orgID := r.URL.Query().Get("org_id")
	writeJSON(w, http.StatusOK, s.projects.ListProjects(orgID))
}

func (s *Server) handleManualTrigger(w http.ResponseWriter, r *http.Request) {
	projectID := r.PathValue("id")
	var req api.ManualTriggerRequest
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON")
		return
	}

	if req.Branch == "" {
		req.Branch = "main"
	}

	proj, _, scmToken, ok := s.projects.GetProject(projectID)
	if !ok {
		writeError(w, http.StatusNotFound, "project not found")
		return
	}

	// Sync cache first to resolve HEAD or branch to a commit SHA.
	if err := s.gitCache.Sync(proj.RepoURL, scmToken); err != nil {
		writeError(w, http.StatusInternalServerError, fmt.Sprintf("failed to sync repo: %v", err))
		return
	}

	commitSHA := req.Commit
	if commitSHA == "" {
		// Resolve branch to SHA
		dir := s.gitCache.RepoDir(proj.RepoURL)
		cmd := exec.Command("git", "-C", dir, "rev-parse", "origin/"+req.Branch)
		output, err := cmd.CombinedOutput()
		if err != nil {
			cmd = exec.Command("git", "-C", dir, "rev-parse", req.Branch)
			output, err = cmd.CombinedOutput()
			if err != nil {
				writeError(w, http.StatusBadRequest, fmt.Sprintf("failed to resolve commit for branch %s: %v, output: %s", req.Branch, err, string(output)))
				return
			}
		}
		commitSHA = strings.TrimSpace(string(output))
	}

	meta := api.WebhookRunMeta{
		Provider:  "manual",
		RepoURL:   proj.RepoURL,
		RepoName:  proj.Name,
		Branch:    req.Branch,
		CommitSHA: commitSHA,
		CommitMsg: "Manual trigger",
		Author:    "API User",
	}

	runID, err := s.triggerWebhookRun(proj, proj.RepoURL, req.Branch, commitSHA, "", scmToken, meta)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}

	writeJSON(w, http.StatusCreated, map[string]string{"run_id": runID})
}

// handleAppendJobLogs receives a batch of log events from the agent and
// broadcasts them to any browser watching this job's log stream.
// We do NOT write to the database here — Complete() already stores the
// full canonical log set when the job finishes. Writing here AND in Complete
// was causing every event to appear twice in GetJobLogs.
func (s *Server) handleAppendJobLogs(w http.ResponseWriter, r *http.Request) {
	jobID := r.PathValue("id")

	var req api.AppendLogsRequest
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if len(req.Events) == 0 {
		w.WriteHeader(http.StatusNoContent)
		return
	}

	// Broadcast to any browser watching this job's live log stream.
	for _, e := range req.Events {
		data, _ := json.Marshal(e)
		s.broker.Publish("log:"+jobID, string(data))
	}

	w.WriteHeader(http.StatusNoContent)
}

// handleJobLogStream opens an SSE connection that streams log events for a job.
// Sends all stored logs first (catch-up), then streams new events as they arrive.
func (s *Server) handleJobLogStream(w http.ResponseWriter, r *http.Request) {
	jobID := r.PathValue("id")

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("Access-Control-Allow-Origin", "*")

	flusher, ok := w.(http.Flusher)
	if !ok {
		writeError(w, http.StatusInternalServerError, "streaming not supported")
		return
	}

	// Subscribe first, THEN send existing logs.
	// This order prevents a race where a new event arrives between
	// "fetch existing" and "subscribe" and gets silently dropped.
	ch := s.broker.Subscribe("log:" + jobID)
	defer s.broker.Unsubscribe("log:"+jobID, ch)

	// Send all existing log events so the browser can catch up.
	if logs, ok := s.store.GetJobLogs(jobID); ok {
		for _, e := range logs {
			data, _ := json.Marshal(e)
			fmt.Fprintf(w, "data: %s\n\n", data)
		}
		flusher.Flush()
	}

	// Stream new events as they arrive from the agent.
	for {
		select {
		case event, open := <-ch:
			if !open {
				return
			}
			fmt.Fprintf(w, "data: %s\n\n", event)
			flusher.Flush()
		case <-r.Context().Done():
			return
		}
	}
}

// ── Run management handlers ───────────────────────────────────────────────────

func (s *Server) handleCancelRun(w http.ResponseWriter, r *http.Request) {
	runID := r.PathValue("id")
	n, err := s.store.CancelRun(runID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if n == 0 {
		writeError(w, http.StatusNotFound, "run not found or already finished")
		return
	}
	s.publishRunDetail(runID)
	fmt.Printf("[scheduler] run %s canceled (%d jobs)\n", runID[:8], n)
	writeJSON(w, http.StatusOK, map[string]int64{"canceled_jobs": n})
}

func (s *Server) handleRerun(w http.ResponseWriter, r *http.Request) {
	if !requireAdmin(w, r) {
		return
	}
	runID := r.PathValue("id")
	name, steps, workspaceDir, err := s.store.RerunSteps(runID)
	if err != nil {
		writeError(w, http.StatusNotFound, err.Error())
		return
	}

	newRunID, err := s.store.SubmitRun("rerun: "+name, workspaceDir, "", "", steps, nil)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	fmt.Printf("[scheduler] rerun of %s → new run %s\n", runID[:8], newRunID[:8])
	s.publishRunDetail(newRunID)
	writeJSON(w, http.StatusCreated, api.SubmitRunResponse{RunID: newRunID})
}

func (s *Server) handlePruneRuns(w http.ResponseWriter, r *http.Request) {
	if !requireAdmin(w, r) {
		return
	}
	days := 30
	if d := r.URL.Query().Get("days"); d != "" {
		fmt.Sscanf(d, "%d", &days)
	}
	if days < 0 {
		writeError(w, http.StatusBadRequest, "days must be >= 0")
		return
	}
	n, err := s.store.PruneRuns(days)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	fmt.Printf("[scheduler] pruned %d runs older than %d days\n", n, days)
	writeJSON(w, http.StatusOK, map[string]any{"pruned": n, "older_than_days": days})
}

// startRetentionWorker runs a daily background job that deletes old runs.
func (s *Server) startRetentionWorker(ctx context.Context) {
	days := 30
	if d := os.Getenv("FORGE_RUN_RETENTION_DAYS"); d != "" {
		fmt.Sscanf(d, "%d", &days)
	}
	if days <= 0 {
		fmt.Println("[scheduler] run retention disabled (FORGE_RUN_RETENTION_DAYS=0)")
		return
	}
	fmt.Printf("[scheduler] run retention: deleting runs older than %d days (daily)\n", days)

	ticker := time.NewTicker(24 * time.Hour)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			n, err := s.store.PruneRuns(days)
			if err != nil {
				fmt.Printf("[scheduler] retention error: %v\n", err)
			} else if n > 0 {
				fmt.Printf("[scheduler] retention: pruned %d runs older than %d days\n", n, days)
			}
		}
	}
}

// handleFlakySteps returns steps with inconsistent pass/fail history.
func (s *Server) handleFlakySteps(w http.ResponseWriter, r *http.Request) {
	windowDays := 30
	minRuns := 5
	minRate := 0.05 // 5% flake rate threshold
	if d := r.URL.Query().Get("days"); d != "" {
		fmt.Sscanf(d, "%d", &windowDays)
	}
	if n := r.URL.Query().Get("min_runs"); n != "" {
		fmt.Sscanf(n, "%d", &minRuns)
	}

	results, err := s.store.FlakySteps(windowDays, minRuns, minRate)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if results == nil {
		results = []api.FlakyStep{}
	}
	writeJSON(w, http.StatusOK, results)
}
