package scheduler

import (
	"strings"

	"github.com/JBraunsmaJr/forge/internal/api"
)

// evaluateCondition handler simple Forge condition expressions.
// Supported: "success()", "failure()", "always()", "tag()" or empty (default to success).
func evaluateCondition(condition string, runPassed bool, ref string) bool {
	cond := strings.TrimSpace(strings.ToLower(condition))
	if cond == "" || cond == "success()" {
		return runPassed
	}
	if cond == "always()" {
		return true
	}
	if cond == "failure()" {
		return !runPassed
	}
	if cond == "tag()" {
		return strings.HasPrefix(ref, "refs/tags/")
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
