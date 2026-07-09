package scheduler

import (
	"context"
	"database/sql"
	"embed"
	"encoding/json"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"net/http/httputil"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/JBraunsmaJr/forge/internal/api"
	"github.com/JBraunsmaJr/forge/internal/artifacts"
	"github.com/JBraunsmaJr/forge/internal/executor"
	"github.com/JBraunsmaJr/forge/internal/gitcache"
	policyengine "github.com/JBraunsmaJr/forge/internal/policy"
	"github.com/JBraunsmaJr/forge/internal/secrets"
	"github.com/JBraunsmaJr/forge/internal/tracing"
	"github.com/gorilla/websocket"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

//go:embed all:web/dist/*
var webAssets embed.FS

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
	secrets     *secrets.Client
	internalURL string
	apiToken    string
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

	vaultAddr := os.Getenv("FORGE_VAULT_ADDR")
	vaultToken := os.Getenv("FORGE_VAULT_TOKEN")
	var sc *secrets.Client
	if vaultAddr != "" && vaultToken != "" {
		sc = secrets.NewClient(vaultAddr, vaultToken)
		if err := sc.Ping(); err != nil {
			fmt.Printf("[scheduler] Vault connection failed: %v\n", err)
		} else {
			fmt.Printf("[scheduler] Vault connected at %s\n", vaultAddr)
		}
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
		secrets:     sc,
		internalURL: internalURL,
		apiToken:    os.Getenv("FORGE_API_TOKEN"),
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
	// Clean up any dangling policy transformer containers from previous runs.
	executor.Cleanup()
	defer executor.Cleanup()

	s.tokens.bootstrapIfEmpty()

	mux := http.NewServeMux()

	// Web UI - public (must load before the user can authenticate).
	mux.HandleFunc("GET /", s.handleIndex)
	mux.Handle("GET /metrics", promhttp.Handler())

	// Token management
	mux.HandleFunc("POST /api/v1/tokens", s.handleCreateToken)
	mux.HandleFunc("GET /api/v1/tokens", s.handleListTokens)
	mux.HandleFunc("DELETE /api/v1/tokens/{id}", s.handleRevokeToken)

	// Project management
	mux.HandleFunc("POST /api/v1/projects", s.handleCreateProject)
	mux.HandleFunc("GET /api/v1/projects", s.handleListProjects)
	mux.HandleFunc("GET /api/v1/projects/{id}/branches", s.handleListBranches)
	mux.HandleFunc("POST /api/v1/projects/{id}/trigger", s.handleManualTrigger)

	// SCM webhooks - HMAC secured, excempt from token auth
	mux.HandleFunc("POST /api/v1/webhook/github/{id}", s.handleGitHubWebhook)
	mux.HandleFunc("POST /api/v1/webhook/gitlab/{id}", s.handleGitLabWebhook)
	mux.HandleFunc("POST /api/v1/webhook/generic/{id}", s.handleGenericWebhook)

	// Source serving - used by agents.
	mux.HandleFunc("GET /api/v1/source/{id}", s.handleServeSource)

	// Org and policy management.
	mux.HandleFunc("POST /api/v1/orgs", s.handleCreateOrg)
	mux.HandleFunc("GET /api/v1/orgs", s.handleListOrgs)
	mux.HandleFunc("GET /api/v1/orgs/{id}", s.handleGetOrg)
	mux.HandleFunc("POST /api/v1/orgs/{id}/policies", s.handleCreatePolicy)
	mux.HandleFunc("GET /api/v1/orgs/{id}/policies", s.handleListPolicies)
	mux.HandleFunc("DELETE /api/v1/orgs/{id}/policies/{polID}", s.handleDeletePolicy)

	// Secret management
	mux.HandleFunc("GET /api/v1/orgs/{id}/secrets", s.handleListOrgSecrets)
	mux.HandleFunc("POST /api/v1/orgs/{id}/secrets", s.handleSetOrgSecret)
	mux.HandleFunc("DELETE /api/v1/orgs/{id}/secrets/{name}", s.handleDeleteOrgSecret)
	mux.HandleFunc("GET /api/v1/projects/{id}/secrets", s.handleListProjectSecrets)
	mux.HandleFunc("POST /api/v1/projects/{id}/secrets", s.handleSetProjectSecret)
	mux.HandleFunc("DELETE /api/v1/projects/{id}/secrets/{name}", s.handleDeleteProjectSecret)

	// Pipeline submission + status (used by CLI).
	mux.HandleFunc("POST /api/v1/runs", s.handleSubmitRun)
	mux.HandleFunc("GET /api/v1/runs/{id}", s.handleRunStatus)
	mux.HandleFunc("POST /api/v1/runs/{id}/cancel", s.handleCancelRun)
	mux.HandleFunc("POST /api/v1/runs/{id}/rerun", s.handleRerun)
	mux.HandleFunc("POST /api/v1/runs/{id}/rerun-failed", s.handleRerunFailed)
	mux.HandleFunc("POST /api/v1/jobs/{id}/rerun", s.handleRerunJob)
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

	// Debug sesion - browser side.
	mux.HandleFunc("POST /api/v1/debug", s.handleCreateDebugSession)
	mux.HandleFunc("GET /api/v1/debug/{id}", s.handleGetDebugSession)
	mux.HandleFunc("GET /api/v1/debug/{id}/stream", s.handleDebugStream)
	mux.HandleFunc("POST /api/v1/debug/{id}/exec", s.handleDebugExec)
	mux.HandleFunc("DELETE /api/v1/debug/{id}", s.handleCloseDebugSession)
	mux.HandleFunc("POST /api/v1/debug/{id}/cancel", s.handleCancelDebugCommand)
	mux.HandleFunc("GET /api/v1/debug/{id}/cancel-check", s.handleDebugCancelCheck)

	mux.HandleFunc("GET /api/v1/debug/{id}/ws", s.handleDebugTerminalProxy)

	// Debug session agent side.
	mux.HandleFunc("POST /api/v1/debug/lease", s.handleDebugLease)
	mux.HandleFunc("POST /api/v1/debug/{id}/container", s.handleDebugRegisterContainer)
	mux.HandleFunc("GET /api/v1/debug/{id}/commands", s.handleDebugPollCommands)
	mux.HandleFunc("POST /api/v1/debug/{id}/output", s.handleDebugSubmitOutput)

	// Flaky test detection.
	mux.HandleFunc("GET /api/v1/flaky", s.handleFlakySteps)

	// Artifact management.
	mux.HandleFunc("POST /api/v1/artifacts/presign", s.handlePresignUpload)
	mux.HandleFunc("GET /api/v1/artifacts", s.handleGetArtifact)
	mux.HandleFunc("POST /api/v1/artifacts/{id}/confirm", s.handleConfirmUpload)
	mux.HandleFunc("PUT /api/v1/artifacts/{id}/upload", s.handleArtifactUpload)
	mux.HandleFunc("GET /api/v1/artifacts/{id}/download", s.handleArtifactDownload)
	mux.HandleFunc("DELETE /api/v1/artifacts/{id}", s.handleDeleteArtifact)

	// Artifacts listed as a sub-resource of a run (avoid wildcard conflict).
	mux.HandleFunc("GET /api/v1/runs/{runID}/artifacts", s.handleListArtifacts)

	s.server = &http.Server{Addr: s.addr, Handler: s.authMiddleware(mux)}

	go s.heartbeatMonitor(ctx)
	go s.startRetentionWorker(ctx)
	go s.startMetricsWorker(ctx)
	go s.startDockerPruneWorker(ctx)

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

func (s *Server) startMetricsWorker(ctx context.Context) {
	ticker := time.NewTicker(30 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ticker.C:
			if n, err := s.store.ActiveAgentsCount(); err == nil {
				agentsActive.Set(float64(n))
			}
		case <-ctx.Done():
			return
		}
	}
}

func (s *Server) handleIndex(w http.ResponseWriter, r *http.Request) {
	if devURL := os.Getenv("FORGE_UI_DEV_URL"); devURL != "" {
		target, err := url.Parse(devURL)
		if err != nil {
			fmt.Printf("[scheduler] invalid FORGE_UI_DEV_URL: %v\n", err)
			http.Error(w, "Invalid Dev URL", http.StatusInternalServerError)
			return
		}

		if strings.ToLower(r.Header.Get("Upgrade")) == "websocket" {
			s.proxyWebSocket(w, r, target)
			return
		}

		proxy := httputil.NewSingleHostReverseProxy(target)
		proxy.ServeHTTP(w, r)
		return
	}

	distFS, err := fs.Sub(webAssets, "web/dist")
	if err != nil {
		http.Error(w, "Internal Server Error", http.StatusInternalServerError)
		return
	}

	path := strings.TrimPrefix(r.URL.Path, "/")
	if path == "" {
		path = "index.html"
	}

	if _, err := fs.Stat(distFS, path); err != nil {
		path = "index.html"
	}

	f, err := distFS.Open(path)
	if err != nil {
		http.Error(w, "Not Found", http.StatusNotFound)
		return
	}
	defer f.Close()

	fi, _ := f.Stat()

	http.ServeContent(w, r, path, fi.ModTime(), f.(io.ReadSeeker))
}

func (s *Server) proxyWebSocket(w http.ResponseWriter, r *http.Request, target *url.URL) {
	wsTarget := *target
	if wsTarget.Scheme == "http" {
		wsTarget.Scheme = "ws"
	} else if wsTarget.Scheme == "https" {
		wsTarget.Scheme = "wss"
	}
	wsTarget.Path = r.URL.Path
	wsTarget.RawQuery = r.URL.RawQuery

	upgrader := websocket.Upgrader{
		CheckOrigin: func(r *http.Request) bool { return true },
	}
	browserConn, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		fmt.Printf("[scheduler] ws upgrade failed: %v\n", err)
		return
	}
	defer browserConn.Close()

	backendConn, _, err := websocket.DefaultDialer.Dial(wsTarget.String(), nil)
	if err != nil {
		fmt.Printf("[scheduler] ws dial failed: %v\n", err)
		return
	}
	defer backendConn.Close()

	errc := make(chan error, 2)
	cp := func(dst, src *websocket.Conn) {
		for {
			mt, msg, err := src.ReadMessage()
			if err != nil {
				errc <- err
				return
			}
			if err := dst.WriteMessage(mt, msg); err != nil {
				errc <- err
				return
			}
		}
	}
	go cp(browserConn, backendConn)
	go cp(backendConn, browserConn)
	<-errc
}

