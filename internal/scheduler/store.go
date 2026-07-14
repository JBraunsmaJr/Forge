package scheduler

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/JBraunsmaJr/forge/internal/api"
)

// Store is the Postgres-backed implementation of the job store.
type Store struct {
	db *sql.DB
}

// NewStore creates a Store backed by the given database connection.
func NewStore(db *sql.DB) *Store {
	return &Store{db: db}
}

// SubmitRun inserts a new run and all its jobs in a single transaction.
func (s *Store) SubmitRun(name, workspaceDir, orgID, projectID, ref, commitSHA, scmProvider, preferredAgentID string, steps []api.StepDef, appliedStepIDs []string, parentRunID string) (string, error) {
	return s.SubmitRunWithID(newID(), name, workspaceDir, orgID, projectID, ref, commitSHA, scmProvider, preferredAgentID, steps, appliedStepIDs, parentRunID)
}

// SubmitRunWithID is like SubmitRun but uses a caller-provided run ID.
// Used by webhook handlers which allocate the ID before creating the
// workspace directory (so the dir name can include the run ID).
func (s *Store) SubmitRunWithID(runID, name, workspaceDir, orgID, projectID, ref, commitSHA, scmProvider, preferredAgentID string, steps []api.StepDef, appliedStepIDs []string, parentRunID string) (string, error) {

	stepIDsJSON, _ := json.Marshal(appliedStepIDs)

	tx, err := s.db.Begin()
	if err != nil {
		return "", fmt.Errorf("begin transaction: %w", err)
	}
	defer tx.Rollback()

	var orgIDParam interface{}
	if orgID != "" {
		orgIDParam = orgID
	}

	var parentRunParam interface{}
	if parentRunID != "" {
		parentRunParam = parentRunID
	}

	var preferredAgentParam interface{}
	if preferredAgentID != "" {
		preferredAgentParam = preferredAgentID
	}

	_, err = tx.Exec(
		`INSERT INTO runs (id, name, workspace_dir, applied_step_ids, org_id, project_id, ref, commit_sha, scm_provider, parent_run_id, preferred_agent_id)
		 VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11)`,
		runID, name, workspaceDir, stepIDsJSON, orgIDParam, projectID, ref, commitSHA, scmProvider, parentRunParam, preferredAgentParam,
	)
	if err != nil {
		return "", fmt.Errorf("insert run: %w", err)
	}

	for _, step := range steps {
		command := step.Command
		if len(command) == 0 && step.Run != "" {
			command = []string{"sh", "-c", step.Run}
		}
		workDir := step.WorkDir
		if workDir == "" {
			workDir = "/workspace"
		}
		stepType := step.Type
		if stepType == "" {
			stepType = "task"
		}
		timeout := step.Timeout
		if timeout == 0 {
			timeout = 30 * time.Minute
		}

		status := string(step.Status)
		if status == "" {
			status = "pending"
			if len(step.DependsOn) == 0 {
				if stepType == "approval" {
					status = string(api.JobStatusApproval)
				} else if stepType == "release" {
					status = string(api.JobStatusRelease)
				} else {
					status = "queued"
				}
			}
		}

		if err := insertJob(tx, runID, step, command, workDir, stepType, timeout, status); err != nil {
			return "", fmt.Errorf("insert job %s: %w", step.ID, err)
		}

		// If this is a rerun and the job was already passed, copy its artifacts
		if parentRunID != "" && status == string(api.JobStatusPassed) {
			_, err = tx.Exec(`
				INSERT INTO artifacts (id, run_id, job_id, name, filename, size_bytes, content_type, storage_key, confirmed, created_at)
				SELECT md5(random()::text || clock_timestamp()::text), $1, 'rerun-skipped', name, filename, size_bytes, content_type, storage_key, true, created_at
				FROM artifacts
				WHERE run_id = $2 AND job_id IN (
					SELECT id FROM jobs WHERE run_id = $2 AND step_id = $3
				) AND confirmed = true
			`, runID, parentRunID, step.ID)
			if err != nil {
				fmt.Printf("[store] failed to copy artifacts for rerun step %s: %v\n", step.ID, err)
			}
		}
	}

	if err := s.unlockDownstream(tx, runID); err != nil {
		return "", fmt.Errorf("unlock downstream: %w", err)
	}

	if err := tx.Commit(); err != nil {
		return "", fmt.Errorf("commit: %w", err)
	}
	return runID, nil
}

// insertJob inserts a single job row inside a transaction.
func insertJob(tx *sql.Tx, runID string, step api.StepDef,
	command []string, workDir, stepType string,
	timeout time.Duration, status string) error {

	jobID := newID()
	toJSON := func(v any) []byte { b, _ := json.Marshal(v); return b }

	pipelineRefJSON := toJSON(step.PipelineRef)
	releaseConfigJSON := toJSON(step.Release)
	artifactUploadsJSON := toJSON(step.ArtifactUploads)
	artifactDownloadsJSON := toJSON(step.ArtifactDownloads)

	_, err := tx.Exec(`
		INSERT INTO jobs (
			id, run_id, step_id, step_type, image, entrypoint, command, work_dir,
			env, inputs, timeout_ns, depends_on, secret_names,
			policy_source, condition, always_run, docker_socket, pipeline_ref,
			release_config, artifact_uploads, artifact_downloads, status
		) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16,$17,$18,$19,$20,$21,$22)`,
		jobID, runID, step.ID, stepType, step.Image, toJSON(step.Entrypoint),
		toJSON(command), workDir,
		toJSON(step.Env), toJSON(step.Inputs), int64(timeout),
		toJSON(step.DependsOn), toJSON(step.SecretNames),
		step.PolicySource, step.Condition, step.AlwaysRun, step.DockerSocket, pipelineRefJSON,
		releaseConfigJSON, artifactUploadsJSON, artifactDownloadsJSON,
		status,
	)
	return err
}

