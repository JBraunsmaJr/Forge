package scheduler

import (
	"fmt"
	"net/http"
	"sync"
	"time"

	"github.com/JBraunsmaJr/forge/internal/api"
)

// healthTriggerCooldown is the minimum time since the last check (whether
// scheduled or manually triggered) before another manual trigger is
// accepted. Short enough that "I want a fresh check now" is never
// meaningfully delayed, long enough that a double-click or a client-side
// retry loop can't repeatedly force full git syncs back to back.
const healthTriggerCooldown = 30 * time.Second

// handleGetProjectHealth serves the latest health snapshot for a project,
// plus trend delta and org-average comparison. Returns 404 if the project has never been checked
// (rather than a zeroed/misleading 200) — the health monitor runs
// on its own schedule, so "no snapshot yet" is an expected, normal state
// for a newly registered project, not an error condition to hide.
func (s *Server) handleGetProjectHealth(w http.ResponseWriter, r *http.Request) {
	projectID := r.PathValue("id")

	latest, previous, err := s.store.LatestHealthSnapshots(projectID)
	if err != nil {
		fmt.Printf("[health] GET %s: %v\n", projectID, err)
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if latest == nil {
		writeError(w, http.StatusNotFound, "no health check has run for this project yet")
		return
	}

	writeJSON(w, http.StatusOK, s.buildHealthResponse(projectID, latest, previous))
}

var healthCheckInProgress sync.Map // map[string]struct{}

// handleTriggerProjectHealth lets a user request a fresh health check
// immediately instead of waiting for the next scheduled run (the
// scheduled interval defaults to a week — see healthCheckInterval).
// Runs synchronously: the point of a manual trigger is to see the result
// right away, not to poll for one, and a single pipeline-file fetch +
// score is fast enough that blocking the request for it is the right
// tradeoff over a fire-and-forget-then-poll design.
func (s *Server) handleTriggerProjectHealth(w http.ResponseWriter, r *http.Request) {
	if !requireOperator(w, r) {
		return
	}
	projectID := r.PathValue("id")

	if _, loaded := healthCheckInProgress.LoadOrStore(projectID, struct{}{}); loaded {
		writeError(w, http.StatusTooManyRequests, "a health check is already in progress for this project")
		return
	}

	defer healthCheckInProgress.Delete(projectID)

	if latest, _, err := s.store.LatestHealthSnapshots(projectID); err == nil && latest != nil {
		if wait := healthTriggerCooldown - time.Since(latest.ComputedAt); wait > 0 {
			writeError(w, http.StatusTooManyRequests, "a health check ran too recently; try again in "+wait.Round(time.Second).String())
			return
		}
	}

	proj, _, _, ok := s.projects.GetProject(projectID)
	if !ok {
		writeError(w, http.StatusNotFound, "project not found")
		return
	}

	snap, err := s.checkProjectHealth(*proj)
	if err != nil {
		fmt.Printf("[health] manual trigger for %s failed: %v\n", proj.Name, err)
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}

	// checkProjectHealth doesn't know its own "previous" snapshot (that's
	// the row it just superseded) — look it up now the same way the GET
	// handler does, so a manually-triggered response includes the trend
	// delta too, not just the fresh score on its own.
	_, previous, err := s.store.LatestHealthSnapshots(projectID)
	if err != nil {
		previous = nil // trend is best-effort; still return the fresh score either way
	}

	writeJSON(w, http.StatusOK, s.buildHealthResponse(projectID, snap, previous))
}

// buildHealthResponse assembles the API response shared by both the GET
// and manual-trigger handlers: the snapshot itself, the trend delta if a
// previous snapshot exists, and the org average if the project belongs to
// one that has at least one other scored project.
func (s *Server) buildHealthResponse(projectID string, latest, previous *HealthSnapshot) api.ProjectHealthResponse {
	resp := api.ProjectHealthResponse{
		ProjectID:    latest.ProjectID,
		PipelineName: latest.PipelineName,
		Score:        latest.Score,
		ComputedAt:   latest.ComputedAt,
		Findings:     latest.Findings,
	}
	if previous != nil {
		resp.PreviousScore = &previous.Score
		resp.PreviousAt = &previous.ComputedAt
	}

	if proj, _, _, ok := s.projects.GetProject(projectID); ok && proj.OrgID != "" {
		if avg, count, err := s.store.OrgHealthAverage(proj.OrgID); err == nil && count > 0 {
			resp.OrgAverage = &avg
			resp.OrgProjectCount = count
		}
	}

	return resp
}
