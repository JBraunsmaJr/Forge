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

	filename := fmt.Sprintf("%s-%s.tar.gz", proj.Name, commit)
	if len(commit) > 8 {
		filename = fmt.Sprintf("%s-%s.tar.gz", proj.Name, commit[:8])
	}
	w.Header().Set("Content-Disposition", fmt.Sprintf("attachment; filename=\"%s\"", filename))

	if err := s.gitCache.WriteArchive(proj.RepoURL, commit, w); err != nil {

		fmt.Printf("Error writing source archive for project %s at commit %s: %v\n", projectID, commit, err)

		if strings.Contains(err.Error(), "not found") {
			http.Error(w, "Repository not found in cache. It might still be syncing.", http.StatusServiceUnavailable)
			return
		}
		if strings.Contains(err.Error(), "invalid commit") {
			http.Error(w, "Invalid commit or reference", http.StatusBadRequest)
			return
		}

		http.Error(w, "Failed to generate source archive", http.StatusInternalServerError)
	}
}
