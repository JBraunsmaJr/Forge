// Package scheduler — Postgres-backed project store.
package scheduler

import (
	"database/sql"
	"fmt"
	"time"

	"github.com/JBraunsmaJr/forge/internal/api"
)

// ProjectStore manages source repo registrations.
type ProjectStore struct {
	db *sql.DB
}

func newProjectStore(db *sql.DB) *ProjectStore {
	return &ProjectStore{db: db}
}

// CreateProject registers a new project and generates a webhook secret.
func (p *ProjectStore) CreateProject(orgID string, req api.CreateProjectRequest) (*api.ProjectInfo, error) {
	id := newID()[:12]
	secret := newID()

	pipelinePath := req.PipelinePath
	if pipelinePath == "" {
		pipelinePath = ".forge/pipeline.json"
	}

	// Use nil for org_id when empty — the column allows NULL.
	// Passing an empty string would violate the foreign-key constraint.
	var orgIDParam interface{}
	if orgID != "" {
		orgIDParam = orgID
	}

	var createdAt time.Time
	err := p.db.QueryRow(`
		INSERT INTO projects (id, org_id, name, repo_url, pipeline_path, webhook_secret, scm_token)
		VALUES ($1,$2,$3,$4,$5,$6,$7)
		RETURNING created_at`,
		id, orgIDParam, req.Name, req.RepoURL, pipelinePath, secret, req.SCMToken,
	).Scan(&createdAt)
	if err != nil {
		return nil, fmt.Errorf("creating project: %w", err)
	}

	return &api.ProjectInfo{
		ID:            id,
		OrgID:         orgID,
		Name:          req.Name,
		RepoURL:       req.RepoURL,
		PipelinePath:  pipelinePath,
		WebhookSecret: secret,
		CreatedAt:     createdAt,
	}, nil
}

// GetProject returns a project by ID or Name, including its webhook secret (for verification).
func (p *ProjectStore) GetProject(projectIDOrName string) (*api.ProjectInfo, string, string, bool) {
	var info api.ProjectInfo
	var secret, scmToken string
	err := p.db.QueryRow(`
		SELECT id, org_id, name, repo_url, pipeline_path, webhook_secret, scm_token, created_at
		FROM projects WHERE id=$1 OR name=$1
		LIMIT 1`, projectIDOrName,
	).Scan(&info.ID, &info.OrgID, &info.Name, &info.RepoURL, &info.PipelinePath,
		&secret, &scmToken, &info.CreatedAt)
	if err != nil {
		return nil, "", "", false
	}
	return &info, secret, scmToken, true
}

// GetProjectByRepo finds a project by its repo URL.
func (p *ProjectStore) GetProjectByRepo(repoURL string) (*api.ProjectInfo, string, string, bool) {
	var info api.ProjectInfo
	var secret, scmToken string
	err := p.db.QueryRow(`
		SELECT id, org_id, name, repo_url, pipeline_path, webhook_secret, scm_token, created_at
		FROM projects WHERE repo_url=$1`, repoURL,
	).Scan(&info.ID, &info.OrgID, &info.Name, &info.RepoURL, &info.PipelinePath,
		&secret, &scmToken, &info.CreatedAt)
	if err != nil {
		return nil, "", "", false
	}
	return &info, secret, scmToken, true
}

// ListProjects returns all projects for an org.
func (p *ProjectStore) ListProjects(orgID string) []api.ProjectInfo {
	q := `SELECT id, org_id, name, repo_url, pipeline_path, created_at FROM projects`
	args := []any{}
	if orgID != "" {
		q += ` WHERE org_id=$1`
		args = append(args, orgID)
	}
	q += ` ORDER BY created_at DESC`

	rows, err := p.db.Query(q, args...)
	if err != nil {
		return nil
	}
	defer rows.Close()

	var result []api.ProjectInfo
	for rows.Next() {
		var proj api.ProjectInfo
		rows.Scan(&proj.ID, &proj.OrgID, &proj.Name, &proj.RepoURL, &proj.PipelinePath, &proj.CreatedAt)
		result = append(result, proj)
	}
	return result
}