func (s *Server) handleListRuns(w http.ResponseWriter, r *http.Request) {
	opts := ListRunsOptions{
		Status: r.URL.Query().Get("status"),
		Search: r.URL.Query().Get("search"),
	}
	if l := r.URL.Query().Get("limit"); l != "" {
		fmt.Sscanf(l, "%d", &opts.Limit)
	}
	if o := r.URL.Query().Get("offset"); o != "" {
		fmt.Sscanf(o, "%d", &opts.Offset)
	}
	writeJSON(w, http.StatusOK, s.store.ListRuns(opts))
}

func (s *Server) handleRunDetail(w http.ResponseWriter, r *http.Request) {
	detail, ok := s.store.RunDetail(r.PathValue("id"))
	if !ok {
		writeError(w, http.StatusNotFound, "run not found")
		return
	}
	writeJSON(w, http.StatusOK, detail)
}

func (s *Server) handleListOrgSecrets(w http.ResponseWriter, r *http.Request) {
	if s.secrets == nil {
		writeError(w, http.StatusServiceUnavailable, "Vault is not configured")
		return
	}
	orgID := r.PathValue("id")
	prefix := secrets.OrgScopePath(orgID)
	names, err := s.secrets.List(prefix)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if names == nil {
		names = []string{}
	}
	writeJSON(w, http.StatusOK, names)
}