// LeaseNext atomically finds and claims the next queued job.
// Returns (nil, false) if the queue is empty.
//
// SELECT FOR UPDATE SKIP LOCKED is the core of a Postgres-based job queue.
// See package doc for why this works safely with concurrent agents.
func (s *Store) LeaseNext(agentID string) (*api.JobSpec, bool) {
	leaseID := newID()
	now := time.Now()

	row := s.db.QueryRow(`
		WITH next AS (
			SELECT jobs.id FROM jobs
			JOIN runs ON jobs.run_id = runs.id
			WHERE  jobs.status = 'queued'
			ORDER BY
				CASE
					WHEN runs.preferred_agent_id = $2 THEN 0
					WHEN runs.preferred_agent_id IS NULL THEN 1
					ELSE 2
				END ASC,
				jobs.id ASC
			LIMIT  1
			FOR UPDATE SKIP LOCKED
		)
		UPDATE jobs
		SET    status       = 'running',
		       lease_id     = $1,
		       agent_id     = $2,
		       leased_at    = $3,
		       heartbeat_at = $3,
		       started_at   = $3
		FROM   next, runs
		WHERE  jobs.id = next.id AND jobs.run_id = runs.id
		RETURNING
			jobs.id, jobs.run_id, jobs.step_id, jobs.image, jobs.entrypoint,
			jobs.command, jobs.work_dir, jobs.env, jobs.inputs,
			jobs.timeout_ns, jobs.secret_names, jobs.step_type,
			jobs.docker_socket,
			COALESCE(jobs.condition, ''),
			jobs.always_run,
			COALESCE(jobs.pipeline_ref::text, 'null'),
			COALESCE(jobs.release_config::text, 'null'),
			COALESCE(jobs.artifact_uploads::text,   '[]'),
			COALESCE(jobs.artifact_downloads::text, '[]'),
			runs.workspace_dir,
			runs.applied_step_ids
		`,
		leaseID, agentID, now,
	)

	return s.scanJobSpec(row, leaseID)
}

// LeaseReleaseJob atomically finds and claims the next release job for the scheduler to process.
func (s *Store) LeaseReleaseJob() (*api.JobSpec, bool) {
	leaseID := newID()
	now := time.Now()

	row := s.db.QueryRow(`
		WITH next AS (
			SELECT id FROM jobs
			WHERE  status = 'release'
			LIMIT  1
			FOR UPDATE SKIP LOCKED
		)
		UPDATE jobs
		SET    status       = 'running',
		       lease_id     = $1,
		       agent_id     = 'scheduler',
		       leased_at    = $2,
		       heartbeat_at = $2,
		       started_at   = $2
		FROM   next, runs
		WHERE  jobs.id = next.id AND jobs.run_id = runs.id
		RETURNING
			jobs.id, jobs.run_id, jobs.step_id, jobs.image, jobs.entrypoint,
			jobs.command, jobs.work_dir, jobs.env, jobs.inputs,
			jobs.timeout_ns, jobs.secret_names, jobs.step_type,
			jobs.docker_socket,
			COALESCE(jobs.condition, ''),
			jobs.always_run,
			COALESCE(jobs.pipeline_ref::text, 'null'),
			COALESCE(jobs.release_config::text, 'null'),
			COALESCE(jobs.artifact_uploads::text,   '[]'),
			COALESCE(jobs.artifact_downloads::text, '[]'),
			runs.workspace_dir,
			runs.applied_step_ids
		`,
		leaseID, now,
	)

	return s.scanJobSpec(row, leaseID)
}

func (s *Store) scanJobSpec(row *sql.Row, leaseID string) (*api.JobSpec, bool) {
	var (
		jobID, runID, stepID, image, stepType      string
		entrypointJSON, commandJSON, envJSON       []byte
		inputsJSON, secretsJSON                    []byte
		pipelineRefJSON, releaseConfigJSON         string
		artifactUploadsJSON, artifactDownloadsJSON string
		workDir, workspaceDir                      string
		appliedStepIDsJSON                         []byte
		timeoutNS                                  int64
		dockerSocket                               bool
		condition                                  string
		alwaysRun                                  bool
	)
	err := row.Scan(
		&jobID, &runID, &stepID, &image, &entrypointJSON,
		&commandJSON, &workDir, &envJSON, &inputsJSON,
		&timeoutNS, &secretsJSON, &stepType,
		&dockerSocket,
		&condition,
		&alwaysRun,
		&pipelineRefJSON,
		&releaseConfigJSON,
		&artifactUploadsJSON, &artifactDownloadsJSON,
		&workspaceDir,
		&appliedStepIDsJSON,
	)
	if err == sql.ErrNoRows {
		return nil, false
	}
	if err != nil {
		fmt.Printf("[store] job scan error: %v\n", err)
		return nil, false
	}

	var entrypoint, command, secretNames []string
	var env map[string]string
	var inputs, appliedStepIDs []string
	json.Unmarshal(entrypointJSON, &entrypoint)
	json.Unmarshal(commandJSON, &command)
	json.Unmarshal(envJSON, &env)
	json.Unmarshal(inputsJSON, &inputs)
	json.Unmarshal(secretsJSON, &secretNames)
	json.Unmarshal(appliedStepIDsJSON, &appliedStepIDs)

	var pipelineRef *api.PipelineRef
	if pipelineRefJSON != "null" && pipelineRefJSON != "" {
		json.Unmarshal([]byte(pipelineRefJSON), &pipelineRef)
	}

	var releaseConfig *api.ReleaseConfig
	if releaseConfigJSON != "null" && releaseConfigJSON != "" {
		json.Unmarshal([]byte(releaseConfigJSON), &releaseConfig)
	}

	var artifactUploads []api.ArtifactUploadSpec
	var artifactDownloads []api.ArtifactDownloadSpec
	json.Unmarshal([]byte(artifactUploadsJSON), &artifactUploads)
	json.Unmarshal([]byte(artifactDownloadsJSON), &artifactDownloads)

	// Fetch org_id, project_id and ref from the parent run for secret scoping and conditions.
	var orgID, projectID, ref, commitSHA string
	s.db.QueryRow(`SELECT COALESCE(org_id,''), COALESCE(project_id,''), COALESCE(ref,''), COALESCE(commit_sha,'') FROM runs WHERE id=$1`, runID).
		Scan(&orgID, &projectID, &ref, &commitSHA)

	return &api.JobSpec{
		JobID:             jobID,
		RunID:             runID,
		LeaseID:           leaseID,
		StepID:            stepID,
		Image:             image,
		Entrypoint:        entrypoint,
		Command:           command,
		WorkDir:           workDir,
		Env:               env,
		Inputs:            inputs,
		Timeout:           time.Duration(timeoutNS),
		SecretNames:       secretNames,
		DockerSocket:      dockerSocket,
		Type:              stepType,
		Condition:         condition,
		AppliedStepIDs:    appliedStepIDs,
		WorkspaceDir:      workspaceDir,
		AlwaysRun:         alwaysRun,
		OrgID:             orgID,
		ProjectID:         projectID,
		Ref:               ref,
		CommitSHA:         commitSHA,
		PipelineRef:       pipelineRef,
		Release:           releaseConfig,
		ArtifactUploads:   artifactUploads,
		ArtifactDownloads: artifactDownloads,
	}, true
}

