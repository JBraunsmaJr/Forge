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
func (s *Store) SubmitRun(name, workspaceDir, orgID, projectID, commitSHA, scmProvider string, steps []api.StepDef, appliedPolicies []string) (string, error) {
	return s.SubmitRunWithID(newID(), name, workspaceDir, orgID, projectID, commitSHA, scmProvider, steps, appliedPolicies)
}

// SubmitRunWithID is like SubmitRun but uses a caller-provided run ID.
// Used by webhook handlers which allocate the ID before creating the
// workspace directory (so the dir name can include the run ID).
func (s *Store) SubmitRunWithID(runID, name, workspaceDir, orgID, projectID, commitSHA, scmProvider string, steps []api.StepDef, appliedPolicies []string) (string, error) {

	policiesJSON, _ := json.Marshal(appliedPolicies)

	tx, err := s.db.Begin()
	if err != nil {
		return "", fmt.Errorf("begin transaction: %w", err)
	}
	defer tx.Rollback()

	var orgIDParam interface{}
	if orgID != "" {
		orgIDParam = orgID
	}

	_, err = tx.Exec(
		`INSERT INTO runs (id, name, workspace_dir, applied_policies, org_id, project_id, commit_sha, scm_provider)
		 VALUES ($1, $2, $3, $4, $5, $6, $7, $8)`,
		runID, name, workspaceDir, policiesJSON, orgIDParam, projectID, commitSHA, scmProvider,
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
				status = "queued"
			}
		}

		if err := insertJob(tx, runID, step, command, workDir, stepType, timeout, status); err != nil {
			return "", fmt.Errorf("insert job %s: %w", step.ID, err)
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
	artifactUploadsJSON := toJSON(step.ArtifactUploads)
	artifactDownloadsJSON := toJSON(step.ArtifactDownloads)

	_, err := tx.Exec(`
		INSERT INTO jobs (
			id, run_id, step_id, step_type, image, entrypoint, command, work_dir,
			env, inputs, timeout_ns, depends_on, secret_names,
			policy_source, condition, always_run, docker_socket, pipeline_ref,
			artifact_uploads, artifact_downloads, status
		) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16,$17,$18,$19,$20,$21)`,
		jobID, runID, step.ID, stepType, step.Image, toJSON(step.Entrypoint),
		toJSON(command), workDir,
		toJSON(step.Env), toJSON(step.Inputs), int64(timeout),
		toJSON(step.DependsOn), toJSON(step.SecretNames),
		step.PolicySource, step.Condition, step.AlwaysRun, step.DockerSocket, pipelineRefJSON,
		artifactUploadsJSON, artifactDownloadsJSON,
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
			SELECT id FROM jobs
			WHERE  status = 'queued'
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
		FROM   next
		WHERE  jobs.id = next.id
		RETURNING
			jobs.id, jobs.run_id, jobs.step_id, jobs.image, jobs.entrypoint,
			jobs.command, jobs.work_dir, jobs.env, jobs.inputs,
			jobs.timeout_ns, jobs.secret_names, jobs.step_type,
			jobs.docker_socket,
			COALESCE(jobs.condition, ''),
			jobs.always_run,
			COALESCE(jobs.pipeline_ref::text, 'null'),
			COALESCE(jobs.artifact_uploads::text,   '[]'),
			COALESCE(jobs.artifact_downloads::text, '[]')
		`,
		leaseID, agentID, now,
	)

	var (
		jobID, runID, stepID, image, stepType      string
		entrypointJSON, commandJSON, envJSON       []byte
		inputsJSON, secretsJSON                    []byte
		pipelineRefJSON                            string
		artifactUploadsJSON, artifactDownloadsJSON string
		workDir                                    string
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
		&artifactUploadsJSON, &artifactDownloadsJSON,
	)
	if err == sql.ErrNoRows {
		return nil, false
	}
	if err != nil {
		return nil, false
	}

	var entrypoint, command, secretNames []string
	var env map[string]string
	var inputs []string
	json.Unmarshal(entrypointJSON, &entrypoint)
	json.Unmarshal(commandJSON, &command)
	json.Unmarshal(envJSON, &env)
	json.Unmarshal(inputsJSON, &inputs)
	json.Unmarshal(secretsJSON, &secretNames)

	var pipelineRef *api.PipelineRef
	if pipelineRefJSON != "null" && pipelineRefJSON != "" {
		var ref api.PipelineRef
		if json.Unmarshal([]byte(pipelineRefJSON), &ref) == nil {
			pipelineRef = &ref
		}
	}

	var artifactUploads []api.ArtifactUploadSpec
	var artifactDownloads []api.ArtifactDownloadSpec
	json.Unmarshal([]byte(artifactUploadsJSON), &artifactUploads)
	json.Unmarshal([]byte(artifactDownloadsJSON), &artifactDownloads)

	// Fetch org_id and project_id from the parent run for secret scoping.
	var orgID, projectID, commitSHA string
	s.db.QueryRow(`SELECT COALESCE(org_id,''), COALESCE(project_id,''), COALESCE(commit_sha,'') FROM runs WHERE id=$1`, runID).
		Scan(&orgID, &projectID, &commitSHA)

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
		AlwaysRun:         alwaysRun,
		OrgID:             orgID,
		ProjectID:         projectID,
		CommitSHA:         commitSHA,
		PipelineRef:       pipelineRef,
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

// ReclaimStaleJobs resets running jobs whose heartbeat has expired.
// Called by the heartbeat monitor goroutine every 15 seconds.
func (s *Store) ReclaimStaleJobs() int {
	res, err := s.db.Exec(`
		UPDATE jobs
		SET    status = 'queued', lease_id = '', agent_id = ''
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
	logs []api.LogEvent, emittedSteps []api.StepDef, skipped bool) (string, error) {

	tx, err := s.db.Begin()
	if err != nil {
		return "", fmt.Errorf("begin: %w", err)
	}
	defer tx.Rollback()

	status := "passed"
	if skipped {
		status = "skipped"
	} else if exitCode != 0 {
		status = "failed"
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

	rows, err := tx.Query(
		`SELECT step_id, status, depends_on, condition, always_run, id FROM jobs WHERE run_id=$1`, runID)
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
	}

	allJobs := make(map[string]jobInfo)
	var pending []jobInfo

	for rows.Next() {
		var ji jobInfo
		var depsJSON []byte
		if err := rows.Scan(&ji.stepID, &ji.status, &depsJSON, &ji.condition, &ji.alwaysRun, &ji.dbID); err != nil {
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
				allFinished = false
				break
			}

			if dep.status == "pending" || dep.status == "queued" || dep.status == "running" {
				allFinished = false
				break
			}
			if dep.status == "failed" || dep.status == "canceled" {
				anyFailed = true
			}

			if emits, ok := generatorEmits[depID]; ok {
				for _, emittedID := range emits {
					emitted, ok := allJobs[emittedID]
					if !ok || emitted.status == "pending" || emitted.status == "queued" || emitted.status == "running" {
						allFinished = false
						break
					}
					if emitted.status == "failed" || emitted.status == "canceled" {
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
				if evaluateCondition(job.condition, runPassed || job.alwaysRun) {
					newStatus = string(api.JobStatusQueued)
				} else {
					newStatus = string(api.JobStatusSkipped)
				}
			}

			if newStatus != "" {
				_, err := tx.Exec(`UPDATE jobs SET status=$1 WHERE id=$2`, newStatus, job.dbID)
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
			           WHEN bool_or(j.status IN ('running', 'queued', 'pending')) THEN 'running'
			           WHEN bool_or(j.status = 'failed') THEN 'failed'
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
	var policiesJSON []byte
	var orgID, projectID, commitSHA, scmProvider sql.NullString
	err := s.db.QueryRow(
		`SELECT name, applied_policies, created_at, org_id, project_id, commit_sha, scm_provider FROM runs WHERE id=$1`, runID,
	).Scan(&detail.Name, &policiesJSON, &detail.CreatedAt, &orgID, &projectID, &commitSHA, &scmProvider)
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

	var appliedPolicies []string
	json.Unmarshal(policiesJSON, &appliedPolicies)

	rows, err := s.db.Query(`
		SELECT id, step_id, status, depends_on,
		       duration_ms, exit_code, policy_source
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
			&j.DurationMs, &j.ExitCode, &j.PolicySource)
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
	detail.AppliedPolicies = appliedPolicies
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
	for _, s := range statuses {
		if s == api.JobStatusRunning || s == api.JobStatusQueued || s == api.JobStatusPending {
			return api.JobStatusRunning
		}
	}
	for _, s := range statuses {
		if s == api.JobStatusFailed {
			return api.JobStatusFailed
		}
	}
	for _, s := range statuses {
		if s == api.JobStatusCanceled {
			return api.JobStatusCanceled
		}
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
func (s *Store) RerunSteps(runID string) (name string, steps []api.StepDef, workspaceDir, orgID, projectID, commitSHA string, err error) {
	err = s.db.QueryRow(`SELECT name, workspace_dir, COALESCE(org_id,''), COALESCE(project_id,''), COALESCE(commit_sha,'') FROM runs WHERE id=$1`, runID).
		Scan(&name, &workspaceDir, &orgID, &projectID, &commitSHA)
	if err == sql.ErrNoRows {
		return "", nil, "", "", "", "", fmt.Errorf("run %s not found", runID)
	}
	if err != nil {
		return "", nil, "", "", "", "", err
	}

	rows, err := s.db.Query(`
		SELECT step_id, step_type, image, entrypoint, command, work_dir, env,
		       inputs, timeout_ns, depends_on, secret_names, policy_source,
		       docker_socket,
		       COALESCE(pipeline_ref::text, 'null'),
		       COALESCE(artifact_uploads::text, '[]'),
		       COALESCE(artifact_downloads::text, '[]'),
		       status
		FROM jobs WHERE run_id=$1 ORDER BY started_at NULLS FIRST, id`, runID)
	if err != nil {
		return "", nil, "", "", "", "", err
	}
	defer rows.Close()

	for rows.Next() {
		var stepID, stepType, image, workDir, policySource string
		var pipelineRefJSON, artifactUploadsJSON, artifactDownloadsJSON, status string
		var entrypointJSON, commandJSON, envJSON, inputsJSON, dependsJSON, secretsJSON []byte
		var timeoutNS int64
		var dockerSocket bool

		if err := rows.Scan(
			&stepID, &stepType, &image, &entrypointJSON, &commandJSON, &workDir, &envJSON,
			&inputsJSON, &timeoutNS, &dependsJSON, &secretsJSON, &policySource,
			&dockerSocket,
			&pipelineRefJSON, &artifactUploadsJSON, &artifactDownloadsJSON,
			&status,
		); err != nil {
			continue
		}

		var entrypoint, command, dependsOn, secretNames []string
		var env map[string]string
		var inputs []string
		var artifactUploads []api.ArtifactUploadSpec
		var artifactDownloads []api.ArtifactDownloadSpec
		json.Unmarshal(entrypointJSON, &entrypoint)
		json.Unmarshal(commandJSON, &command)
		json.Unmarshal(envJSON, &env)
		json.Unmarshal(inputsJSON, &inputs)
		json.Unmarshal(dependsJSON, &dependsOn)
		json.Unmarshal(secretsJSON, &secretNames)
		json.Unmarshal([]byte(artifactUploadsJSON), &artifactUploads)
		json.Unmarshal([]byte(artifactDownloadsJSON), &artifactDownloads)

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
			ArtifactUploads:   artifactUploads,
			ArtifactDownloads: artifactDownloads,
			Status:            api.JobStatus(status),
		})
	}
	return name, steps, workspaceDir, orgID, projectID, commitSHA, nil
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
