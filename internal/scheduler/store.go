// Package scheduler — Postgres-backed job store.
//
// Every method that reads or writes job state goes through Postgres.
// No in-memory caching: the database is the source of truth. This means
// the scheduler can be restarted without losing any run state, and multiple
// scheduler instances could be run (though a single instance is fine for now).
//
// # The key query: SELECT FOR UPDATE SKIP LOCKED
//
// LeaseNext uses this pattern to atomically claim exactly one queued job:
//
//	WITH next AS (
//	    SELECT id FROM jobs WHERE status='queued'
//	    FOR UPDATE SKIP LOCKED LIMIT 1
//	)
//	UPDATE jobs SET status='running', lease_id=$1, ...
//	FROM next WHERE jobs.id = next.id
//	RETURNING *;
//
// SKIP LOCKED means: skip any row that another transaction already has locked.
// Two agents calling this concurrently each get a DIFFERENT job — no extra
// coordination, no message broker, no distributed lock.
package scheduler

import (
	"database/sql"
	"encoding/json"
	"fmt"
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

// ── Run management ────────────────────────────────────────────────────────────

// SubmitRun inserts a new run and all its jobs in a single transaction.
func (s *Store) SubmitRun(name, workspaceDir, orgID, projectID string, steps []api.StepDef, appliedPolicies []string) (string, error) {
	return s.SubmitRunWithID(newID(), name, workspaceDir, orgID, projectID, steps, appliedPolicies)
}

// SubmitRunWithID is like SubmitRun but uses a caller-provided run ID.
// Used by webhook handlers which allocate the ID before creating the
// workspace directory (so the dir name can include the run ID).
func (s *Store) SubmitRunWithID(runID, name, workspaceDir, orgID, projectID string, steps []api.StepDef, appliedPolicies []string) (string, error) {

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
		`INSERT INTO runs (id, name, workspace_dir, applied_policies, org_id, project_id)
		 VALUES ($1, $2, $3, $4, $5, $6)`,
		runID, name, workspaceDir, policiesJSON, orgIDParam, projectID,
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

		status := "pending"
		if len(step.DependsOn) == 0 {
			status = "queued"
		}

		if err := insertJob(tx, runID, step, command, workDir, stepType, timeout, status); err != nil {
			return "", fmt.Errorf("insert job %s: %w", step.ID, err)
		}
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

	// Store PipelineRef as JSON in the pipeline_ref column (see schema).
	pipelineRefJSON := toJSON(step.PipelineRef)
	artifactUploadsJSON := toJSON(step.ArtifactUploads)
	artifactDownloadsJSON := toJSON(step.ArtifactDownloads)

	_, err := tx.Exec(`
		INSERT INTO jobs (
			id, run_id, step_id, step_type, image, command, work_dir,
			env, inputs, timeout_ns, depends_on, secret_names,
			policy_source, pipeline_ref, artifact_uploads, artifact_downloads, status
		) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16,$17)`,
		jobID, runID, step.ID, stepType, step.Image,
		toJSON(command), workDir,
		toJSON(step.Env), toJSON(step.Inputs), int64(timeout),
		toJSON(step.DependsOn), toJSON(step.SecretNames),
		step.PolicySource, pipelineRefJSON,
		artifactUploadsJSON, artifactDownloadsJSON,
		status,
	)
	return err
}

// ── Job leasing ───────────────────────────────────────────────────────────────

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
			jobs.id, jobs.run_id, jobs.step_id, jobs.image,
			jobs.command, jobs.work_dir, jobs.env, jobs.inputs,
			jobs.timeout_ns, jobs.secret_names, jobs.step_type,
			COALESCE(jobs.pipeline_ref::text, 'null'),
			COALESCE(jobs.artifact_uploads::text,   '[]'),
			COALESCE(jobs.artifact_downloads::text, '[]')
		`,
		leaseID, agentID, now,
	)

	var (
		jobID, runID, stepID, image, stepType         string
		commandJSON, envJSON, inputsJSON, secretsJSON []byte
		pipelineRefJSON                               string
		artifactUploadsJSON, artifactDownloadsJSON    string
		workDir                                       string
		timeoutNS                                     int64
	)
	err := row.Scan(
		&jobID, &runID, &stepID, &image,
		&commandJSON, &workDir, &envJSON, &inputsJSON,
		&timeoutNS, &secretsJSON, &stepType,
		&pipelineRefJSON,
		&artifactUploadsJSON, &artifactDownloadsJSON,
	)
	if err == sql.ErrNoRows {
		return nil, false
	}
	if err != nil {
		return nil, false
	}

	var command, secretNames []string
	var env map[string]string
	var inputs []string
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
	var orgID, projectID string
	s.db.QueryRow(`SELECT COALESCE(org_id,''), COALESCE(project_id,'') FROM runs WHERE id=$1`, runID).
		Scan(&orgID, &projectID)

	return &api.JobSpec{
		JobID:             jobID,
		RunID:             runID,
		LeaseID:           leaseID,
		StepID:            stepID,
		Image:             image,
		Command:           command,
		WorkDir:           workDir,
		Env:               env,
		Inputs:            inputs,
		Timeout:           time.Duration(timeoutNS),
		SecretNames:       secretNames,
		Type:              stepType,
		OrgID:             orgID,
		ProjectID:         projectID,
		PipelineRef:       pipelineRef,
		ArtifactUploads:   artifactUploads,
		ArtifactDownloads: artifactDownloads,
	}, true
}

// ── Heartbeat ─────────────────────────────────────────────────────────────────

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

// ── Completion ────────────────────────────────────────────────────────────────

// Complete marks a job done, stores its logs, adds any emitted steps,
// and unblocks downstream jobs whose dependencies are now satisfied.
func (s *Store) Complete(jobID, leaseID string, exitCode int, durationMs int64,
	logs []api.LogEvent, emittedSteps []api.StepDef) (string, error) {

	tx, err := s.db.Begin()
	if err != nil {
		return "", fmt.Errorf("begin: %w", err)
	}
	defer tx.Rollback()

	// Mark the job complete.
	status := "passed"
	if exitCode != 0 {
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

	// Store log events.
	for _, log := range logs {
		_, err = tx.Exec(
			`INSERT INTO job_logs (job_id, ts, level, message) VALUES ($1,$2,$3,$4)`,
			jobID, log.Timestamp, log.Level, log.Message,
		)
		if err != nil {
			return "", fmt.Errorf("inserting log: %w", err)
		}
	}

	if status == "passed" {
		// Add emitted steps from generator jobs.
		if len(emittedSteps) > 0 {
			if err := s.insertEmittedSteps(tx, runID, jobID, stepID, emittedSteps); err != nil {
				return "", err
			}
		}
		// Unblock downstream jobs whose dependencies are now all passed.
		if err := s.unlockDownstream(tx, runID); err != nil {
			return "", err
		}
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

		// Emitted steps start as queued if all their deps have passed.
		status := "queued" // will be corrected by unlockDownstream if needed
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
	// Get all passed step IDs for this run.
	rows, err := tx.Query(
		`SELECT step_id FROM jobs WHERE run_id=$1 AND status='passed'`, runID)
	if err != nil {
		return err
	}
	defer rows.Close()

	passed := map[string]bool{}
	for rows.Next() {
		var id string
		rows.Scan(&id)
		passed[id] = true
	}

	// Also get emitted step IDs from generators so we know which
	// downstream jobs must wait for them too.
	rows2, err := tx.Query(
		`SELECT emitted_step_ids FROM jobs WHERE run_id=$1 AND emitted_step_ids != '[]'`, runID)
	if err != nil {
		return err
	}
	defer rows2.Close()

	generatorEmits := map[string]bool{}
	for rows2.Next() {
		var raw []byte
		rows2.Scan(&raw)
		var ids []string
		json.Unmarshal(raw, &ids)
		for _, id := range ids {
			generatorEmits[id] = true
		}
	}

	// Fetch all pending jobs for this run and check if they're unblockable.
	rows3, err := tx.Query(
		`SELECT id, depends_on FROM jobs WHERE run_id=$1 AND status='pending'`, runID)
	if err != nil {
		return err
	}
	defer rows3.Close()

	type pendingJob struct {
		id   string
		deps []string
	}
	var pending []pendingJob
	for rows3.Next() {
		var id string
		var depsJSON []byte
		rows3.Scan(&id, &depsJSON)
		var deps []string
		json.Unmarshal(depsJSON, &deps)
		pending = append(pending, pendingJob{id, deps})
	}

	for _, job := range pending {
		allMet := true
		for _, dep := range job.deps {
			if !passed[dep] {
				allMet = false
				break
			}
			// If dep is a generator, also wait for all its emitted steps.
			if generatorEmits[dep] {
				// re-check that every emitted step of this generator passed
				// (simplified: if any emitted step hasn't passed, hold)
				for emittedID := range generatorEmits {
					if !passed[emittedID] {
						allMet = false
						break
					}
				}
			}
		}
		if allMet {
			tx.Exec(`UPDATE jobs SET status='queued' WHERE id=$1`, job.id)
		}
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
	if len(ids) == 0 {
		return nil, false
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
func (s *Store) ListRuns() []api.RunSummary {
	rows, err := s.db.Query(`
		SELECT r.id, r.name, r.created_at,
		       COUNT(j.id) AS job_count
		FROM   runs r
		LEFT   JOIN jobs j ON j.run_id = r.id
		GROUP  BY r.id, r.name, r.created_at
		ORDER  BY r.created_at DESC`)
	if err != nil {
		return nil
	}
	defer rows.Close()

	var result []api.RunSummary
	for rows.Next() {
		var r api.RunSummary
		rows.Scan(&r.RunID, &r.Name, &r.CreatedAt, &r.JobCount)
		// Compute overall status from individual jobs
		statuses := s.jobStatuses(r.RunID)
		r.Status = overallStatus(statuses)
		result = append(result, r)
	}
	return result
}

// RunDetail returns the full run state for the DAG view.
func (s *Store) RunDetail(runID string) (*api.RunDetail, bool) {
	var name string
	var policiesJSON []byte
	var createdAt time.Time
	err := s.db.QueryRow(
		`SELECT name, applied_policies, created_at FROM runs WHERE id=$1`, runID,
	).Scan(&name, &policiesJSON, &createdAt)
	if err != nil {
		return nil, false
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

	return &api.RunDetail{
		RunID:           runID,
		Name:            name,
		Status:          overallStatus(statuses),
		CreatedAt:       createdAt,
		Jobs:            jobs,
		AppliedPolicies: appliedPolicies,
	}, true
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

// GetJobDetails returns image/workdir/workspaceDir for a debug session.
func (s *Store) GetJobDetails(jobID string) (image, unused, workDir, workspaceDir string) {
	var runID string
	s.db.QueryRow(
		`SELECT image, work_dir, run_id FROM jobs WHERE id=$1`, jobID,
	).Scan(&image, &workDir, &runID)
	s.db.QueryRow(
		`SELECT workspace_dir FROM runs WHERE id=$1`, runID,
	).Scan(&workspaceDir)
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

// ── Helpers ───────────────────────────────────────────────────────────────────

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
		if s == api.JobStatusFailed {
			return api.JobStatusFailed
		}
	}
	for _, s := range statuses {
		if s == api.JobStatusRunning || s == api.JobStatusQueued || s == api.JobStatusPending {
			return api.JobStatusRunning
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

	// Insert each event. A single tx keeps this atomic.
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

// ── Run retention / cleanup ───────────────────────────────────────────────────

// PruneRuns deletes runs older than the given number of days.
// Pass days=0 to delete all runs. Cascading deletes in the schema handle
// jobs, job_logs, and artifacts automatically.
func (s *Store) PruneRuns(olderThanDays int) (int64, error) {
	res, err := s.db.Exec(
		`DELETE FROM runs WHERE created_at <= NOW() - ($1 || ' days')::INTERVAL`,
		olderThanDays,
	)
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

// ── Cancel / Rerun ────────────────────────────────────────────────────────────

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
func (s *Store) RerunSteps(runID string) (name string, steps []api.StepDef, workspaceDir string, err error) {
	err = s.db.QueryRow(`SELECT name, workspace_dir FROM runs WHERE id=$1`, runID).
		Scan(&name, &workspaceDir)
	if err == sql.ErrNoRows {
		return "", nil, "", fmt.Errorf("run %s not found", runID)
	}
	if err != nil {
		return "", nil, "", err
	}

	rows, err := s.db.Query(`
		SELECT step_id, step_type, image, command, work_dir, env,
		       inputs, timeout_ns, depends_on, secret_names, policy_source,
		       COALESCE(pipeline_ref::text, 'null'),
		       COALESCE(artifact_uploads::text, '[]'),
		       COALESCE(artifact_downloads::text, '[]')
		FROM jobs WHERE run_id=$1 ORDER BY started_at NULLS FIRST, id`, runID)
	if err != nil {
		return "", nil, "", err
	}
	defer rows.Close()

	for rows.Next() {
		var stepID, stepType, image, workDir, policySource string
		var pipelineRefJSON, artifactUploadsJSON, artifactDownloadsJSON string
		var commandJSON, envJSON, inputsJSON, dependsJSON, secretsJSON []byte
		var timeoutNS int64

		if err := rows.Scan(
			&stepID, &stepType, &image, &commandJSON, &workDir, &envJSON,
			&inputsJSON, &timeoutNS, &dependsJSON, &secretsJSON, &policySource,
			&pipelineRefJSON, &artifactUploadsJSON, &artifactDownloadsJSON,
		); err != nil {
			continue
		}

		var command, dependsOn, secretNames []string
		var env map[string]string
		var inputs []string
		var artifactUploads []api.ArtifactUploadSpec
		var artifactDownloads []api.ArtifactDownloadSpec
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
			Command:           command,
			WorkDir:           workDir,
			Env:               env,
			Inputs:            inputs,
			Timeout:           time.Duration(timeoutNS),
			DependsOn:         dependsOn,
			SecretNames:       secretNames,
			Type:              stepType,
			PolicySource:      policySource,
			ArtifactUploads:   artifactUploads,
			ArtifactDownloads: artifactDownloads,
		})
	}
	return name, steps, workspaceDir, nil
}

// ── Step result recording (flaky test detection) ──────────────────────────────

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
