package scheduler

import (
	"strings"
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