// Heartbeat updates the last-seen timestamp for a running job.
func (s *Store) Heartbeat(jobID, leaseID, agentID string) error {
	res, err := s.db.Exec(`
		UPDATE jobs SET heartbeat_at = NOW()
		WHERE  id = $1 AND lease_id = $2 AND status = 'running'`,
		jobID, leaseID,
	)
	if err != nil {
		return err
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return fmt.Errorf("lease mismatch or job not running")
	}
	return nil
}

// ActiveAgentsCount returns the number of unique agents that heartbeated in the last 2 minutes.
func (s *Store) ActiveAgentsCount() (int, error) {
	var count int
	err := s.db.QueryRow(`
		SELECT COUNT(DISTINCT agent_id) FROM jobs 
		WHERE status = 'running' AND heartbeat_at > NOW() - INTERVAL '2 minutes'
	`).Scan(&count)
	return count, err
}

func (s *Store) ActiveJobsCount(agentID string) (int, error) {
	var count int
	err := s.db.QueryRow(`SELECT COUNT(*) FROM jobs WHERE agent_id = $1 AND status = 'running'`, agentID).Scan(&count)
	return count, err
}

// ReclaimStaleJobs resets running jobs whose heartbeat has expired.
// Called by the heartbeat monitor goroutine every 15 seconds.
func (s *Store) ReclaimStaleJobs() int {
	res, err := s.db.Exec(`
		UPDATE jobs
		SET    status = CASE WHEN step_type = 'release' THEN 'release' ELSE 'queued' END,
		       lease_id = '', agent_id = ''
		WHERE  status       = 'running'
		AND    heartbeat_at < NOW() - INTERVAL '30 seconds'`)
	if err != nil {
		return 0
	}
	n, _ := res.RowsAffected()
	if n > 0 {
		fmt.Printf("[scheduler] reclaimed %d stale job(s)\n", n)
	}
	return int(n)
}

// Complete marks a job done, stores its logs, adds any emitted steps,
// and unblocks downstream jobs whose dependencies are now satisfied.
func (s *Store) Complete(jobID, leaseID string, exitCode int, durationMs int64,
	logs []api.LogEvent, emittedSteps []api.StepDef, skipped bool, timedOut bool) (string, error) {

	tx, err := s.db.Begin()
	if err != nil {
		return "", fmt.Errorf("begin: %w", err)
	}
	defer tx.Rollback()

	status := "passed"
	if timedOut {
		status = string(api.JobStatusTimedOut)
	} else if skipped {
		status = string(api.JobStatusSkipped)
	} else if exitCode != 0 {
		status = string(api.JobStatusFailed)
	}
	var runID, stepID string
	err = tx.QueryRow(`
		UPDATE jobs
		SET    status = $1, exit_code = $2, duration_ms = $3, finished_at = NOW()
		WHERE  id = $4 AND lease_id = $5 AND status = 'running'
		RETURNING run_id, step_id`,
		status, exitCode, durationMs, jobID, leaseID,
	).Scan(&runID, &stepID)
	if err == sql.ErrNoRows {
		return "", fmt.Errorf("lease mismatch or job already canceled: ignored")
	}
	if err != nil {
		return "", fmt.Errorf("updating job: %w", err)
	}

	if len(logs) > 0 {
		if _, err := tx.Exec(`DELETE FROM job_logs WHERE job_id = $1`, jobID); err != nil {
			return "", fmt.Errorf("clearing logs: %w", err)
		}
		for _, log := range logs {
			_, err = tx.Exec(
				`INSERT INTO job_logs (job_id, ts, level, message) VALUES ($1,$2,$3,$4)`,
				jobID, log.Timestamp, log.Level, log.Message,
			)
			if err != nil {
				return "", fmt.Errorf("inserting log: %w", err)
			}
		}
	}

	if status == "passed" && len(emittedSteps) > 0 {

		if err := s.insertEmittedSteps(tx, runID, jobID, stepID, emittedSteps); err != nil {
			return "", err
		}
	}

	if err := s.unlockDownstream(tx, runID); err != nil {
		return "", err
	}

	return runID, tx.Commit()
}

