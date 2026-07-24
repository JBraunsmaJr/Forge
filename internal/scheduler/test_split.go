package scheduler

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"

	"github.com/JBraunsmaJr/forge/internal/api"
)

type ShardAssignment struct {
	ShardIndex  int
	Files       []string
	EstimatedMS int64
}

type fileInfo struct {
	path  string
	avgMS int64
	runs  int // distinct runs this file has timing data from
}

func (s *Store) expandSplitSteps(tx *sql.Tx, runID, projectID, pipelineName string, steps []api.StepDef) ([]api.StepDef, error) {
	var expanded []api.StepDef
	for _, step := range steps {
		if step.Split == nil {
			expanded = append(expanded, step)
			continue
		}

		// Compute shard assignments using historical data.
		assignments, err := s.computeShardAssignments(
			projectID,
			pipelineName,
			step.ID,
			step.Split,
		)
		if err != nil {
			return nil, err
		}

		// Store the shard plan so the UI can show it.
		if err := s.storeShardAssignments(tx, runID, step.ID, assignments); err != nil {
			return nil, err
		}

		// Fan out: one step per shard.
		//
		// coldStart: no shard has any assigned files (no usable timing
		// history yet). Historically every shard then ran the entire suite
		// concurrently — N× the work, and for docker-heavy suites N nested
		// stacks stampeding one daemon (OOM kills, flaky failures). The
		// SplitConfig.Fallback field documents "round-robin" | "single";
		// implement it: with "single" (the default), only shard 1 runs the
		// full suite on a cold start and the rest are told to no-op via
		// FORGE_TEST_SHARD_EMPTY=1. Set fallback: "round-robin" to keep the
		// old run-everything-everywhere behavior.
		coldStart := true
		for _, a := range assignments {
			if len(a.Files) > 0 {
				coldStart = false
				break
			}
		}
		if step.Split.Fallback == "round-robin" {
			coldStart = false
		}
		originalDeps := step.DependsOn
		var shardIDs []string
		for i, assignment := range assignments {
			shardID := fmt.Sprintf("%s-shard-%d", step.ID, i+1)
			shardStep := step // copy
			shardStep.ID = shardID
			shardStep.DependsOn = originalDeps
			shardStep.Split = nil // don't recurse
			shardStep.Env = copyEnv(step.Env)
			shardStep.Env["FORGE_TEST_FILES"] = strings.Join(assignment.Files, ",")
			shardStep.Env["FORGE_SHARD_INDEX"] = strconv.Itoa(i)
			shardStep.Env["FORGE_SHARD_TOTAL"] = strconv.Itoa(len(assignments))
			shardStep.Env["FORGE_SHARD_ESTIMATED_MS"] = strconv.FormatInt(assignment.EstimatedMS, 10)
			if coldStart && i > 0 {
				// See coldStart above: only shard 1 runs the full suite.
				shardStep.Env["FORGE_TEST_SHARD_EMPTY"] = "1"
			}
			expanded = append(expanded, shardStep)
			shardIDs = append(shardIDs, shardID)
		}

		// Synthesize a fan-in step so downstream deps still work.
		// Any step that depends on "test" now depends on all shards.
		// The fan-in is a no-op step that just waits.
		fanIn := api.StepDef{
			ID:        step.ID, // same ID as the original
			Image:     "alpine:latest",
			Command:   []string{"true"}, // no-op
			DependsOn: shardIDs,
			Status:    api.JobStatusPending,
		}
		expanded = append(expanded, fanIn)
	}
	return expanded, nil
}