func (s *Server) handleSetOrgSecret(w http.ResponseWriter, r *http.Request) {
	if s.secrets == nil {
		writeError(w, http.StatusServiceUnavailable, "Vault is not configured")
		return
	}
	orgID := r.PathValue("id")
	var req struct {
		Name  string `json:"name"`
		Value string `json:"value"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if req.Name == "" || req.Value == "" {
		writeError(w, http.StatusBadRequest, "name and value are required")
		return
	}
	prefix := secrets.OrgScopePath(orgID)
	if err := s.secrets.Set(prefix, req.Name, req.Value); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	w.WriteHeader(http.StatusCreated)
}

func (s *Server) handleDeleteOrgSecret(w http.ResponseWriter, r *http.Request) {
	if s.secrets == nil {
		writeError(w, http.StatusServiceUnavailable, "Vault is not configured")
		return
	}
	orgID := r.PathValue("id")
	name := r.PathValue("name")
	prefix := secrets.OrgScopePath(orgID)
	if err := s.secrets.Delete(prefix, name); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) handleListProjectSecrets(w http.ResponseWriter, r *http.Request) {
	if s.secrets == nil {
		writeError(w, http.StatusServiceUnavailable, "Vault is not configured")
		return
	}
	projectID := r.PathValue("id")
	prefix := secrets.ProjectScopePath(projectID)
	names, err := s.secrets.List(prefix)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if names == nil {
		names = []string{}
	}
	writeJSON(w, http.StatusOK, names)
}

func (s *Server) handleSetProjectSecret(w http.ResponseWriter, r *http.Request) {
	if s.secrets == nil {
		writeError(w, http.StatusServiceUnavailable, "Vault is not configured")
		return
	}
	projectID := r.PathValue("id")
	var req struct {
		Name  string `json:"name"`
		Value string `json:"value"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if req.Name == "" || req.Value == "" {
		writeError(w, http.StatusBadRequest, "name and value are required")
		return
	}
	prefix := secrets.ProjectScopePath(projectID)
	if err := s.secrets.Set(prefix, req.Name, req.Value); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	w.WriteHeader(http.StatusCreated)
}

func (s *Server) handleDeleteProjectSecret(w http.ResponseWriter, r *http.Request) {
	if s.secrets == nil {
		writeError(w, http.StatusServiceUnavailable, "Vault is not configured")
		return
	}
	projectID := r.PathValue("id")
	name := r.PathValue("name")
	prefix := secrets.ProjectScopePath(projectID)
	if err := s.secrets.Delete(prefix, name); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// handleRunEvents streams server-sent-events to the browser.
// The connection stays open until the client disconnects or the run finishes.
//
// SSE wire format - each event is two lines:
//
//	data: <json>\n
//	\n
//
// The blank line signals the end of one event. The browser's EventSource fires a "message" event for each one.
func (s *Server) handleRunEvents(w http.ResponseWriter, r *http.Request) {
	runID := r.PathValue("id")

	// SSE requires these three headers. Without Cache-Control: no-cache,
	// proxies may buffer the stream and the browser sees nothing.
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")

	w.Header().Set("Access-Control-Allow-Origin", "*")

	flusher, ok := w.(http.Flusher)
	if !ok {
		writeError(w, http.StatusInternalServerError, "streaming not supported")
		return
	}

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

func (s *Server) handleSubmitRun(w http.ResponseWriter, r *http.Request) {
	_, span := tracing.Tracer().Start(r.Context(), "handleSubmitRun")
	defer span.End()

	var req api.SubmitRunRequest
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}

	/*
		Policy injection.

		if an OrgID was provided, load that org's policies and inject
		mandatory steps before queuing. This happens server-side so users
		cannot bypass it by emitting steps from the pipeline.json.
	*/
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

				writeError(w, http.StatusForbidden, err.Error())
				return
			}
		}
	}

	if err := validateSteps(steps); err != nil {
		writeError(w, http.StatusBadRequest, fmt.Sprintf("invalid pipeline: %v", err))
		return
	}

	runID, err := s.store.SubmitRun(req.PipelineName, req.WorkspaceDir, req.OrgID, req.ProjectID, req.CommitSHA, steps, appliedPolicies)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}

	runsTotal.WithLabelValues(req.OrgID, req.ProjectID, "cli").Inc()
	jobsSubmittedTotal.WithLabelValues(req.OrgID, req.ProjectID).Add(float64(len(steps)))

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

	if detail, ok := s.store.RunDetail(runID); ok {
		stepID := s.store.GetJobStepID(r.PathValue("id"))
		if stepID == "" {
			stepID = r.PathValue("id")
		}
		go s.store.RecordStepResult(runID, detail.Name, stepID, result, req.Duration)

		jobsCompletedTotal.WithLabelValues(detail.OrgID, detail.ProjectID, result).Inc()
		jobDurationSeconds.WithLabelValues(detail.OrgID, detail.ProjectID).Observe(float64(req.Duration) / 1000.0)
	}

	s.publishRunDetail(runID)
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) publishRunDetail(runID string) {
	if detail, ok := s.store.RunDetail(runID); ok {
		data, _ := json.Marshal(detail)
		s.broker.Publish(runID, string(data))
	}
}

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