// insertEmittedSteps adds dynamically generated steps to a live run.
func (s *Store) insertEmittedSteps(tx *sql.Tx, runID, generatorJobID, generatorStepID string, steps []api.StepDef) error {
	// Record emitted step IDs on the generator job.
	var ids []string
	for _, s := range steps {
		ids = append(ids, s.ID)
	}
	idsJSON, _ := json.Marshal(ids)
	_, err := tx.Exec(`UPDATE jobs SET emitted_step_ids=$1 WHERE id=$2`, idsJSON, generatorJobID)
	if err != nil {
		return err
	}

	for _, step := range steps {
		cmd := step.Command
		if len(cmd) == 0 && step.Run != "" {
			cmd = []string{"sh", "-c", step.Run}
		}
		workDir := step.WorkDir
		if workDir == "" {
			workDir = "/workspace"
		}
		stepType := step.Type
		if stepType == "" {
			stepType = "task"
		}
		timeout := step.Timeout
		if timeout == 0 {
			timeout = 30 * time.Minute
		}

		// Ensure the emitted step depends on the generator.
		deps := step.DependsOn
		hasGen := false
		for _, d := range deps {
			if d == generatorStepID {
				hasGen = true
				break
			}
		}
		if !hasGen {
			deps = append(deps, generatorStepID)
		}
		step.DependsOn = deps

		status := "queued"
		if err := insertJob(tx, runID, step, cmd, workDir, stepType, timeout, status); err != nil {
			return fmt.Errorf("inserting emitted step %s: %w", step.ID, err)
		}
	}
	return nil
}

// unlockDownstream promotes pending jobs to queued when all their
// dependencies have passed. Called inside a transaction after a job passes.
//
// This replaces the old in-memory graph walk with a pure SQL query:
// a job is ready when every step it depends on appears in the set of
// passed step IDs for this run.
func (s *Store) unlockDownstream(tx *sql.Tx, runID string) error {
	var ref string
	if err := tx.QueryRow(`SELECT COALESCE(ref,'') FROM runs WHERE id=$1`, runID).Scan(&ref); err != nil {
		return err
	}

	rows, err := tx.Query(
		`SELECT step_id, status, depends_on, condition, always_run, id, step_type FROM jobs WHERE run_id=$1`, runID)
	if err != nil {
		return err
	}
	defer rows.Close()

	type jobInfo struct {
		dbID      string
		stepID    string
		status    string
		deps      []string
		condition string
		alwaysRun bool
		stepType  string
	}

	allJobs := make(map[string]jobInfo)
	var pending []jobInfo

	for rows.Next() {
		var ji jobInfo
		var depsJSON []byte
		if err := rows.Scan(&ji.stepID, &ji.status, &depsJSON, &ji.condition, &ji.alwaysRun, &ji.dbID, &ji.stepType); err != nil {
			return err
		}
		json.Unmarshal(depsJSON, &ji.deps)
		allJobs[ji.stepID] = ji
		if ji.status == "pending" {
			pending = append(pending, ji)
		}
	}

	rows2, err := tx.Query(
		`SELECT step_id, emitted_step_ids FROM jobs WHERE run_id=$1 AND emitted_step_ids != '[]'`, runID)
	if err != nil {
		return err
	}
	defer rows2.Close()
	generatorEmits := map[string][]string{}
	for rows2.Next() {
		var stepID string
		var raw []byte
		rows2.Scan(&stepID, &raw)
		var ids []string
		json.Unmarshal(raw, &ids)
		generatorEmits[stepID] = ids
	}

	changed := false
	for _, job := range pending {
		allFinished := true
		anyFailed := false

		for _, depID := range job.deps {
			dep, ok := allJobs[depID]
			if !ok {
				fmt.Printf("[store] warning: dependency %q not found for job %q\n", depID, job.stepID)
				allFinished = false
				break
			}

			if dep.status == "pending" || dep.status == "queued" || dep.status == "running" || dep.status == "approval" || dep.status == "release" {
				allFinished = false
				break
			}
			if dep.status == "failed" || dep.status == "canceled" || dep.status == "skipped" || dep.status == "timed_out" {
				anyFailed = true
			}

			if emits, ok := generatorEmits[depID]; ok {
				for _, emittedID := range emits {
					emitted, ok := allJobs[emittedID]
					if !ok || emitted.status == "pending" || emitted.status == "queued" || emitted.status == "running" || emitted.status == "approval" {
						allFinished = false
						break
					}
					if emitted.status == "failed" || emitted.status == "canceled" || emitted.status == "skipped" || emitted.status == "timed_out" {
						anyFailed = true
					}
				}
				if !allFinished {
					break
				}
			}
		}

		if allFinished {
			runPassed := !anyFailed
			var newStatus string

			isAlways := strings.Contains(strings.ToLower(job.condition), "always()") || job.alwaysRun
			isFailure := strings.Contains(strings.ToLower(job.condition), "failure()")

			if !runPassed && !isAlways && !isFailure {
				newStatus = string(api.JobStatusSkipped)
			} else {
				if evaluateCondition(job.condition, runPassed || job.alwaysRun, ref) {
					if job.stepType == "approval" {
						newStatus = string(api.JobStatusApproval)
					} else if job.stepType == "release" {
						newStatus = string(api.JobStatusRelease)
					} else {
						newStatus = string(api.JobStatusQueued)
					}
				} else {
					newStatus = string(api.JobStatusSkipped)
				}
			}

			if newStatus != "" {
				var err error
				if newStatus == string(api.JobStatusApproval) {
					_, err = tx.Exec(`UPDATE jobs SET status=$1, started_at=NOW() WHERE id=$2`, newStatus, job.dbID)
				} else {
					_, err = tx.Exec(`UPDATE jobs SET status=$1 WHERE id=$2`, newStatus, job.dbID)
				}
				if err != nil {
					return err
				}
				changed = true
			}
		}
	}

	if changed {
		return s.unlockDownstream(tx, runID)
	}

	return nil
}