func (s *Store) computeShardAssignments(
	projectID, pipelineName, stepID string,
	config *api.SplitConfig,
) ([]ShardAssignment, error) {

	// Apply documented defaults (see api.SplitConfig).
	historyDays := config.HistoryDays
	if historyDays <= 0 {
		historyDays = 14
	}
	minHistoryRuns := config.MinHistoryRuns
	if minHistoryRuns <= 0 {
		minHistoryRuns = 3
	}

	// Query historical file durations.
	rows, err := s.db.Query(`
		SELECT file_path, AVG(duration_ms)::BIGINT as avg_ms, COUNT(DISTINCT run_id) as runs
		FROM   test_file_durations
		WHERE  COALESCE(project_id, '') = $1
		AND    pipeline_name = $2
		AND    step_id       LIKE $3   -- matches "test" AND "test-shard-1", "test-shard-2" etc
		AND    created_at    > NOW() - ($4 || ' days')::INTERVAL
		GROUP  BY file_path
		ORDER  BY avg_ms DESC`, // longest files first (greedy assignment works better)
		projectID, pipelineName,
		stepID+"%", // step_id prefix match covers split shards
		historyDays,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var files []fileInfo
	for rows.Next() {
		var f fileInfo
		if err := rows.Scan(&f.path, &f.avgMS, &f.runs); err != nil {
			return nil, err
		}
		files = append(files, f)
	}

	if len(files) == 0 {
		// Not enough history — fall back.
		return s.roundRobinAssignment(projectID, pipelineName, stepID, config)
	}

	var known []fileInfo
	var unknown []string
	for _, f := range files {
		if f.runs < minHistoryRuns {
			unknown = append(unknown, f.path)
		} else {
			known = append(known, f)
		}
	}

	if len(known) == 0 {
		return s.roundRobinAssignment(projectID, pipelineName, stepID, config)
	}

	assignments := greedyBinPacking(known, config.Shards)
	distributeUnknownFiles(unknown, assignments)
	return assignments, nil
}

func (s *Store) roundRobinAssignment(projectID, pipelineName, stepID string, config *api.SplitConfig) ([]ShardAssignment, error) {
	// Query all known files for this step, even if they don't have much
	// history. We still pull the average duration where one exists so the
	// UI can show a rough estimate instead of 0 for fallback assignments.
	rows, err := s.db.Query(`
		SELECT file_path, COALESCE(AVG(duration_ms), 0)::BIGINT AS avg_ms
		FROM   test_file_durations
		WHERE  COALESCE(project_id, '') = $1
		AND    pipeline_name = $2
		AND    step_id       LIKE $3
		GROUP  BY file_path
		ORDER  BY file_path ASC`,
		projectID, pipelineName, stepID+"%",
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var allFiles []fileInfo
	for rows.Next() {
		var f fileInfo
		if err := rows.Scan(&f.path, &f.avgMS); err != nil {
			return nil, err
		}
		allFiles = append(allFiles, f)
	}

	shards := config.Shards
	if shards < 1 {
		shards = 1
	}
	assignments := make([]ShardAssignment, shards)
	for i := range assignments {
		assignments[i].ShardIndex = i
	}

	if len(allFiles) == 0 {
		// No history at all — return empty shards, the agent will run everything (default behavior)
		return assignments, nil
	}

	for i, f := range allFiles {
		shardIdx := i % shards
		assignments[shardIdx].Files = append(assignments[shardIdx].Files, f.path)
		assignments[shardIdx].EstimatedMS += f.avgMS
	}

	return assignments, nil
}

func greedyBinPacking(files []fileInfo, shards int) []ShardAssignment {
	assignments := make([]ShardAssignment, shards)
	for i := range assignments {
		assignments[i].ShardIndex = i
	}

	// Min-heap by current estimated total — assign each file to the
	// lightest shard at the time of assignment.
	for _, f := range files {
		// Find shard with minimum estimated time.
		minIdx := 0
		for i := 1; i < shards; i++ {
			if assignments[i].EstimatedMS < assignments[minIdx].EstimatedMS {
				minIdx = i
			}
		}
		assignments[minIdx].Files = append(assignments[minIdx].Files, f.path)
		assignments[minIdx].EstimatedMS += f.avgMS
	}

	return assignments
}

// distributeUnknownFiles assigns files with no history to shards round-robin
// by estimated remaining capacity. New files always go to the shard with
// the most headroom.
func distributeUnknownFiles(unknown []string, assignments []ShardAssignment) {
	for _, path := range unknown {
		// Assign to shard with lowest current total (greedy logic).
		minIdx := 0
		for i := 1; i < len(assignments); i++ {
			if assignments[i].EstimatedMS < assignments[minIdx].EstimatedMS {
				minIdx = i
			}
		}
		assignments[minIdx].Files = append(assignments[minIdx].Files, path)
		// Don't update EstimatedMS — we don't know the duration.
		// Use a small constant so this file doesn't cause all unknowns
		// to pile up in the same shard.
		assignments[minIdx].EstimatedMS += 30_000 // assume 30s for unknown files
	}
}

func (s *Store) storeShardAssignments(tx *sql.Tx, runID, stepID string, assignments []ShardAssignment) error {
	for _, a := range assignments {
		filePathsJSON, _ := json.Marshal(a.Files)
		_, err := tx.Exec(`
			INSERT INTO test_shard_assignments (run_id, step_id, shard_index, total_shards, file_paths, estimated_ms)
			VALUES ($1, $2, $3, $4, $5, $6)`,
			runID, stepID, a.ShardIndex, len(assignments), filePathsJSON, a.EstimatedMS,
		)
		if err != nil {
			return err
		}
	}
	return nil
}

// copyEnv returns a non-nil copy so callers can safely add keys.
// (Returning nil for a nil input would panic on the first assignment.)
func copyEnv(env map[string]string) map[string]string {
	res := make(map[string]string, len(env)+4)
	for k, v := range env {
		res[k] = v
	}
	return res
}