func (s *Server) handleListBranches(w http.ResponseWriter, r *http.Request) {
	projectID := r.PathValue("id")
	proj, _, scmToken, ok := s.projects.GetProject(projectID)
	if !ok {
		writeError(w, http.StatusNotFound, "project not found")
		return
	}

	if err := s.gitCache.Sync(proj.RepoURL, scmToken); err != nil {
		writeError(w, http.StatusInternalServerError, fmt.Sprintf("failed to sync repo: %v", err))
		return
	}

	branches, defaultBranch, err := s.gitCache.ListBranches(proj.RepoURL)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}

	writeJSON(w, http.StatusOK, api.ProjectBranchesResponse{
		Branches: branches,
		Default:  defaultBranch,
	})
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

	if err := s.gitCache.Sync(proj.RepoURL, scmToken); err != nil {
		writeError(w, http.StatusInternalServerError, fmt.Sprintf("failed to sync repo: %v", err))
		return
	}

	commitSHA := req.Commit
	if commitSHA == "" {

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
// broadcasts to any browser matching this job's log stream.
// We do NOT write to the database here - Complete() already stores the full canonical log
// set when the job finishes. Writing here AND in Complete was
// causing each event to appear twice in GetJobLogs.
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

	if err := s.store.AppendJobLogs(jobID, req.LeaseID, req.Events); err != nil {
		fmt.Printf("[scheduler] failed to persist logs for job %s: %v\n", jobID[:8], err)

	}

	for _, e := range req.Events {
		data, _ := json.Marshal(e)
		s.broker.Publish("log:"+jobID, string(data))
	}

	w.WriteHeader(http.StatusNoContent)
}

