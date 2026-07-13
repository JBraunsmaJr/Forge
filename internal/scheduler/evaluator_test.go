package scheduler

import (
	"testing"

	"github.com/JBraunsmaJr/forge/internal/api"
)

func TestEvaluateCondition(t *testing.T) {
	tests := []struct {
		condition string
		runPassed bool
		ref       string
		expected  bool
	}{
		{"", true, "refs/heads/main", true},
		{"", false, "refs/heads/main", false},
		{"success()", true, "refs/heads/main", true},
		{"success()", false, "refs/heads/main", false},
		{"failure()", true, "refs/heads/main", false},
		{"failure()", false, "refs/heads/main", true},
		{"always()", true, "refs/heads/main", true},
		{"always()", false, "refs/heads/main", true},
		{"  SUCCESS()  ", true, "refs/heads/main", true},
		{"unknown()", true, "refs/heads/main", false},
		// Tag tests
		{"tag()", true, "refs/tags/v1.0.0", true},
		{"tag()", false, "refs/tags/v1.0.0", true},
		{"tag()", true, "refs/heads/main", false},
		{"TAG()", true, "refs/tags/v1.2.3", true},
	}

	for _, tt := range tests {
		t.Run(tt.condition, func(t *testing.T) {
			got := evaluateCondition(tt.condition, tt.runPassed, tt.ref)
			if got != tt.expected {
				t.Errorf("evaluateCondition(%q, %v, %q) = %v; want %v", tt.condition, tt.runPassed, tt.ref, got, tt.expected)
			}
		})
	}
}

func TestPruneSteps(t *testing.T) {
	steps := []api.StepDef{
		{ID: "A", Condition: ""},
		{ID: "B", Condition: "tag()", DependsOn: []string{"A"}},
		{ID: "C", Condition: "", DependsOn: []string{"B"}},
		{ID: "D", Condition: "", DependsOn: []string{"A"}},
	}

	t.Run("Prune tag() when not a tag", func(t *testing.T) {
		pruned := PruneSteps(steps, "refs/heads/main")
		// B should be removed because of tag()
		// C should be removed because it depends on B
		// A and D should remain
		if len(pruned) != 2 {
			t.Errorf("expected 2 steps, got %d", len(pruned))
		}
		ids := make(map[string]bool)
		for _, s := range pruned {
			ids[s.ID] = true
		}
		if !ids["A"] || !ids["D"] {
			t.Errorf("expected A and D, got %v", ids)
		}
	})

	t.Run("Keep tag() when it is a tag", func(t *testing.T) {
		pruned := PruneSteps(steps, "refs/tags/v1.0.0")
		if len(pruned) != 4 {
			t.Errorf("expected 4 steps, got %d", len(pruned))
		}
	})

	t.Run("Deep cascading removal", func(t *testing.T) {
		complexSteps := []api.StepDef{
			{ID: "1", Condition: "tag()"},
			{ID: "2", DependsOn: []string{"1"}},
			{ID: "3", DependsOn: []string{"2"}},
			{ID: "4", DependsOn: []string{"3"}},
			{ID: "5", Condition: ""},
		}
		pruned := PruneSteps(complexSteps, "refs/heads/main")
		if len(pruned) != 1 || pruned[0].ID != "5" {
			t.Errorf("expected only step 5, got %v", pruned)
		}
	})
}
