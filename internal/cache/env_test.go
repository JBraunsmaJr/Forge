package cache

import (
	"path/filepath"
	"testing"

	"github.com/JBraunsmaJr/forge/internal/pipeline"
)

// hashWith computes the task hash of a fixed build step after mutate has
// adjusted it.
func hashWith(t *testing.T, mutate func(*pipeline.Step)) string {
	t.Helper()
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "main.go"), "package main")

	step := &pipeline.Step{
		ID:      "build",
		Image:   "golang:1.26.5",
		Command: []string{"go", "build", "./..."},
		Inputs:  []string{"*.go"},
		Env:     map[string]string{"CGO_ENABLED": "0"},
	}
	mutate(step)

	hash, err := ComputeTaskHash(step, dir)
	if err != nil {
		t.Fatalf("ComputeTaskHash(): %v", err)
	}
	return hash
}

// The same step with unchanged inputs must get the same key on any agent,
// for any commit, and in any run.
func TestComputeTaskHash_SameKeyAcrossAgentsCommitsAndRuns(t *testing.T) {
	first := hashWith(t, func(s *pipeline.Step) {
		s.Env["FORGE_AGENT_ID"] = "agent-1"
		s.Env["FORGE_PROXY_AGENT_ID"] = "agent-1"
		s.Env["FORGE_BUILD_NUMBER"] = "2026-11.41"
		s.Env["FORGE_BUILD_COUNTER"] = "41"
		s.Env["FORGE_COMMIT_SHA"] = "1111111"
		s.Env["FORGE_BRANCH"] = "feature/a"
		s.Env["FORGE_REF"] = "refs/heads/feature/a"
		s.Env["FORGE_RUN_ID"] = "run-1"
	})
	second := hashWith(t, func(s *pipeline.Step) {
		s.Env["FORGE_AGENT_ID"] = "agent-2"
		s.Env["FORGE_PROXY_AGENT_ID"] = "agent-2"
		s.Env["FORGE_BUILD_NUMBER"] = "2026-11.42"
		s.Env["FORGE_BUILD_COUNTER"] = "42"
		s.Env["FORGE_COMMIT_SHA"] = "2222222"
		s.Env["FORGE_BRANCH"] = "main"
		s.Env["FORGE_REF"] = "refs/heads/main"
		s.Env["FORGE_COMMIT_TAG"] = "v1.0.0"
		s.Env["FORGE_RUN_ID"] = "run-2"
	})
	bare := hashWith(t, func(*pipeline.Step) {})

	if first != second {
		t.Errorf("keys differ although only the agent, commit, and run changed:\n  %s\n  %s", first, second)
	}
	if first != bare {
		t.Errorf("setting variables the step never mentions changed its key:\n  %s\n  %s", first, bare)
	}
}

// A step that mentions a run-scoped variable depends on it, so its key must
// change with the variable's value.
func TestComputeTaskHash_ReferencedRunScopedVarsAreHashed(t *testing.T) {
	mentions := map[string]func(*pipeline.Step, string){
		"shell variable in the command": func(s *pipeline.Step, name string) {
			s.Command = []string{"sh", "-c", "go build -ldflags \"-X main.v=$" + name + "\""}
		},
		"placeholder in the command": func(s *pipeline.Step, name string) {
			s.Command = []string{"sh", "-c", "echo ${{ env." + name + " }}"}
		},
		"entrypoint": func(s *pipeline.Step, name string) {
			s.Entrypoint = []string{"/bin/stamp", "--from-env", name}
		},
		"value of another variable": func(s *pipeline.Step, name string) {
			s.Env["VERSION_FROM"] = name
		},
		"template parameter": func(s *pipeline.Step, name string) {
			s.With = map[string]string{"version": "${{ env." + name + " }}"}
		},
	}

	for name := range runScopedVars {
		for how, mention := range mentions {
			one := hashWith(t, func(s *pipeline.Step) { mention(s, name); s.Env[name] = "one" })
			two := hashWith(t, func(s *pipeline.Step) { mention(s, name); s.Env[name] = "two" })
			if one == two {
				t.Errorf("%s mentioned via %s: the key did not change with its value", name, how)
			}
		}
	}
}

// Mentioning one run-scoped variable must not pull the others into the key.
func TestComputeTaskHash_OnlyTheMentionedVariableIsHashed(t *testing.T) {
	build := func(sha string) string {
		return hashWith(t, func(s *pipeline.Step) {
			s.Command = []string{"sh", "-c", "echo $FORGE_BUILD_NUMBER"}
			s.Env["FORGE_BUILD_NUMBER"] = "42"
			s.Env["FORGE_COMMIT_SHA"] = sha
		})
	}
	if build("1111111") != build("2222222") {
		t.Error("a step that mentions only the build number is keyed by the commit as well")
	}
}

// The agent ID is never hashed, even if a step mentions it: it cannot change
// what the step produces, and hashing it would stop agents from sharing
// cache entries.
func TestComputeTaskHash_ExecutionContextIsNeverHashed(t *testing.T) {
	for name := range executionContextVars {
		build := func(value string) string {
			return hashWith(t, func(s *pipeline.Step) {
				s.Command = []string{"sh", "-c", "echo $" + name}
				s.Env[name] = value
			})
		}
		if build("one") != build("two") {
			t.Errorf("%s changed the key", name)
		}
	}
}

// A variable's own name inside its own value, or a mention inside a
// never-hashed variable, is not a reference.
func TestRefersTo_IgnoresSelfAndExecutionContext(t *testing.T) {
	step := &pipeline.Step{Env: map[string]string{
		"FORGE_COMMIT_SHA": "FORGE_COMMIT_SHA",
		"FORGE_RUN_NAME":   "build for FORGE_COMMIT_SHA",
	}}
	if refersTo(step, "FORGE_COMMIT_SHA") {
		t.Error("refersTo() found a reference where there is none")
	}
}

// Ordinary variables are always part of the key.
func TestComputeTaskHash_OrdinaryVariablesAreHashed(t *testing.T) {
	one := hashWith(t, func(s *pipeline.Step) { s.Env["TARGET"] = "linux" })
	two := hashWith(t, func(s *pipeline.Step) { s.Env["TARGET"] = "windows" })
	if one == two {
		t.Error("changing an ordinary variable did not change the key")
	}
}