func (s *Store) ApproveJob(jobID string) error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()

	var runID string
	err = tx.QueryRow(`
		UPDATE jobs 
		SET    status = 'passed', 
		       finished_at = NOW(),
		       duration_ms = COALESCE(EXTRACT(EPOCH FROM (NOW() - started_at)) * 1000, 0)::BIGINT
		WHERE  id = $1 AND status = 'approval'
		RETURNING run_id`, jobID).Scan(&runID)
	if err == sql.ErrNoRows {
		return fmt.Errorf("job not found or not in approval state")
	}
	if err != nil {
		return err
	}

	if err := s.unlockDownstream(tx, runID); err != nil {
		return err
	}

	if err := tx.Commit(); err != nil {
		return err
	}
	return nil
}

func (s *Store) GetRunComparison(runID string) (*api.RunComparison, error) {
	var projectID string
	var durationMs int64
	err := s.db.QueryRow(`
		SELECT r.project_id, 
		       COALESCE(EXTRACT(EPOCH FROM (MAX(j.finished_at) - MIN(j.started_at))) * 1000, 0)::BIGINT as duration_ms
		FROM runs r
		JOIN jobs j ON j.run_id = r.id
		WHERE r.id = $1
		GROUP BY r.id, r.project_id`, runID).Scan(&projectID, &durationMs)
	if err != nil {
		return nil, err
	}

	if projectID == "" {
		return &api.RunComparison{RunID: runID, DurationMs: durationMs}, nil
	}

	// Calculate average of last 10 successful runs
	var avgDuration float64
	err = s.db.QueryRow(`
		SELECT AVG(duration_ms) FROM (
			SELECT COALESCE(EXTRACT(EPOCH FROM (MAX(j.finished_at) - MIN(j.started_at))) * 1000, 0) as duration_ms
			FROM runs r
			JOIN jobs j ON j.run_id = r.id
			WHERE r.project_id = $1 AND r.id != $2
			GROUP BY r.id
			ORDER BY MAX(j.finished_at) DESC
			LIMIT 10
		) as last_runs`, projectID, runID).Scan(&avgDuration)

	if err != nil || avgDuration == 0 {
		return &api.RunComparison{RunID: runID, DurationMs: durationMs}, nil
	}

	diff := durationMs - int64(avgDuration)
	percent := (float64(diff) / avgDuration) * 100

	return &api.RunComparison{
		RunID:              runID,
		DurationMs:         durationMs,
		AvgDurationMs:      int64(avgDuration),
		DiffMs:             diff,
		PercentChange:      percent,
		RegressionDetected: percent > 10, // 10% threshold
	}, nil
}

func (s *Store) GetJobRunID(jobID string) string {
	var runID string
	s.db.QueryRow(`SELECT run_id FROM jobs WHERE id=$1`, jobID).Scan(&runID)
	return runID
}

// ── Query methods ─────────────────────────────────────────────────────────────

// RunStatus returns a lightweight status snapshot (used by `forge status`).
func (s *Store) RunStatus(runID string) (*api.RunStatus, bool) {
	rows, err := s.db.Query(
		`SELECT id, step_id, status FROM jobs WHERE run_id=$1 ORDER BY started_at NULLS FIRST, id`, runID)
	if err != nil {
		return nil, false
	}
	defer rows.Close()

	var statuses []api.JobStatus
	var ids []string
	for rows.Next() {
		var id, stepID, status string
		rows.Scan(&id, &stepID, &status)
		statuses = append(statuses, api.JobStatus(status))
		ids = append(ids, id)
	}

	if statuses == nil {
		statuses = []api.JobStatus{}
	}
	if ids == nil {
		ids = []string{}
	}

	var name string
	s.db.QueryRow(`SELECT name FROM runs WHERE id=$1`, runID).Scan(&name)

	return &api.RunStatus{
		RunID:  runID,
		Name:   name,
		Status: overallStatus(statuses),
		Jobs:   statuses,
		JobIDs: ids,
	}, true
}

// ListRuns returns all runs newest-first (for the web UI sidebar).
// ListRunsOptions controls filtering and pagination for ListRuns.
type ListRunsOptions struct {
	Status string // filter by status: "passed","failed","running","canceled","" (any)
	Search string // filter by name substring (case-insensitive)
	Limit  int    // max results (default 50)
	Offset int    // skip this many results
}

func (s *Store) ListRuns(opts ListRunsOptions) []api.RunSummary {
	if opts.Limit <= 0 {
		opts.Limit = 50
	}

	rows, err := s.db.Query(`
		WITH aggregated AS (
			SELECT r.id, r.name, r.created_at,
			       COUNT(j.id) AS job_count,
			       CASE 
			           WHEN bool_or(j.status IN ('running', 'queued', 'release')) THEN 'running'
			           WHEN bool_or(j.status = 'approval') THEN 'approval'
			           WHEN bool_or(j.status = 'pending') THEN 'running'
			           WHEN bool_or(j.status IN ('failed', 'timed_out')) THEN 'failed'
			           WHEN bool_or(j.status = 'canceled') THEN 'canceled'
			           ELSE 'passed'
			       END as status
			FROM   runs r
			LEFT   JOIN jobs j ON j.run_id = r.id
			WHERE  ($1 = '' OR LOWER(r.name) LIKE '%' || LOWER($1) || '%')
			GROUP  BY r.id, r.name, r.created_at
		)
		SELECT id, name, created_at, job_count, status
		FROM   aggregated
		WHERE  ($4 = '' OR status = $4)
		ORDER  BY created_at DESC
		LIMIT  $2 OFFSET $3`,
		opts.Search, opts.Limit, opts.Offset, opts.Status)
	if err != nil {
		return nil
	}
	defer rows.Close()

	result := []api.RunSummary{}
	for rows.Next() {
		var r api.RunSummary
		var status string
		if err := rows.Scan(&r.RunID, &r.Name, &r.CreatedAt, &r.JobCount, &status); err != nil {
			continue
		}
		r.Status = api.JobStatus(status)
		result = append(result, r)
	}
	return result
}

