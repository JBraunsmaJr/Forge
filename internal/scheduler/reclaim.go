package scheduler

import (
	"errors"
	"fmt"
	"time"
)

// maxJobAttempts is how many agents a job is handed to before it is failed.
// One lost agent is bad luck. A job that outlives three agents is far more
// likely to be the cause, for example by exhausting the host's memory, and
// handing it out again would only take down more of the fleet.
const maxJobAttempts = 3

// startupGrace is how long after a start the scheduler leaves stale-looking
// jobs alone. It must be at least staleJobAfter: an agent that reconnects
// right away needs one heartbeat to prove its job is alive.
const startupGrace = staleJobAfter + 30*time.Second

// errLeaseLost is returned when an agent reports on a job it no longer
// holds: the job finished, was canceled, or was taken back and handed to
// another agent.
var errLeaseLost = errors.New("lease mismatch or job not running")

// reclaimAllowed reports whether enough time has passed since the scheduler
// started for a missing heartbeat to mean a lost agent.
func reclaimAllowed(started, now time.Time) bool {
	return now.Sub(started) >= startupGrace
}

// reclaimedJob is a job taken back from an agent that stopped responding.
type reclaimedJob struct {
	ID       string
	RunID    string
	StepID   string
	AgentID  string
	Attempts int // agents the job has been handed to so far
	Failed   bool
}

// ReclaimStaleJobs takes back running and waiting jobs whose agent has not
// sent a heartbeat for staleJobAfter. A job goes back to the queue for
// another agent, unless it has already been through maxJobAttempts agents:
// then it fails, and the steps that depend on it are resolved as for any
// failed step. Either way the reason is added to the job's log.
//
// It returns the number of jobs taken back. Called by the heartbeat monitor.
func (s *Store) ReclaimStaleJobs() int {
	tx, err := s.db.Begin()
	if err != nil {
		return 0
	}
	defer tx.Rollback()

	rows, err := tx.Query(`
		WITH stale AS (
			SELECT id, agent_id FROM jobs
			WHERE  (status = 'running' OR status = 'waiting')
			AND    heartbeat_at < NOW() - make_interval(secs => $1)
			FOR UPDATE SKIP LOCKED
		)
		UPDATE jobs j
		SET    reclaim_count = j.reclaim_count + 1,
		       status = CASE WHEN j.reclaim_count + 1 >= $2 THEN 'failed'
		                     WHEN j.step_type = 'release' THEN 'release'
		                     ELSE 'queued' END,
		       exit_code   = CASE WHEN j.reclaim_count + 1 >= $2 THEN 1 ELSE j.exit_code END,
		       finished_at = CASE WHEN j.reclaim_count + 1 >= $2 THEN NOW() ELSE j.finished_at END,
		       lease_id = '', agent_id = ''
		FROM   stale
		WHERE  j.id = stale.id
		RETURNING j.id, j.run_id, j.step_id, stale.agent_id, j.reclaim_count, j.status = 'failed'`,
		staleJobAfter.Seconds(), maxJobAttempts)
	if err != nil {
		fmt.Printf("[scheduler] reclaiming stale jobs failed: %v\n", err)
		return 0
	}

	var jobs []reclaimedJob
	for rows.Next() {
		var job reclaimedJob
		if err := rows.Scan(&job.ID, &job.RunID, &job.StepID, &job.AgentID, &job.Attempts, &job.Failed); err != nil {
			rows.Close()
			fmt.Printf("[scheduler] reclaiming stale jobs failed: %v\n", err)
			return 0
		}
		jobs = append(jobs, job)
	}
	if err := rows.Close(); err != nil {
		return 0
	}
	if len(jobs) == 0 {
		return 0
	}

	failedRuns := make(map[string]bool)
	for _, job := range jobs {
		level, message := reclaimNotice(job)
		if _, err := tx.Exec(`INSERT INTO job_logs (job_id, ts, level, message) VALUES ($1, NOW(), $2, $3)`,
			job.ID, level, message); err != nil {
			fmt.Printf("[scheduler] reclaiming stale jobs failed: %v\n", err)
			return 0
		}
		if job.Failed {
			failedRuns[job.RunID] = true
		}
	}

	// A job that failed here never reaches Complete, so resolve the steps
	// waiting on it now: they are skipped, or run if they always run.
	for runID := range failedRuns {
		if err := s.unlockDownstream(tx, runID); err != nil {
			fmt.Printf("[scheduler] reclaiming stale jobs failed: %v\n", err)
			return 0
		}
	}

	if err := tx.Commit(); err != nil {
		fmt.Printf("[scheduler] reclaiming stale jobs failed: %v\n", err)
		return 0
	}

	for _, job := range jobs {
		outcome := "returned to the queue"
		if job.Failed {
			outcome = "failed"
		}
		fmt.Printf("[scheduler] agent %s stopped responding: step %s of run %s %s (attempt %d of %d)\n",
			shortID(job.AgentID), job.StepID, shortID(job.RunID), outcome, job.Attempts, maxJobAttempts)
	}
	return len(jobs)
}

// reclaimNotice is the line added to a reclaimed job's log.
func reclaimNotice(job reclaimedJob) (level, message string) {
	agent := shortID(job.AgentID)
	if job.Failed {
		return "ERROR", fmt.Sprintf(
			"✗ agent %s stopped responding while running this step. That was attempt %d of %d, so the step failed. "+
				"A step that keeps losing its agent may be the cause, for example by using more memory than the host has.",
			agent, job.Attempts, maxJobAttempts)
	}
	return "WARN", fmt.Sprintf(
		"⚠ agent %s stopped responding while running this step. The step was returned to the queue for another agent (attempt %d of %d is next).",
		agent, job.Attempts+1, maxJobAttempts)
}
