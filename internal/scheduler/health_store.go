package scheduler

import (
	"encoding/json"
	"fmt"
	"time"

	"github.com/JBraunsmaJr/forge/internal/api"
)

// HealthSnapshot is one row from project_health_snapshots.
type HealthSnapshot struct {
	ID           string
	ProjectID    string
	ComputedAt   time.Time
	PipelineName string
	Score        int
	Findings     []api.HealthFinding
}

// RecordHealthSnapshot inserts a new snapshot for a project. The health
// monitor calls this once per project per check interval; there is
// deliberately no upsert/update path — every check is its own permanent
// row, since the row IS the history the trend and org-average features
// depend on.
func (s *Store) RecordHealthSnapshot(projectID, pipelineName string, score int, findings []api.HealthFinding) error {
	findingsJSON, err := json.Marshal(findings)
	if err != nil {
		return err
	}
	_, err = s.db.Exec(`
		INSERT INTO project_health_snapshots (id, project_id, pipeline_name, score, findings)
		VALUES ($1, $2, $3, $4, $5)`,
		newID(), projectID, pipelineName, score, findingsJSON,
	)
	return err
}

// LatestHealthSnapshots returns the two most recent snapshots for a
// project, newest first — the second (if present) is "last check" for the
// trend delta. Returns (nil, nil, nil) if the project has never been
// checked, which callers should treat as "no data yet," not an error.
func (s *Store) LatestHealthSnapshots(projectID string) (latest, previous *HealthSnapshot, err error) {
	rows, err := s.db.Query(`
		SELECT id, project_id, computed_at, pipeline_name, score, findings::text
		FROM project_health_snapshots
		WHERE project_id = $1
		ORDER BY computed_at DESC
		LIMIT 2`, projectID)
	if err != nil {
		return nil, nil, err
	}
	defer rows.Close()

	var snaps []*HealthSnapshot
	for rows.Next() {
		var h HealthSnapshot
		var findingsJSON string

		if err := rows.Scan(&h.ID, &h.ProjectID, &h.ComputedAt, &h.PipelineName, &h.Score, &findingsJSON); err != nil {
			return nil, nil, err
		}

		if err := json.Unmarshal([]byte(findingsJSON), &h.Findings); err != nil {
			return nil, nil, fmt.Errorf("decoding findings for snapshot %s: %w", h.ID, err)
		}

		snaps = append(snaps, &h)
	}
	if len(snaps) > 0 {
		latest = snaps[0]
	}
	if len(snaps) > 1 {
		previous = snaps[1]
	}
	return latest, previous, nil
}

// OrgHealthAverage averages the LATEST score of every project in orgID
// that has at least one snapshot. Projects never checked (no snapshot at
// all) are excluded rather than counted as 0 — an unscored project isn't
// evidence of a bad pipeline, just an absence of data. Returns
// (0, 0, nil) when there's nothing to average (empty org, or no project
// in it has been checked yet); callers should treat count==0 as "no
// average available," not "average is zero."
func (s *Store) OrgHealthAverage(orgID string) (avg float64, count int, err error) {
	// DISTINCT ON picks each project's most recent snapshot; the outer
	// query then averages exactly one score per project.
	row := s.db.QueryRow(`
		SELECT COALESCE(AVG(latest.score), 0), COUNT(*)
		FROM (
			SELECT DISTINCT ON (h.project_id) h.project_id, h.score
			FROM project_health_snapshots h
			JOIN projects p ON p.id = h.project_id
			WHERE p.org_id = $1
			ORDER BY h.project_id, h.computed_at DESC
		) latest`, orgID)
	err = row.Scan(&avg, &count)
	return avg, count, err
}