// handleJobLogStream opens an SSE connection that stream log events for a job.
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

	/*
		Subscribe first, THEN send existing logs.
		This order pevents a race condition where a new event arrives between
		"fetch existing" and "subscribe" and get silently dropped.
	*/
	ch := s.broker.Subscribe("log:" + jobID)
	defer s.broker.Unsubscribe("log:"+jobID, ch)

	if logs, ok := s.store.GetJobLogs(jobID); ok {
		for i, e := range logs {
			data, _ := json.Marshal(e)
			fmt.Fprintf(w, "data: %s\n\n", data)
			if (i+1)%100 == 0 {
				flusher.Flush()
			}
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
	name, steps, workspaceDir, orgID, projectID, commitSHA, err := s.store.RerunSteps(runID)
	if err != nil {
		writeError(w, http.StatusNotFound, err.Error())
		return
	}

	if err := validateSteps(steps); err != nil {
		writeError(w, http.StatusBadRequest, fmt.Sprintf("invalid pipeline for rerun: %v", err))
		return
	}

	newName := name
	for strings.HasPrefix(newName, "rerun: ") {
		newName = strings.TrimPrefix(newName, "rerun: ")
	}
	newName = "rerun: " + newName

	for i := range steps {
		steps[i].Status = ""
	}

	newRunID, err := s.store.SubmitRun(newName, workspaceDir, orgID, projectID, commitSHA, steps, nil)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	fmt.Printf("[scheduler] rerun of %s → new run %s\n", runID[:8], newRunID[:8])
	s.publishRunDetail(newRunID)
	writeJSON(w, http.StatusCreated, api.SubmitRunResponse{RunID: newRunID})
}

func (s *Server) handleRerunFailed(w http.ResponseWriter, r *http.Request) {
	if !requireAdmin(w, r) {
		return
	}
	runID := r.PathValue("id")
	name, steps, workspaceDir, orgID, projectID, commitSHA, err := s.store.RerunSteps(runID)
	if err != nil {
		writeError(w, http.StatusNotFound, err.Error())
		return
	}

	if err := validateSteps(steps); err != nil {
		writeError(w, http.StatusBadRequest, fmt.Sprintf("invalid pipeline for rerun: %v", err))
		return
	}

	newName := name
	for strings.HasPrefix(newName, "rerun: ") {
		newName = strings.TrimPrefix(newName, "rerun: ")
	}
	newName = "rerun: " + newName

	for i := range steps {
		if steps[i].Status == api.JobStatusPassed {
			// Keep as passed
		} else {
			steps[i].Status = "" // Rerun
		}
	}

	newRunID, err := s.store.SubmitRun(newName, workspaceDir, orgID, projectID, commitSHA, steps, nil)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	fmt.Printf("[scheduler] rerun-failed of %s → new run %s\n", runID[:8], newRunID[:8])
	s.publishRunDetail(newRunID)
	writeJSON(w, http.StatusCreated, api.SubmitRunResponse{RunID: newRunID})
}

func (s *Server) handleRerunJob(w http.ResponseWriter, r *http.Request) {
	if !requireAdmin(w, r) {
		return
	}
	jobID := r.PathValue("id")
	detail, ok := s.store.RunDetailByJobID(jobID)
	if !ok {
		writeError(w, http.StatusNotFound, "job not found")
		return
	}
	runID := detail.RunID

	name, steps, workspaceDir, orgID, projectID, commitSHA, err := s.store.RerunSteps(runID)
	if err != nil {
		writeError(w, http.StatusNotFound, err.Error())
		return
	}

	var targetStepID string
	for _, j := range detail.Jobs {
		if j.JobID == jobID {
			targetStepID = j.StepID
			break
		}
	}

	if err := validateSteps(steps); err != nil {
		writeError(w, http.StatusBadRequest, fmt.Sprintf("invalid pipeline for rerun: %v", err))
		return
	}

	newName := name
	for strings.HasPrefix(newName, "rerun: ") {
		newName = strings.TrimPrefix(newName, "rerun: ")
	}
	newName = "rerun: " + newName

	toRerun := make(map[string]bool)
	toRerun[targetStepID] = true
	changed := true
	for changed {
		changed = false
		for _, step := range steps {
			if toRerun[step.ID] {
				continue
			}
			for _, dep := range step.DependsOn {
				if toRerun[dep] {
					toRerun[step.ID] = true
					changed = true
					break
				}
			}
		}
	}

	for i := range steps {
		if toRerun[steps[i].ID] {
			steps[i].Status = ""
		} else if steps[i].Status == api.JobStatusPassed {
			// Keep as passed
		} else {
			// Job didn't pass, but isn't part of the rerun set.
			// It remains in its current status, which will block its own downstreams
			// (unless they are also in toRerun).
		}
	}

	newRunID, err := s.store.SubmitRun(newName, workspaceDir, orgID, projectID, commitSHA, steps, nil)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	fmt.Printf("[scheduler] rerun-job %s of %s → new run %s\n", targetStepID, runID[:8], newRunID[:8])
	s.publishRunDetail(newRunID)
	writeJSON(w, http.StatusCreated, api.SubmitRunResponse{RunID: newRunID})
}

func (s *Server) prune(ctx context.Context, olderThan time.Duration) (int64, error) {
	ids, err := s.store.GetRunsOlderThan(olderThan)
	if err != nil {
		return 0, err
	}
	for _, id := range ids {
		s.artifacts.DeleteRunArtifacts(ctx, id)
	}
	return s.store.PruneRuns(olderThan)
}

func (s *Server) handlePruneRuns(w http.ResponseWriter, r *http.Request) {
	if !requireAdmin(w, r) {
		return
	}
	olderThan := 30 * 24 * time.Hour
	if d := r.URL.Query().Get("days"); d != "" {
		var days int
		fmt.Sscanf(d, "%d", &days)
		olderThan = time.Duration(days) * 24 * time.Hour
	} else if age := r.URL.Query().Get("age"); age != "" {
		if val, err := time.ParseDuration(age); err == nil {
			olderThan = val
		} else {
			writeError(w, http.StatusBadRequest, "invalid age format (e.g. 30m, 24h, 7d)")
			return
		}
	}

	n, err := s.prune(r.Context(), olderThan)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	fmt.Printf("[scheduler] pruned %d runs older than %s\n", n, olderThan)
	writeJSON(w, http.StatusOK, map[string]any{"pruned": n, "older_than": olderThan.String()})
}

// startRetentionWorker runs a background job that deletes old runs and artifacts.
func (s *Server) startRetentionWorker(ctx context.Context) {
	retention := 30 * 24 * time.Hour
	if r := os.Getenv("FORGE_RUN_RETENTION"); r != "" {
		if val, err := time.ParseDuration(r); err == nil {
			retention = val
		} else if days, err := strconv.Atoi(r); err == nil {
			retention = time.Duration(days) * 24 * time.Hour
		}
	} else if d := os.Getenv("FORGE_RUN_RETENTION_DAYS"); d != "" {
		var days int
		fmt.Sscanf(d, "%d", &days)
		retention = time.Duration(days) * 24 * time.Hour
	}

	if retention <= 0 {
		fmt.Println("[scheduler] run retention disabled")
		return
	}

	interval := 24 * time.Hour
	if i := os.Getenv("FORGE_RUN_RETENTION_INTERVAL"); i != "" {
		if val, err := time.ParseDuration(i); err == nil {
			interval = val
		}
	} else if retention < 24*time.Hour {
		interval = 1 * time.Hour
	}

	fmt.Printf("[scheduler] run retention: deleting runs older than %s (every %s)\n", retention, interval)

	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			s.artifacts.Cleanup()
			n, err := s.prune(ctx, retention)
			if err != nil {
				fmt.Printf("[scheduler] retention error: %v\n", err)
			} else if n > 0 {
				fmt.Printf("[scheduler] retention: pruned %d runs older than %s\n", n, retention)
			}
		}
	}
}

