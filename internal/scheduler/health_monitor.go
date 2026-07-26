package scheduler

import (
	"context"
	"fmt"
	"os"
	"strconv"
	"time"

	"github.com/JBraunsmaJr/forge/internal/api"
	"github.com/JBraunsmaJr/forge/internal/compiler"
)

// defaultHealthCheckInterval matches issue #46's "weekly (or configurable)"
// — configurable via FORGE_HEALTH_CHECK_INTERVAL_HOURS (an integer number
// of hours; 168 = 7 days).
const defaultHealthCheckInterval = 7 * 24 * time.Hour

func healthCheckInterval() time.Duration {
	if v := os.Getenv("FORGE_HEALTH_CHECK_INTERVAL_HOURS"); v != "" {
		if hours, err := strconv.Atoi(v); err == nil && hours > 0 {
			return time.Duration(hours) * time.Hour
		}
	}
	return defaultHealthCheckInterval
}

// healthMonitor periodically scores every project's pipeline and records
// a snapshot. It never submits a run: this is read-only analysis of the
// pipeline file's current content on the project's default branch, using
// the same git cache the webhook path uses for the actual fetch — just
// without ever handing the result to the job queue.
func (s *Server) healthMonitor(ctx context.Context) {
	interval := healthCheckInterval()
	ticker := time.NewTicker(1 * time.Hour)
	defer ticker.Stop()

	// Run once shortly after startup too, so a fresh deployment doesn't
	// wait a full hour for the first pass.
	s.runHealthChecks(interval)

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			s.runHealthChecks(interval)
		}
	}
}

const maxConcurrentHealthChecks = 5

func (s *Server) runHealthChecks(interval time.Duration) {
	projects := s.projects.ListProjects("")
	now := time.Now()
	sem := make(chan struct{}, maxConcurrentHealthChecks)

	for _, proj := range projects {
		latest, _, err := s.store.LatestHealthSnapshots(proj.ID)
		if err != nil {
			fmt.Printf("[health] failed to read snapshot history for %s: %v\n", proj.Name, err)
			continue
		}
		if latest != nil && now.Sub(latest.ComputedAt) < interval {
			continue // not due yet
		}

		sem <- struct{}{}
		go func(p api.ProjectInfo) {
			defer func() { <-sem }()
			if _, err := s.checkProjectHealth(p); err != nil {
				fmt.Printf("[health] %s: %v\n", p.Name, err)
			}
		}(proj)
	}
}

func (s *Server) checkProjectHealth(proj api.ProjectInfo) (*HealthSnapshot, error) {
	fullProj, _, scmToken, ok := s.projects.GetProject(proj.ID)
	if !ok {
		return nil, fmt.Errorf("project %s disappeared before check ran", proj.Name)
	}

	_, defaultBranch, err := s.gitCache.ListBranches(fullProj.RepoURL)
	if err != nil || defaultBranch == "" {
		defaultBranch = "main"
	}

	if err := s.gitCache.Sync(fullProj.RepoURL, scmToken); err != nil {
		return nil, fmt.Errorf("failed to sync repo: %w", err)
	}
	commitSHA, err := s.gitCache.ResolveCommit(fullProj.RepoURL, defaultBranch)
	if err != nil {
		return nil, fmt.Errorf("failed to resolve %s@%s: %w", fullProj.RepoURL, defaultBranch, err)
	}

	pipelinePath, pipelineData, err := readProjectPipelineFile(s, fullProj.RepoURL, commitSHA, fullProj.PipelinePath)
	if err != nil {
		return nil, err
	}

	var lastModified *time.Time
	if t, err := s.gitCache.LastCommitTime(fullProj.RepoURL, commitSHA, pipelinePath); err == nil {
		lastModified = &t
	}

	report, err := compiler.Score(pipelinePath, pipelineData, lastModified)
	if err != nil {
		return nil, fmt.Errorf("scoring failed: %w", err)
	}

	findings := make([]api.HealthFinding, 0, len(report.Findings))
	for _, f := range report.Findings {
		findings = append(findings, api.HealthFinding{Severity: string(f.Severity), Message: f.Message})
	}

	if err := s.store.RecordHealthSnapshot(proj.ID, report.PipelineName, report.Score, findings); err != nil {
		return nil, fmt.Errorf("failed to record snapshot: %w", err)
	}
	fmt.Printf("[health] %s: scored %d/100 (%d findings)\n", proj.Name, report.Score, len(findings))

	// Approximates the row that was just inserted (ComputedAt is this
	// process's clock, not the DB's NOW() from the INSERT — close enough
	// for immediate UI feedback; a subsequent GET reads the exact row).
	return &HealthSnapshot{
		ProjectID:    proj.ID,
		ComputedAt:   time.Now(),
		PipelineName: report.PipelineName,
		Score:        report.Score,
		Findings:     findings,
	}, nil
}

// readProjectPipelineFile mirrors triggerWebhookRun's own default-path
// fallback (webhook.go) — deliberately duplicated, not shared; see the
// comment on healthMonitor above for why.
func readProjectPipelineFile(s *Server, repoURL, commitSHA, configuredPath string) (path string, data []byte, err error) {
	if configuredPath != "" && configuredPath != ".forge/pipeline.json" {
		data, err = s.gitCache.ReadFile(repoURL, commitSHA, configuredPath)
		if err != nil {
			return "", nil, fmt.Errorf("reading pipeline file %s: %w", configuredPath, err)
		}
		return configuredPath, data, nil
	}

	defaults := []string{".forge/pipeline.yml", ".forge/pipeline.yaml", ".forge/pipeline.json"}
	var lastErr error
	for _, p := range defaults {
		data, err = s.gitCache.ReadFile(repoURL, commitSHA, p)
		if err == nil {
			return p, data, nil
		}
		lastErr = err
	}
	return "", nil, fmt.Errorf("pipeline file not found at any of the default locations (.forge/pipeline.{yml,yaml,json}): %w", lastErr)
}
