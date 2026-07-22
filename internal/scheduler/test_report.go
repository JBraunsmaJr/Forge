package scheduler

import (
	"github.com/JBraunsmaJr/forge/internal/api"
)

func (s *Store) RecordTestReport(req api.RecordTestReportRequest) error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()

	// project_id carries a foreign key; an empty string is not NULL and
	// violates it, which 500'd duration recording for any run without a
	// project (e.g. direct API submissions).
	var projectID any
	if req.ProjectID != "" {
		projectID = req.ProjectID
	}

	for _, f := range req.Report.Files {
		_, err := tx.Exec(`
			INSERT INTO test_file_durations (
				run_id, job_id, project_id, pipeline_name, step_id,
				file_path, duration_ms, test_count, passed, failed, skipped
			) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11)`,
			req.RunID, req.JobID, projectID, req.PipelineName, req.StepID,
			f.Path, f.DurationMS, f.Tests, f.Passed, f.Failed, f.Skipped,
		)
		if err != nil {
			return err
		}
	}

	return tx.Commit()
}

func (s *Store) GetFlakyByDuration(projectID string) ([]api.FlakyDurationResult, error) {
	rows, err := s.db.Query(`
		SELECT
			file_path,
			AVG(CASE WHEN failed > 0 THEN duration_ms END)  AS avg_fail_ms,
			AVG(CASE WHEN failed = 0 THEN duration_ms END)  AS avg_pass_ms,
			AVG(CASE WHEN failed = 0 THEN duration_ms END) /
			NULLIF(AVG(CASE WHEN failed > 0 THEN duration_ms END), 0) AS ratio
		FROM test_file_durations
		WHERE project_id = $1
		  AND created_at > NOW() - INTERVAL '14 days'
		GROUP BY file_path
		HAVING
			COUNT(CASE WHEN failed > 0 THEN 1 END) >= 3
			AND COUNT(CASE WHEN failed = 0 THEN 1 END) >= 3
			AND (
				AVG(CASE WHEN failed > 0 THEN duration_ms END) < 
				AVG(CASE WHEN failed = 0 THEN duration_ms END) * 0.3
				OR
				AVG(CASE WHEN failed > 0 THEN duration_ms END) > 
				AVG(CASE WHEN failed = 0 THEN duration_ms END) * 3.0
			)
		ORDER BY ratio ASC`, projectID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var results []api.FlakyDurationResult
	for rows.Next() {
		var r api.FlakyDurationResult
		if err := rows.Scan(&r.FilePath, &r.AvgFailMS, &r.AvgPassMS, &r.Ratio); err != nil {
			return nil, err
		}
		results = append(results, r)
	}
	return results, nil
}
