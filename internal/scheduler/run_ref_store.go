package scheduler

// RunStatusRef returns the branch or tag a run's commit status belongs to.
//
// For a push that is the run's own ref. For a pull or merge request the
// run's ref is the request's head ref ("refs/merge-requests/7/head"), which
// is not what the Git host files statuses under: GitLab expects the source
// branch. Forge sets that branch as FORGE_BRANCH on every step of a
// webhook-triggered run, so it is read from there, and the run's ref is the
// fallback.
func (s *Store) RunStatusRef(runID string) string {
	var branch string
	err := s.db.QueryRow(`
		SELECT env->>'FORGE_BRANCH' FROM jobs
		WHERE run_id = $1 AND COALESCE(env->>'FORGE_BRANCH', '') <> ''
		LIMIT 1`, runID).Scan(&branch)
	if err == nil && branch != "" {
		return branch
	}

	var ref string
	if err := s.db.QueryRow(`SELECT COALESCE(ref, '') FROM runs WHERE id = $1`, runID).Scan(&ref); err != nil {
		return ""
	}
	return ref
}