func (s *Server) startDockerPruneWorker(ctx context.Context) {
	schedule := os.Getenv("FORGE_PRUNE_SCHEDULE")
	d := 24 * time.Hour
	if schedule == "@hourly" {
		d = time.Hour
	} else if schedule != "" && schedule != "@daily" {
		if val, err := time.ParseDuration(schedule); err == nil {
			d = val
		}
	}

	fmt.Printf("[scheduler] Docker prune scheduled every %s\n", d)
	ticker := time.NewTicker(d)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			fmt.Println("[scheduler] running scheduled docker system prune...")
			exec.Command("docker", "system", "prune", "-f").Run()
			s.cleanupWorkspaces()
		}
	}
}

func (s *Server) cleanupWorkspaces() {
	tempDir := os.TempDir()
	files, err := os.ReadDir(tempDir)
	if err != nil {
		return
	}

	now := time.Now()
	for _, f := range files {
		if !f.IsDir() || !strings.HasPrefix(f.Name(), "forge-ws-") {
			continue
		}

		info, err := f.Info()
		if err != nil {
			continue
		}

		// Remove workspaces older than 24 hours
		if now.Sub(info.ModTime()) > 24*time.Hour {
			fmt.Printf("[scheduler] cleaning up old workspace: %s\n", f.Name())
			os.RemoveAll(filepath.Join(tempDir, f.Name()))
		}
	}
}

