package scheduler

import (
	"database/sql"
	"errors"
	"fmt"
)

// maxChildRunDepth bounds how deep child runs may nest across projects. It
// stops two projects that start each other from looping forever.
const maxChildRunDepth = 5

// errChildRunParent is returned when a job cannot be the parent of a child
// run: it does not exist, is not a pipeline step, or is not running.
var errChildRunParent = errors.New("job is not a running pipeline step")

// childRunParent describes the run a cross-project child run is started
// from.
type childRunParent struct {
	RunID     string
	OrgID     string
	ProjectID string
	Ref       string
	// Depth is the number of ancestors the parent run has: 0 for a run that
	// is not itself a child run.
	Depth int
}

// ChildRunParent looks up the run that jobID belongs to, for starting a
// child run from it. The job must be a pipeline step that an agent is
// executing right now.
func (s *Store) ChildRunParent(jobID string) (childRunParent, error) {
	var parent childRunParent
	var stepType, status string
	err := s.db.QueryRow(`
		SELECT r.id, COALESCE(r.org_id, ''), COALESCE(r.project_id, ''), COALESCE(r.ref, ''),
		       j.step_type, j.status
		FROM jobs j JOIN runs r ON r.id = j.run_id
		WHERE j.id = $1`, jobID,
	).Scan(&parent.RunID, &parent.OrgID, &parent.ProjectID, &parent.Ref, &stepType, &status)
	if errors.Is(err, sql.ErrNoRows) {
		return childRunParent{}, errChildRunParent
	}
	if err != nil {
		return childRunParent{}, fmt.Errorf("looking up parent job: %w", err)
	}
	if stepType != "pipeline" || (status != "running" && status != "waiting") {
		return childRunParent{}, errChildRunParent
	}

	// Count ancestors, stopping one past the limit: the caller only needs to
	// know whether the limit is exceeded.
	runID := parent.RunID
	for parent.Depth <= maxChildRunDepth {
		var ancestor sql.NullString
		err := s.db.QueryRow(`SELECT parent_run_id FROM runs WHERE id = $1`, runID).Scan(&ancestor)
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return childRunParent{}, fmt.Errorf("looking up run ancestry: %w", err)
		}
		if !ancestor.Valid || ancestor.String == "" {
			break
		}
		runID = ancestor.String
		parent.Depth++
	}
	return parent, nil
}