// RunDetail returns the full run state for the DAG view.
func (s *Store) RunDetail(runID string) (*api.RunDetail, bool) {
	detail := &api.RunDetail{RunID: runID}
	var orgID, projectID, commitSHA, scmProvider sql.NullString
	err := s.db.QueryRow(
		`SELECT name, created_at, org_id, project_id, commit_sha, scm_provider FROM runs WHERE id=$1`, runID,
	).Scan(&detail.Name, &detail.CreatedAt, &orgID, &projectID, &commitSHA, &scmProvider)
	if err != nil {
		return nil, false
	}

	if orgID.Valid {
		detail.OrgID = orgID.String
	}
	if projectID.Valid {
		detail.ProjectID = projectID.String
	}
	if commitSHA.Valid {
		detail.CommitSHA = commitSHA.String
	}
	if scmProvider.Valid {
		detail.SCMProvider = scmProvider.String
	}

	rows, err := s.db.Query(`
		SELECT id, step_id, status, depends_on,
		       duration_ms, timeout_ns, started_at, finished_at, exit_code, policy_source
		FROM   jobs WHERE run_id=$1`, runID)
	if err != nil {
		return nil, false
	}
	defer rows.Close()

	var jobs []api.JobDetail
	var statuses []api.JobStatus
	for rows.Next() {
		var j api.JobDetail
		var depsJSON []byte
		rows.Scan(&j.JobID, &j.StepID, &j.Status, &depsJSON,
			&j.DurationMs, &j.TimeoutNS, &j.StartedAt, &j.FinishedAt, &j.ExitCode, &j.PolicySource)
		json.Unmarshal(depsJSON, &j.DependsOn)
		if j.DependsOn == nil {
			j.DependsOn = []string{}
		}
		jobs = append(jobs, j)
		statuses = append(statuses, j.Status)
	}

	if jobs == nil {
		jobs = []api.JobDetail{}
	}
	detail.Jobs = jobs
	detail.Status = overallStatus(statuses)

	return detail, true
}

// GetJobLogs returns stored log events for a job.
func (s *Store) GetJobLogs(jobID string) ([]api.LogEvent, bool) {
	// Check job exists.
	var count int
	s.db.QueryRow(`SELECT COUNT(*) FROM jobs WHERE id=$1`, jobID).Scan(&count)
	if count == 0 {
		return nil, false
	}

	rows, err := s.db.Query(
		`SELECT ts, level, message FROM job_logs WHERE job_id=$1 ORDER BY id`, jobID)
	if err != nil {
		return []api.LogEvent{}, true
	}
	defer rows.Close()

	var logs []api.LogEvent
	for rows.Next() {
		var e api.LogEvent
		rows.Scan(&e.Timestamp, &e.Level, &e.Message)
		logs = append(logs, e)
	}
	if logs == nil {
		logs = []api.LogEvent{}
	}
	return logs, true
}