// handleFlakySteps returns steps with inconsistent pass/fail history.
func (s *Server) handleFlakySteps(w http.ResponseWriter, r *http.Request) {
	windowDays := 30
	minRuns := 5
	minRate := 0.05
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

func validateSteps(steps []api.StepDef) error {
	// 1. Check for duplicate IDs
	ids := make(map[string]struct{})
	for _, s := range steps {
		if s.ID == "" {
			continue // compiler fills these in, but policies might not.
		}
		if _, ok := ids[s.ID]; ok {
			return fmt.Errorf("duplicate step ID: %s", s.ID)
		}
		ids[s.ID] = struct{}{}
	}

	// 2. Check for cycles and missing dependencies
	visited := make(map[string]bool)
	onStack := make(map[string]bool)

	adj := make(map[string][]string)
	for _, s := range steps {
		adj[s.ID] = s.DependsOn
	}

	var check func(string) error
	check = func(u string) error {
		visited[u] = true
		onStack[u] = true
		for _, v := range adj[u] {
			if _, ok := adj[v]; !ok {
				return fmt.Errorf("step %s depends on non-existent step %s", u, v)
			}
			if onStack[v] {
				return fmt.Errorf("cycle detected: step %s is part of a dependency loop", v)
			}
			if !visited[v] {
				if err := check(v); err != nil {
					return err
				}
			}
		}
		onStack[u] = false
		return nil
	}

	for _, s := range steps {
		if !visited[s.ID] {
			if err := check(s.ID); err != nil {
				return err
			}
		}
	}

	return nil
}
