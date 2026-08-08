package scheduler

import (
	"net/http"
	"time"

	"github.com/JBraunsmaJr/forge/internal/api"
	"github.com/JBraunsmaJr/forge/internal/buildnumber"
)

// handleGetBuildFormat returns the configured build-number format and
// version state for a (project, pipeline) scope, plus a sample rendered
// build number so the UI's format editor can show a live preview
// (issue #57). This is also the read side backing
// `forge project get-build-format` / `get-version`.
func (s *Server) handleGetBuildFormat(w http.ResponseWriter, r *http.Request) {
	projectID := r.PathValue("id")
	pipelineName := r.URL.Query().Get("pipeline")
	if pipelineName == "" {
		writeError(w, http.StatusBadRequest, "pipeline query parameter is required")
		return
	}

	rawFormat, major, minor, source, setBy, tagFilter, err := s.store.GetBuildFormat(projectID, pipelineName)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}

	var sample string
	if format, perr := buildnumber.Parse(rawFormat); perr == nil {
		sample = format.Render(1, major, minor, time.Now().UTC())
	}

	writeJSON(w, http.StatusOK, api.BuildFormatInfo{
		ProjectID:         projectID,
		PipelineName:      pipelineName,
		Format:            rawFormat,
		Major:             major,
		Minor:             minor,
		VersionSource:     source,
		VersionSetBy:      setBy,
		VersionTagFilter:  tagFilter,
		SampleBuildNumber: sample,
	})
}

// handleSetBuildFormat validates and persists a build-number format
// string. Rejecting an unknown/malformed token here means it's caught
// at config-save time and never reaches a run — "not first discovered
// at run time" (issue #57).
func (s *Server) handleSetBuildFormat(w http.ResponseWriter, r *http.Request) {
	if !requireOperator(w, r) {
		return
	}
	projectID := r.PathValue("id")
	var req api.SetBuildFormatRequest
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if req.PipelineName == "" {
		writeError(w, http.StatusBadRequest, "pipeline_name is required")
		return
	}

	// Validate here first so a bad format is unambiguously a 400: once
	// this passes, SetBuildFormat re-validates internally too (defense
	// in depth for any other caller), but at that point the only way
	// it can still fail is a genuine persistence problem, which
	// deserves a 500 — matching handleSetVersion/handleSetVersionTagFilter
	// rather than reporting a database error as a client mistake.
	if _, err := buildnumber.Parse(req.Format); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}

	if err := s.store.SetBuildFormat(projectID, req.PipelineName, req.Format); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	s.AuditLog(r, "build_format.set", "project", projectID, map[string]string{
		"pipeline_name": req.PipelineName,
		"format":        req.Format,
	})
	w.WriteHeader(http.StatusNoContent)
}

// handleSetVersion explicitly (manually) sets the major/minor version
// for a (project, pipeline) scope. A later tag push matching the
// configured filter can still override it, and a manual set can in
// turn override a previous tag-derived value.
func (s *Server) handleSetVersion(w http.ResponseWriter, r *http.Request) {
	if !requireOperator(w, r) {
		return
	}
	projectID := r.PathValue("id")
	var req api.SetVersionRequest
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if req.PipelineName == "" {
		writeError(w, http.StatusBadRequest, "pipeline_name is required")
		return
	}

	actor := "unknown"
	if rec, ok := r.Context().Value(contextKeyToken).(*tokenRecord); ok && rec != nil && rec.Name != "" {
		actor = rec.Name
	}

	if err := s.store.SetVersion(projectID, req.PipelineName, req.Major, req.Minor, actor); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	s.AuditLog(r, "build_version.set", "project", projectID, map[string]any{
		"pipeline_name": req.PipelineName,
		"major":         req.Major,
		"minor":         req.Minor,
		"source":        "manual",
	})
	w.WriteHeader(http.StatusNoContent)
}

// handleSetVersionTagFilter configures the branch/ref filter that
// restricts which pushed tags can update the tag-derived major/minor
// version for a (project, pipeline) scope (default: the project's
// default branch), so a tag pushed on a stale or feature branch can't
// unintentionally change mainline builds' version.
func (s *Server) handleSetVersionTagFilter(w http.ResponseWriter, r *http.Request) {
	if !requireOperator(w, r) {
		return
	}
	projectID := r.PathValue("id")
	var req api.SetVersionTagFilterRequest
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if req.PipelineName == "" {
		writeError(w, http.StatusBadRequest, "pipeline_name is required")
		return
	}

	if err := s.store.SetVersionTagFilter(projectID, req.PipelineName, req.Filter); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	s.AuditLog(r, "build_version.set_tag_filter", "project", projectID, map[string]string{
		"pipeline_name": req.PipelineName,
		"filter":        req.Filter,
	})
	w.WriteHeader(http.StatusNoContent)
}
