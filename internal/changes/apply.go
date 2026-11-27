package changes

import (
	"fmt"
	"strings"

	"github.com/JBraunsmaJr/forge/internal/api"
)

// SkippedKey is the key, in a step's parameter map, under which a step
// skipped by Apply carries the reason. The map is stored with the job, so
// the marker is what lets a rerun tell a step skipped by its changes:
// patterns from a step skipped because something upstream failed.
const SkippedKey = "_forge.skipped_by_changes"

// Apply marks every step whose `changes:` patterns match none of the changed
// files as skipped, and returns the adjusted steps with the IDs of the steps
// it skipped. The input is not modified.
//
// A step skipped this way counts as satisfied, not as failed: steps that
// depend on it still run. To make that hold with a scheduler that skips the
// dependents of any skipped step, each dependency on a skipped step is
// replaced by that step's own dependencies, so the dependent still waits for
// everything that was upstream of the skipped step.
func Apply(steps []api.StepDef, set Set) ([]api.StepDef, []string) {
	if !set.Known() {
		return steps, nil
	}

	skipped := make(map[string]bool)
	for _, step := range steps {
		if len(step.Changes) > 0 && !set.Touches(step.Changes) {
			skipped[step.ID] = true
		}
	}
	if len(skipped) == 0 {
		return steps, nil
	}

	dependencies := make(map[string][]string, len(steps))
	for _, step := range steps {
		dependencies[step.ID] = step.DependsOn
	}

	out := make([]api.StepDef, len(steps))
	var skippedIDs []string
	for i, step := range steps {
		if skipped[step.ID] {
			reason := fmt.Sprintf("◯ step skipped: no changed file matches changes: [%s]",
				strings.Join(step.Changes, ", "))
			step.Status = api.JobStatusSkipped
			step.SkipReason = reason

			// Copy the map before marking it; it may be shared.
			with := make(map[string]string, len(step.With)+1)
			for k, v := range step.With {
				with[k] = v
			}
			with[SkippedKey] = reason
			step.With = with
			skippedIDs = append(skippedIDs, step.ID)
		} else {
			step.DependsOn = liveDependencies(step.DependsOn, dependencies, skipped)
		}
		out[i] = step
	}
	return out, skippedIDs
}

// liveDependencies returns deps with every skipped step replaced by the
// nearest steps upstream of it that are not skipped, without duplicates and
// in a stable order.
func liveDependencies(deps []string, dependencies map[string][]string, skipped map[string]bool) []string {
	anySkipped := false
	for _, id := range deps {
		anySkipped = anySkipped || skipped[id]
	}
	if !anySkipped {
		return deps
	}

	live := make([]string, 0, len(deps))
	seen := make(map[string]bool)

	var visit func(id string)
	visit = func(id string) {
		if seen[id] {
			return
		}
		seen[id] = true
		if !skipped[id] {
			live = append(live, id)
			return
		}
		for _, upstream := range dependencies[id] {
			visit(upstream)
		}
	}
	for _, id := range deps {
		visit(id)
	}
	return live
}

// PreserveSkips restores the skipped state of every step that Apply skipped
// in an earlier run, in place. A rerun calls it after clearing statuses: the
// rerun is for the same commits, so the same steps are skipped, and the
// dependencies that were rewired around them stay valid.
func PreserveSkips(steps []api.StepDef) {
	for i := range steps {
		if reason := steps[i].With[SkippedKey]; reason != "" {
			steps[i].Status = api.JobStatusSkipped
			steps[i].SkipReason = reason
		}
	}
}
