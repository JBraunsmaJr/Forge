package scheduler

import "testing"

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
