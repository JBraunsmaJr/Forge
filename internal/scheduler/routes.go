package scheduler

import (
	"net/http"

	"github.com/prometheus/client_golang/prometheus/promhttp"
)

// registerRoutes attaches every HTTP route the scheduler serves to mux.
//
// It is separate from Start so that tests can exercise the real routing table,
// wrapped in authMiddleware, without starting listeners or background workers.
// Authentication is not applied here: callers wrap the mux in authMiddleware.
// Routes wrapped in agentRoute or operatorRoute check the caller's role before
// the handler runs; the remaining routes still check it inside the handler.
func (s *Server) registerRoutes(mux *http.ServeMux) {
	// Web UI - public (must load before the user can authenticate).
	mux.HandleFunc("GET /", s.handleIndex)
	mux.Handle("GET /metrics", promhttp.Handler())

	// OIDC
	if s.oidc != nil {
		mux.HandleFunc("GET /.well-known/openid-configuration", s.oidc.HandleConfiguration)
		mux.HandleFunc("GET /api/v1/jwks", s.oidc.HandleJWKS)
	}

	// Token management
	mux.HandleFunc("POST /api/v1/tokens", s.handleCreateToken)
	mux.HandleFunc("GET /api/v1/tokens", s.handleListTokens)
	mux.HandleFunc("DELETE /api/v1/tokens/{id}", s.handleRevokeToken)

	// Project management
	mux.HandleFunc("POST /api/v1/projects", s.handleCreateProject)
	mux.HandleFunc("GET /api/v1/projects", s.handleListProjects)
	mux.HandleFunc("PATCH /api/v1/projects/{id}", s.handleUpdateProject)
	mux.HandleFunc("PUT /api/v1/projects/{id}", s.handleUpdateProject)
	mux.HandleFunc("DELETE /api/v1/projects/{id}", s.handleDeleteProject)
	mux.HandleFunc("GET /api/v1/projects/{id}/branches", s.handleListBranches)
	mux.HandleFunc("GET /api/v1/projects/{id}/webhook", s.handleGetProjectWebhook)
	mux.HandleFunc("GET /api/v1/projects/{id}/failure-stats", s.handleFailureStats)
	mux.HandleFunc("POST /api/v1/projects/{id}/trigger", s.handleManualTrigger)
	mux.HandleFunc("GET /api/v1/projects/{id}/build-format", s.handleGetBuildFormat)
	mux.HandleFunc("PUT /api/v1/projects/{id}/build-format", s.handleSetBuildFormat)
	mux.HandleFunc("PUT /api/v1/projects/{id}/version", s.handleSetVersion)
	mux.HandleFunc("PUT /api/v1/projects/{id}/version-tag-filter", s.handleSetVersionTagFilter)

	// SCM webhooks - HMAC secured, excempt from token auth
	mux.HandleFunc("POST /api/v1/webhook/github/{id}", s.handleGitHubWebhook)
	mux.HandleFunc("POST /api/v1/webhook/gitlab/{id}", s.handleGitLabWebhook)
	mux.HandleFunc("POST /api/v1/webhook/generic/{id}", s.handleGenericWebhook)

	// Source serving - used by agents.
	mux.HandleFunc("GET /api/v1/source/{id}", s.handleServeSource)

	// Distributed Cache
	mux.HandleFunc("GET /api/v1/cache/{hash}", s.handleCacheLookup)
	mux.HandleFunc("POST /api/v1/cache", agentRoute(s.handleCacheStore))

	// Org and policy management.
	mux.HandleFunc("POST /api/v1/orgs", s.handleCreateOrg)
	mux.HandleFunc("GET /api/v1/orgs", s.handleListOrgs)
	mux.HandleFunc("GET /api/v1/orgs/{id}", s.handleGetOrg)
	mux.HandleFunc("POST /api/v1/orgs/{id}/policies", s.handleCreatePolicy)
	mux.HandleFunc("PUT /api/v1/orgs/{id}/policies/{polID}", s.handleUpdatePolicy)
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
	mux.HandleFunc("POST /api/v1/jobs/{id}/approve", s.handleApproveJob)
	mux.HandleFunc("POST /api/v1/jobs/{id}/deny", s.handleDenyJob)
	mux.HandleFunc("POST /api/v1/jobs/{id}/waiting", s.handleJobWaiting)
	mux.HandleFunc("POST /api/v1/test-reports", agentRoute(s.handleRecordTestReport))
	mux.HandleFunc("GET /api/v1/projects/{id}/flaky-tests", s.handleGetFlakyTests)
	mux.HandleFunc("GET /api/v1/projects/{id}/health", s.handleGetProjectHealth)
	mux.HandleFunc("POST /api/v1/projects/{id}/health/check", s.handleTriggerProjectHealth)

	// Web UI endpoints
	mux.HandleFunc("GET /api/v1/runs", s.handleListRuns)
	mux.HandleFunc("GET /api/v1/runs/{id}/detail", s.handleRunDetail)
	mux.HandleFunc("GET /api/v1/runs/{id}/comparison", s.handleRunComparison)
	mux.HandleFunc("GET /api/v1/runs/{id}/events", s.handleRunEventsWS)
	mux.HandleFunc("GET /api/v1/jobs/{id}/logs", s.handleJobLogs)
	mux.HandleFunc("GET /api/v1/logs/search", s.handleSearchLogs)
	mux.HandleFunc("POST /api/v1/jobs/{id}/logs", agentRoute(s.handleAppendJobLogs))
	mux.HandleFunc("GET /api/v1/jobs/{id}/logs/stream", s.handleJobLogStreamWS)
	// Agent and queue management
	mux.HandleFunc("GET /api/v1/agents", s.handleListAgents)
	mux.HandleFunc("POST /api/v1/agents/{id}/drain", s.handleDrainAgent)
	mux.HandleFunc("GET /api/v1/queue/depth", s.handleQueueDepth)
	mux.HandleFunc("GET /api/v1/audit", s.handleListAuditLogs)
	mux.HandleFunc("GET /api/v1/audit/export", s.handleListAuditLogs)

	// Agent protocol (legacy HTTP - kept for compatibility)
	mux.HandleFunc("POST /api/v1/jobs/lease", agentRoute(s.handleLease))
	mux.HandleFunc("POST /api/v1/jobs/{id}/heartbeat", agentRoute(s.handleHeartbeat))
	mux.HandleFunc("POST /api/v1/jobs/{id}/complete", agentRoute(s.handleComplete))

	// Debug sesion - browser side.
	mux.HandleFunc("POST /api/v1/debug", operatorRoute(s.handleCreateDebugSession))
	mux.HandleFunc("GET /api/v1/debug/{id}", operatorRoute(s.handleGetDebugSession))
	mux.HandleFunc("GET /api/v1/debug/{id}/stream", operatorRoute(s.handleDebugStream))
	mux.HandleFunc("POST /api/v1/debug/{id}/exec", operatorRoute(s.handleDebugExec))
	mux.HandleFunc("DELETE /api/v1/debug/{id}", operatorRoute(s.handleCloseDebugSession))
	mux.HandleFunc("POST /api/v1/debug/{id}/cancel", operatorRoute(s.handleCancelDebugCommand))
	mux.HandleFunc("GET /api/v1/debug/{id}/cancel-check", agentRoute(s.handleDebugCancelCheck))

	mux.HandleFunc("GET /api/v1/debug/{id}/ws", operatorRoute(s.handleDebugTerminalProxy))
	mux.HandleFunc("GET /api/v1/debug/{id}/agent-ws", agentRoute(s.handleAgentDebugTerminal))

	// Debug session agent side.
	mux.HandleFunc("POST /api/v1/debug/lease", agentRoute(s.handleDebugLease))
	mux.HandleFunc("POST /api/v1/debug/{id}/container", agentRoute(s.handleDebugRegisterContainer))
	mux.HandleFunc("GET /api/v1/debug/{id}/commands", agentRoute(s.handleDebugPollCommands))
	mux.HandleFunc("POST /api/v1/debug/{id}/output", agentRoute(s.handleDebugSubmitOutput))

	// Flaky test detection.
	mux.HandleFunc("GET /api/v1/flaky", s.handleFlakySteps)

	// Auth & SSO
	mux.HandleFunc("GET /api/v1/auth/status", s.handleAuthStatus)
	mux.HandleFunc("POST /api/v1/auth/logout", s.handleLogout)
	mux.HandleFunc("GET /api/v1/auth/login/{provider}", s.handleSSOLogin)
	mux.HandleFunc("GET /api/v1/auth/callback/{provider}", s.handleSSOCallback)

	// Artifact management.
	mux.HandleFunc("POST /api/v1/artifacts/presign", agentRoute(s.handlePresignUpload))
	mux.HandleFunc("GET /api/v1/artifacts", s.handleGetArtifact)
	mux.HandleFunc("POST /api/v1/artifacts/{id}/confirm", agentRoute(s.handleConfirmUpload))
	mux.HandleFunc("PUT /api/v1/artifacts/{id}/upload", s.handleArtifactUpload)
	mux.HandleFunc("GET /api/v1/artifacts/{id}/download", s.handleArtifactDownload)
	mux.HandleFunc("DELETE /api/v1/artifacts/{id}", s.handleDeleteArtifact)

	// Artifacts listed as a sub-resource of a run (avoid wildcard conflict).
	mux.HandleFunc("GET /api/v1/runs/{runID}/artifacts", s.handleListArtifacts)
}
