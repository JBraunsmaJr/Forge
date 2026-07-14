package scheduler

import (
	"path"
	"strings"

	"github.com/JBraunsmaJr/forge/internal/api"
)

// evaluateCondition handler simple Forge condition expressions.
// Supported: "success()", "failure()", "always()", "tag()", "branch(...)" or empty (default to success).
func evaluateCondition(condition string, runPassed bool, ref string) bool {
	condition = strings.TrimSpace(condition)
	condLower := strings.ToLower(condition)

	if condLower == "" || condLower == "success()" {
		return runPassed
	}
	if condLower == "always()" {
		return true
	}
	if condLower == "failure()" {
		return !runPassed
	}
	if condLower == "tag()" {
		return strings.HasPrefix(ref, "refs/tags/")
	}

	if strings.HasPrefix(condLower, "branch(") && strings.HasSuffix(condLower, ")") {
		if !strings.HasPrefix(ref, "refs/heads/") {
			return false
		}
		branch := strings.TrimPrefix(ref, "refs/heads/")
		// Extract arguments from original condition to preserve case
		args := condition[len("branch(") : len(condition)-1]
		patterns := strings.Split(args, ",")
		for _, p := range patterns {
			p = strings.TrimSpace(p)
			matched, _ := path.Match(p, branch)
			if matched {
				return true
			}
		}
		return false
	}

	return false
}

// PruneSteps removes steps that will never run in the current session based on static conditions (like tag()).
// It also removes any steps that depend on a removed step.
func PruneSteps(steps []api.StepDef, ref string) []api.StepDef {
	removed := make(map[string]bool)

	// Phase 1: Direct removal based on static conditions.
	// We only prune if we are CERTAIN it won't run.
	for _, s := range steps {
		cond := strings.TrimSpace(strings.ToLower(s.Condition))
		if cond == "tag()" && !strings.HasPrefix(ref, "refs/tags/") {
			removed[s.ID] = true
		}
		if strings.HasPrefix(cond, "branch(") && strings.HasSuffix(cond, ")") {
			if !evaluateCondition(s.Condition, true, ref) {
				removed[s.ID] = true
			}
		}
	}

	if len(removed) == 0 {
		return steps
	}

	// Phase 2: Cascading removal.
	// If a step depends on a removed step, it must also be removed.
	for {
		changed := false
		for _, s := range steps {
			if removed[s.ID] {
				continue
			}
			for _, depID := range s.DependsOn {
				if removed[depID] {
					removed[s.ID] = true
					changed = true
					break
				}
			}
		}
		if !changed {
			break
		}
	}

	// Phase 3: Build final list.
	var pruned []api.StepDef
	for _, s := range steps {
		if !removed[s.ID] {
			pruned = append(pruned, s)
		}
	}
	return pruned
}
