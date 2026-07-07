package scheduler

import (
	"fmt"
	"net/http"
	"strings"
)

// handleServeSource serves a tar.gz archive of the repository at a specific commit.
// GET /api/v1/source/{id}?commit={sha}
func (s *Server) handleServeSource(w http.ResponseWriter, r *http.Request) {
	projectID := r.PathValue("id")
	commit := r.URL.Query().Get("commit")
	if commit == "" {
		writeError(w, http.StatusBadRequest, "missing commit parameter")
		return
	}

	proj, _, _, ok := s.projects.GetProject(projectID)
	if !ok {
		writeError(w, http.StatusNotFound, "project not found")
		return
	}

	w.Header().Set("Content-Type", "application/gzip")
	// Use a filename that includes the project name and short commit for convenience.
	filename := fmt.Sprintf("%s-%s.tar.gz", proj.Name, commit)
	if len(commit) > 8 {
		filename = fmt.Sprintf("%s-%s.tar.gz", proj.Name, commit[:8])
	}
	w.Header().Set("Content-Disposition", fmt.Sprintf("attachment; filename=\"%s\"", filename))

	// We don't call w.WriteHeader(200) yet, so we can still return an error
	// if WriteArchive fails early (e.g. repo not found or invalid commit).
	// WriteArchive will start writing to w, which will trigger 200 OK.
	if err := s.gitCache.WriteArchive(proj.RepoURL, commit, w); err != nil {
		// Log error.
		fmt.Printf("Error writing source archive for project %s at commit %s: %v\n", projectID, commit, err)

		// Check if headers were already sent.
		// If they were, we can't send a proper HTTP error anymore.
		// In this case, just returning is the best we can do.
		// If headers weren't sent, we can send a 500.
		// Note: http.ResponseWriter doesn't have a way to check this directly in Go,
		// but we can assume if WriteArchive returned an error, it might have written something.
		// However, with our new checkCmd in WriteArchive, most failures happen BEFORE writing.

		// If the error is "repository not found" or "invalid commit", we know it happened BEFORE writing.
		if strings.Contains(err.Error(), "not found") {
			http.Error(w, "Repository not found in cache. It might still be syncing.", http.StatusServiceUnavailable)
			return
		}
		if strings.Contains(err.Error(), "invalid commit") {
			http.Error(w, "Invalid commit or reference", http.StatusBadRequest)
			return
		}

		// Fallback for other errors.
		// We try to send 500, but if headers were sent, this will just log a warning in the server.
		http.Error(w, "Failed to generate source archive", http.StatusInternalServerError)
	}
}
