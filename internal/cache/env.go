package cache

import (
	"strings"

	"github.com/JBraunsmaJr/forge/internal/pipeline"
)

// executionContextVars describe where and when a step happens to run. They
// never affect what a step produces, so they are never part of its task
// hash.
var executionContextVars = map[string]bool{
	"FORGE_EVENT":          true,
	"FORGE_PR_NUMBER":      true,
	"FORGE_API_TOKEN":      true,
	"FORGE_SCHEDULER_URL":  true,
	"FORGE_RUN_ID":         true,
	"FORGE_JOB_ID":         true,
	"FORGE_RUN_NAME":       true,
	"FORGE_REPO_URL":       true,
	"FORGE_REPO_NAME":      true,
	"FORGE_AGENT_ID":       true,
	"FORGE_PROXY_AGENT_ID": true,
}

// runScopedVars identify the commit or the run. Forge sets them on every
// step, so hashing them unconditionally would give every commit and every
// run its own cache key and no step would ever be skipped because its inputs
// are unchanged. They are hashed only for a step that refers to them; see
// refersTo.
var runScopedVars = map[string]bool{
	"FORGE_BUILD_NUMBER":  true,
	"FORGE_BUILD_COUNTER": true,
	"FORGE_COMMIT_SHA":    true,
	"FORGE_COMMIT_TAG":    true,
	"FORGE_BRANCH":        true,
	"FORGE_REF":           true,
}

// hashedEnvVar reports whether the environment variable key contributes to
// step's task hash.
func hashedEnvVar(step *pipeline.Step, key string) bool {
	switch {
	case executionContextVars[key]:
		return false
	case runScopedVars[key]:
		return refersTo(step, key)
	}
	return true
}

// refersTo reports whether the step's own definition mentions the variable
// name: in its command or entrypoint, in a template parameter, or in the
// value of another environment variable. The image needs no check here: a
// placeholder in it is resolved before hashing, so the value is already part
// of the hashed image reference.
//
// A step that reads the variable without mentioning it, for example from
// inside a script file, is not detected. Such a step opts in by mentioning
// the name in its definition.
func refersTo(step *pipeline.Step, name string) bool {
	for _, arg := range step.Command {
		if strings.Contains(arg, name) {
			return true
		}
	}
	for _, arg := range step.Entrypoint {
		if strings.Contains(arg, name) {
			return true
		}
	}
	for _, value := range step.With {
		if strings.Contains(value, name) {
			return true
		}
	}
	for key, value := range step.Env {
		if key != name && !executionContextVars[key] && strings.Contains(value, name) {
			return true
		}
	}
	return false
}