func (s *Store) SearchLogs(query string, orgID, projectID, runID, jobID string, limit int) ([]api.LogSearchResult, error) {
	if limit <= 0 {
		limit = 100
	}

	sqlStr := `
		SELECT l.ts, l.level, l.message, l.job_id, j.step_id, j.run_id, r.name, COALESCE(r.org_id, ''), COALESCE(r.project_id, '')
		FROM job_logs l
		JOIN jobs j ON l.job_id = j.id
		JOIN runs r ON j.run_id = r.id
		WHERE l.message ILIKE $1
	`
	args := []any{"%" + query + "%"}
	argCount := 1

	if orgID != "" {
		argCount++
		sqlStr += fmt.Sprintf(" AND r.org_id = $%d", argCount)
		args = append(args, orgID)
	}
	if projectID != "" {
		argCount++
		sqlStr += fmt.Sprintf(" AND r.project_id = $%d", argCount)
		args = append(args, projectID)
	}
	if runID != "" {
		argCount++
		sqlStr += fmt.Sprintf(" AND j.run_id = $%d", argCount)
		args = append(args, runID)
	}
	if jobID != "" {
		argCount++
		sqlStr += fmt.Sprintf(" AND l.job_id = $%d", argCount)
		args = append(args, jobID)
	}

	sqlStr += " ORDER BY l.ts DESC LIMIT " + fmt.Sprint(limit)

	rows, err := s.db.Query(sqlStr, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var results []api.LogSearchResult
	for rows.Next() {
		var res api.LogSearchResult
		err := rows.Scan(
			&res.Timestamp, &res.Level, &res.Message,
			&res.JobID, &res.JobName, &res.RunID, &res.RunName, &res.OrgID, &res.ProjectID,
		)
		if err != nil {
			continue
		}
		results = append(results, res)
	}
	return results, nil
}

func (s *Store) PruneLogs(before time.Time) (int64, error) {
	res, err := s.db.Exec(`DELETE FROM job_logs WHERE ts < $1`, before)
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}

// RunDetailByJobID finds the run containing a job (used by debug sessions).
func (s *Store) RunDetailByJobID(jobID string) (*api.RunDetail, bool) {
	var runID string
	err := s.db.QueryRow(`SELECT run_id FROM jobs WHERE id=$1`, jobID).Scan(&runID)
	if err != nil {
		return nil, false
	}
	return s.RunDetail(runID)
}

// GetJobDetails returns image/workdir/workspaceDir and repo info for a debug session.
func (s *Store) GetJobDetails(jobID string) (image, workDir, workspaceDir, projectID, commitSHA string, dockerSocket bool) {
	var runID string
	s.db.QueryRow(
		`SELECT image, work_dir, run_id, docker_socket FROM jobs WHERE id=$1`, jobID,
	).Scan(&image, &workDir, &runID, &dockerSocket)
	s.db.QueryRow(
		`SELECT workspace_dir, project_id, commit_sha FROM runs WHERE id=$1`, runID,
	).Scan(&workspaceDir, &projectID, &commitSHA)
	return
}

// GetJobEnv returns a job's environment map for debug sessions.
func (s *Store) GetJobEnv(jobID string) map[string]string {
	var envJSON []byte
	s.db.QueryRow(`SELECT env FROM jobs WHERE id=$1`, jobID).Scan(&envJSON)
	var env map[string]string
	json.Unmarshal(envJSON, &env)
	return env
}

func (s *Store) jobStatuses(runID string) []api.JobStatus {
	rows, _ := s.db.Query(`SELECT status FROM jobs WHERE run_id=$1`, runID)
	if rows == nil {
		return nil
	}
	defer rows.Close()
	var result []api.JobStatus
	for rows.Next() {
		var st string
		rows.Scan(&st)
		result = append(result, api.JobStatus(st))
	}
	return result
}

func overallStatus(statuses []api.JobStatus) api.JobStatus {
	hasRunning := false
	hasQueued := false
	hasApproval := false
	hasRelease := false
	hasPending := false
	hasFailed := false
	hasCanceled := false

	for _, s := range statuses {
		switch s {
		case api.JobStatusRunning:
			hasRunning = true
		case api.JobStatusQueued:
			hasQueued = true
		case api.JobStatusApproval:
			hasApproval = true
		case api.JobStatusRelease:
			hasRelease = true
		case api.JobStatusPending:
			hasPending = true
		case api.JobStatusFailed, api.JobStatusTimedOut:
			hasFailed = true
		case api.JobStatusCanceled:
			hasCanceled = true
		}
	}

	if hasRunning || hasQueued || hasRelease {
		return api.JobStatusRunning
	}
	if hasApproval {
		return api.JobStatusApproval
	}
	if hasPending {
		return api.JobStatusRunning
	}
	if hasFailed {
		return api.JobStatusFailed
	}
	if hasCanceled {
		return api.JobStatusCanceled
	}
	return api.JobStatusPassed
}

// AppendJobLogs adds log events to a running job's log buffer.
// Called by the streaming endpoint as the agent forwards lines in real time.
// Verifies the lease so only the agent running the job can append.
func (s *Store) AppendJobLogs(jobID, leaseID string, events []api.LogEvent) error {
	// Verify the lease is still valid.
	var currentLease string
	err := s.db.QueryRow(
		`SELECT lease_id FROM jobs WHERE id=$1 AND status='running'`, jobID,
	).Scan(&currentLease)
	if err != nil || currentLease != leaseID {
		return fmt.Errorf("invalid lease or job not running")
	}

	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()

	for _, e := range events {
		_, err = tx.Exec(
			`INSERT INTO job_logs (job_id, ts, level, message) VALUES ($1,$2,$3,$4)`,
			jobID, e.Timestamp, e.Level, e.Message,
		)
		if err != nil {
			return err
		}
	}
	return tx.Commit()
}

func (s *Store) GetRunsOlderThan(olderThan time.Duration) ([]string, error) {
	cutoff := time.Now().Add(-olderThan)
	rows, err := s.db.Query(`SELECT id FROM runs WHERE created_at <= $1`, cutoff)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err == nil {
			ids = append(ids, id)
		}
	}
	return ids, nil
}

// PruneRuns deletes runs older than the given duration.
// Pass duration=0 to delete all runs. Cascading deletes in the schema handle
// jobs, job_logs, and artifacts automatically.
func (s *Store) PruneRuns(olderThan time.Duration) (int64, error) {
	var res sql.Result
	var err error
	if olderThan == 0 {
		res, err = s.db.Exec(`DELETE FROM runs`)
	} else {
		cutoff := time.Now().Add(-olderThan)
		res, err = s.db.Exec(`DELETE FROM runs WHERE created_at <= $1`, cutoff)
	}
	if err != nil {
		return 0, fmt.Errorf("pruning runs: %w", err)
	}
	return res.RowsAffected()
}

// RunCount returns the total number of runs stored.
func (s *Store) RunCount() (int64, error) {
	var n int64
	err := s.db.QueryRow(`SELECT COUNT(*) FROM runs`).Scan(&n)
	return n, err
}

// CancelRun marks all queued and running jobs in a run as canceled.
func (s *Store) CancelRun(runID string) (int64, error) {
	res, err := s.db.Exec(`
		UPDATE jobs
		SET    status = 'canceled', finished_at = NOW()
		WHERE  run_id = $1 AND status IN ('queued', 'pending', 'running')`,
		runID,
	)
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}

// GetJobStepID returns the logical step_id for a job. Used by flaky detection
// to group outcomes by step name rather than by job UUID.
func (s *Store) GetJobStepID(jobID string) string {
	var stepID string
	s.db.QueryRow(`SELECT step_id FROM jobs WHERE id = $1`, jobID).Scan(&stepID)
	return stepID
}

