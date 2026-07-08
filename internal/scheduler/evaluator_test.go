package scheduler

import "testing"

func TestEvaluateCondition(t *testing.T) {
	tests := []struct {
		condition string
		runPassed bool
		expected  bool
	}{
		{"", true, true},
		{"", false, false},
		{"success()", true, true},
		{"success()", false, false},
		{"failure()", true, false},
		{"failure()", false, true},
		{"always()", true, true},
		{"always()", false, true},
		{"  SUCCESS()  ", true, true},
		{"unknown()", true, false},
	}

	for _, tt := range tests {
		t.Run(tt.condition, func(t *testing.T) {
			got := evaluateCondition(tt.condition, tt.runPassed)
			if got != tt.expected {
				t.Errorf("evaluateCondition(%q, %v) = %v; want %v", tt.condition, tt.runPassed, got, tt.expected)
			}
		})
	}
}
