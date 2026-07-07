// Package scheduler — artifact management HTTP handlers.
package scheduler

import (
	"fmt"
	"io"
	"net/http"
	"strconv"

	"github.com/JBraunsmaJr/forge/internal/api"
	"github.com/JBraunsmaJr/forge/internal/artifacts"
)

// handlePresignUpload gives the agent a pre-signed URL to upload an artifact.
// Agent → POST /api/v1/artifacts/presign
func (s *Server) handlePresignUpload(w http.ResponseWriter, r *http.Request) {
	var req api.PresignUploadRequest
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if req.RunID == "" || req.JobID == "" || req.Name == "" || req.Filename == "" {
		writeError(w, http.StatusBadRequest, "run_id, job_id, name, filename are required")
		return
	}

	resp, err := s.artifacts.PresignUpload(r.Context(), artifacts.PresignRequest{
		RunID:       req.RunID,
		JobID:       req.JobID,
		Name:        req.Name,
		Filename:    req.Filename,
		ContentType: req.ContentType,
	})
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}

	writeJSON(w, http.StatusCreated, api.PresignUploadResponse{
		ArtifactID: resp.ArtifactID,
		UploadURL:  resp.UploadURL,
		Method:     resp.Method,
	})
}

// handleConfirmUpload marks an artifact as fully uploaded.
// Agent → POST /api/v1/artifacts/{id}/confirm
func (s *Server) handleConfirmUpload(w http.ResponseWriter, r *http.Request) {
	var req api.ConfirmUploadRequest
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if err := s.artifacts.ConfirmUpload(r.Context(), r.PathValue("id"), req.SizeBytes); err != nil {
		writeError(w, http.StatusConflict, err.Error())
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// handleGetArtifact returns artifact metadata and a download URL.
// GET /api/v1/artifacts?run_id=X&name=Y
func (s *Server) handleGetArtifact(w http.ResponseWriter, r *http.Request) {
	runID := r.URL.Query().Get("run_id")
	name := r.URL.Query().Get("name")
	if runID == "" || name == "" {
		writeError(w, http.StatusBadRequest, "run_id and name are required")
		return
	}

	meta, err := s.artifacts.GetArtifact(r.Context(), runID, name)
	if err == artifacts.ErrNotFound {
		writeError(w, http.StatusNotFound, fmt.Sprintf("artifact %q not found in run %s", name, runID))
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, toAPIMeta(meta))
}

// handleListArtifacts returns all artifacts for a run.
// GET /api/v1/runs/{runID}/artifacts
func (s *Server) handleListArtifacts(w http.ResponseWriter, r *http.Request) {
	list, err := s.artifacts.ListArtifacts(r.Context(), r.PathValue("runID"))
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	var result []api.ArtifactMeta
	for _, m := range list {
		result = append(result, toAPIMeta(&m))
	}
	writeJSON(w, http.StatusOK, result)
}

// handleArtifactUpload receives a local-backend PUT upload from the agent.
// PUT /api/v1/artifacts/{id}/upload?token=<one-time-token>
// Only routes to LocalStore — S3 agents PUT directly to S3.
func (s *Server) handleArtifactUpload(w http.ResponseWriter, r *http.Request) {
	local, ok := s.artifacts.(*artifacts.LocalStore)
	if !ok {
		writeError(w, http.StatusBadRequest, "direct upload only supported for local backend")
		return
	}

	token := r.URL.Query().Get("token")
	if token == "" {
		writeError(w, http.StatusUnauthorized, "upload token required")
		return
	}

	if err := local.ServeUpload(r.Context(), r.PathValue("id"), token, r.Body); err != nil {
		writeError(w, http.StatusConflict, err.Error())
		return
	}

	// Auto-confirm after local upload (size from Content-Length header).
	size, _ := strconv.ParseInt(r.Header.Get("Content-Length"), 10, 64)
	s.artifacts.ConfirmUpload(r.Context(), r.PathValue("id"), size)

	w.WriteHeader(http.StatusNoContent)
}

// handleArtifactDownload serves a file from the local backend.
// GET /api/v1/artifacts/{id}/download
// S3 download URLs point directly to S3; this endpoint is local-only.
func (s *Server) handleArtifactDownload(w http.ResponseWriter, r *http.Request) {
	local, ok := s.artifacts.(*artifacts.LocalStore)
	if !ok {
		writeError(w, http.StatusBadRequest, "direct download only supported for local backend")
		return
	}

	rc, size, err := local.ServeDownload(r.Context(), r.PathValue("id"))
	if err == artifacts.ErrNotFound {
		writeError(w, http.StatusNotFound, "artifact not found")
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	defer rc.Close()

	if size > 0 {
		w.Header().Set("Content-Length", strconv.FormatInt(size, 10))
	}
	w.Header().Set("Content-Type", "application/octet-stream")
	io.Copy(w, rc)
}

// handleDeleteArtifact removes an artifact.
// DELETE /api/v1/artifacts/{id}
func (s *Server) handleDeleteArtifact(w http.ResponseWriter, r *http.Request) {
	if err := s.artifacts.DeleteArtifact(r.Context(), r.PathValue("id")); err != nil {
		writeError(w, http.StatusNotFound, err.Error())
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func toAPIMeta(m *artifacts.ArtifactMeta) api.ArtifactMeta {
	return api.ArtifactMeta{
		ID:          m.ID,
		RunID:       m.RunID,
		JobID:       m.JobID,
		Name:        m.Name,
		Filename:    m.Filename,
		SizeBytes:   m.SizeBytes,
		ContentType: m.ContentType,
		DownloadURL: m.DownloadURL,
		CreatedAt:   m.CreatedAt,
	}
}