// RerunSteps returns the original step definitions and workspace dir for a run
// so it can be resubmitted as a new run.
func (s *Store) RerunSteps(runID string) (name string, steps []api.StepDef, workspaceDir, orgID, projectID, ref, commitSHA, preferredAgentID string, appliedStepIDs []string, err error) {
	var stepIDsJSON []byte
	err = s.db.QueryRow(`SELECT name, workspace_dir, COALESCE(org_id,''), COALESCE(project_id,''), COALESCE(ref,''), COALESCE(commit_sha,''), COALESCE(preferred_agent_id,''), applied_step_ids FROM runs WHERE id=$1`, runID).
		Scan(&name, &workspaceDir, &orgID, &projectID, &ref, &commitSHA, &preferredAgentID, &stepIDsJSON)
	if err == sql.ErrNoRows {
		return "", nil, "", "", "", "", "", "", nil, fmt.Errorf("run %s not found", runID)
	}
	if err != nil {
		return "", nil, "", "", "", "", "", "", nil, err
	}
	json.Unmarshal(stepIDsJSON, &appliedStepIDs)

	rows, err := s.db.Query(`
		SELECT step_id, step_type, image, entrypoint, command, work_dir, env,
		       inputs, timeout_ns, depends_on, secret_names, policy_source,
		       docker_socket, condition, always_run,
		       COALESCE(pipeline_ref::text, 'null'),
		       COALESCE(release_config::text, 'null'),
		       COALESCE(artifact_uploads::text, '[]'),
		       COALESCE(artifact_downloads::text, '[]'),
		       status
		FROM jobs WHERE run_id=$1 ORDER BY started_at NULLS FIRST, id`, runID)
	if err != nil {
		return "", nil, "", "", "", "", "", "", nil, err
	}
	defer rows.Close()

	for rows.Next() {
		var stepID, stepType, image, workDir, policySource string
		var pipelineRefJSON, releaseConfigJSON, artifactUploadsJSON, artifactDownloadsJSON, status string
		var entrypointJSON, commandJSON, envJSON, inputsJSON, dependsJSON, secretsJSON []byte
		var timeoutNS int64
		var dockerSocket bool
		var condition string
		var alwaysRun bool

		if err := rows.Scan(
			&stepID, &stepType, &image, &entrypointJSON, &commandJSON, &workDir, &envJSON,
			&inputsJSON, &timeoutNS, &dependsJSON, &secretsJSON, &policySource,
			&dockerSocket, &condition, &alwaysRun,
			&pipelineRefJSON, &releaseConfigJSON, &artifactUploadsJSON, &artifactDownloadsJSON,
			&status,
		); err != nil {
			continue
		}

		var entrypoint, command, dependsOn, secretNames []string
		var env map[string]string
		var inputs []string
		var artifactUploads []api.ArtifactUploadSpec
		var artifactDownloads []api.ArtifactDownloadSpec
		var pipelineRef *api.PipelineRef
		var releaseConfig *api.ReleaseConfig
		json.Unmarshal(entrypointJSON, &entrypoint)
		json.Unmarshal(commandJSON, &command)
		json.Unmarshal(envJSON, &env)
		json.Unmarshal(inputsJSON, &inputs)
		json.Unmarshal(dependsJSON, &dependsOn)
		json.Unmarshal(secretsJSON, &secretNames)
		json.Unmarshal([]byte(artifactUploadsJSON), &artifactUploads)
		json.Unmarshal([]byte(artifactDownloadsJSON), &artifactDownloads)
		if pipelineRefJSON != "null" {
			json.Unmarshal([]byte(pipelineRefJSON), &pipelineRef)
		}
		if releaseConfigJSON != "null" {
			json.Unmarshal([]byte(releaseConfigJSON), &releaseConfig)
		}

		steps = append(steps, api.StepDef{
			ID:                stepID,
			Image:             image,
			Entrypoint:        entrypoint,
			Command:           command,
			WorkDir:           workDir,
			Env:               env,
			Inputs:            inputs,
			Timeout:           time.Duration(timeoutNS),
			DependsOn:         dependsOn,
			SecretNames:       secretNames,
			DockerSocket:      dockerSocket,
			Type:              stepType,
			PolicySource:      policySource,
			Condition:         condition,
			AlwaysRun:         alwaysRun,
			ArtifactUploads:   artifactUploads,
			ArtifactDownloads: artifactDownloads,
			PipelineRef:       pipelineRef,
			Release:           releaseConfig,
			Status:            api.JobStatus(status),
		})
	}
	return name, steps, workspaceDir, orgID, projectID, ref, commitSHA, preferredAgentID, appliedStepIDs, nil
}

// RecordStepResult stores a step outcome for flaky detection analysis.
func (s *Store) RecordStepResult(runID, pipelineName, stepID, status string, durationMs int64) {
	s.db.Exec(`
		INSERT INTO step_results (run_id, pipeline_name, step_id, status, duration_ms)
		VALUES ($1, $2, $3, $4, $5)`,
		runID, pipelineName, stepID, status, durationMs,
	)
}

// FlakySteps returns steps with a flake rate above the threshold across the
// last windowDays days and at least minRuns observations.
func (s *Store) FlakySteps(windowDays, minRuns int, minFlakeRate float64) ([]api.FlakyStep, error) {
	rows, err := s.db.Query(`
		SELECT
			pipeline_name,
			step_id,
			COUNT(*)                                         AS total_runs,
			SUM(CASE WHEN status = 'failed' THEN 1 ELSE 0 END) AS failures,
			MAX(created_at)                                  AS last_seen
		FROM step_results
		WHERE created_at > NOW() - ($1 || ' days')::INTERVAL
		GROUP BY pipeline_name, step_id
		HAVING COUNT(*) >= $2
		   AND SUM(CASE WHEN status = 'failed' THEN 1 ELSE 0 END) > 0
		   AND SUM(CASE WHEN status = 'passed' THEN 1 ELSE 0 END) > 0
		ORDER BY (SUM(CASE WHEN status = 'failed' THEN 1 ELSE 0 END)::float / COUNT(*)) DESC`,
		windowDays, minRuns,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var results []api.FlakyStep
	for rows.Next() {
		var f api.FlakyStep
		var lastSeen string
		var totalRuns, failures int
		if err := rows.Scan(&f.PipelineName, &f.StepID, &totalRuns, &failures, &lastSeen); err != nil {
			continue
		}
		f.TotalRuns = totalRuns
		f.Failures = failures
		f.FlakeRate = float64(failures) / float64(totalRuns)
		f.LastSeen = lastSeen
		if f.FlakeRate >= minFlakeRate {
			results = append(results, f)
		}
	}
	return results, nil
}
