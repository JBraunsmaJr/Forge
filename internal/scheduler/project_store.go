package scheduler

import (
	"database/sql"
	"encoding/json"
	"fmt"

	"github.com/JBraunsmaJr/forge/internal/api"
	"github.com/JBraunsmaJr/forge/internal/store"
	"gorm.io/datatypes"
	"gorm.io/gorm"
)

// ProjectStore manages source repo registrations.
type ProjectStore struct {
	db  *sql.DB
	gdb *gorm.DB
}

func newProjectStore(db *sql.DB, gdb *gorm.DB) *ProjectStore {
	return &ProjectStore{db: db, gdb: gdb}
}

// CreateProject registers a new project and generates a webhook secret.
func (p *ProjectStore) CreateProject(orgID string, req api.CreateProjectRequest) (*api.ProjectInfo, error) {
	id := newID()[:12]
	secret := newID()

	branchFilterJSON, _ := json.Marshal(req.BranchFilter)
	if len(req.BranchFilter) == 0 {
		branchFilterJSON = []byte("[]")
	}

	project := store.Project{
		ID:            id,
		Name:          req.Name,
		RepoURL:       req.RepoURL,
		PipelinePath:  req.PipelinePath,
		WebhookSecret: secret,
		SCMToken:      req.SCMToken,
		BranchFilter:  datatypes.JSON(branchFilterJSON),
	}

	if orgID != "" {
		project.OrgID = &orgID
	}

	if err := p.gdb.Create(&project).Error; err != nil {
		return nil, fmt.Errorf("creating project: %w", err)
	}

	return &api.ProjectInfo{
		ID:            id,
		OrgID:         orgID,
		Name:          req.Name,
		RepoURL:       req.RepoURL,
		PipelinePath:  req.PipelinePath,
		BranchFilter:  req.BranchFilter,
		WebhookSecret: secret,
		CreatedAt:     project.CreatedAt,
	}, nil
}

// GetProject returns a project by ID or Name, including its webhook secret (for verification).
func (p *ProjectStore) GetProject(projectIDOrName string) (*api.ProjectInfo, string, string, bool) {
	var project store.Project
	if err := p.gdb.Where("id = ? OR name = ?", projectIDOrName, projectIDOrName).First(&project).Error; err != nil {
		return nil, "", "", false
	}

	info := api.ProjectInfo{
		ID:            project.ID,
		Name:          project.Name,
		RepoURL:       project.RepoURL,
		PipelinePath:  project.PipelinePath,
		CreatedAt:     project.CreatedAt,
		Cron:          project.Cron,
		ScheduledPath: project.ScheduledPipelinePath,
	}
	if project.OrgID != nil {
		info.OrgID = *project.OrgID
	}
	json.Unmarshal(project.BranchFilter, &info.BranchFilter)

	return &info, project.WebhookSecret, project.SCMToken, true
}

// GetProjectByRepo finds a project by its repo URL.
func (p *ProjectStore) GetProjectByRepo(repoURL string) (*api.ProjectInfo, string, string, bool) {
	var project store.Project
	if err := p.gdb.Where("repo_url = ?", repoURL).First(&project).Error; err != nil {
		return nil, "", "", false
	}

	info := api.ProjectInfo{
		ID:            project.ID,
		Name:          project.Name,
		RepoURL:       project.RepoURL,
		PipelinePath:  project.PipelinePath,
		CreatedAt:     project.CreatedAt,
		Cron:          project.Cron,
		ScheduledPath: project.ScheduledPipelinePath,
	}
	if project.OrgID != nil {
		info.OrgID = *project.OrgID
	}
	json.Unmarshal(project.BranchFilter, &info.BranchFilter)

	return &info, project.WebhookSecret, project.SCMToken, true
}

// UpdateProject updates an existing project.
func (p *ProjectStore) UpdateProject(id string, req api.UpdateProjectRequest) error {
	var project store.Project
	if err := p.gdb.Where("id = ? OR name = ?", id, id).First(&project).Error; err != nil {
		if err == gorm.ErrRecordNotFound {
			return fmt.Errorf("project not found")
		}
		return err
	}

	updates := make(map[string]any)
	if req.Name != nil {
		updates["name"] = *req.Name
	}
	if req.RepoURL != nil {
		updates["repo_url"] = *req.RepoURL
	}
	if req.PipelinePath != nil {
		updates["pipeline_path"] = *req.PipelinePath
	}
	if req.Cron != nil {
		updates["cron"] = *req.Cron
	}
	if req.ScheduledPath != nil {
		updates["scheduled_pipeline_path"] = *req.ScheduledPath
	}
	if req.SCMToken != nil {
		updates["scm_token"] = *req.SCMToken
	}
	if req.BranchFilter != nil {
		branchFilterJSON, _ := json.Marshal(req.BranchFilter)
		if len(req.BranchFilter) == 0 {
			branchFilterJSON = []byte("[]")
		}
		updates["branch_filter"] = datatypes.JSON(branchFilterJSON)
	}

	if len(updates) > 0 {
		return p.gdb.Model(&project).Updates(updates).Error
	}
	return nil
}

// DeleteProject removes a project.
func (p *ProjectStore) DeleteProject(id string) error {
	return p.gdb.Where("id = ? OR name = ?", id, id).Delete(&store.Project{}).Error
}

// ListProjects returns all projects for an org.
func (p *ProjectStore) ListProjects(orgID string) []api.ProjectInfo {
	var projects []store.Project
	query := p.gdb.Order("created_at DESC")
	if orgID != "" {
		query = query.Where("org_id = ?", orgID)
	}

	if err := query.Find(&projects).Error; err != nil {
		return nil
	}

	result := make([]api.ProjectInfo, len(projects))
	for i, project := range projects {
		info := api.ProjectInfo{
			ID:            project.ID,
			Name:          project.Name,
			RepoURL:       project.RepoURL,
			PipelinePath:  project.PipelinePath,
			CreatedAt:     project.CreatedAt,
			Cron:          project.Cron,
			ScheduledPath: project.ScheduledPipelinePath,
		}
		if project.OrgID != nil {
			info.OrgID = *project.OrgID
		}
		json.Unmarshal(project.BranchFilter, &info.BranchFilter)
		result[i] = info
	}
	return result
}
