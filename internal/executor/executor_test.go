package executor

import (
	"reflect"
	"testing"

	"github.com/JBraunsmaJr/forge/internal/pipeline"
)

func TestBuildDockerArgs_PathInjection(t *testing.T) {
	e := &Executor{
		AgentID: "test-agent",
	}

	tests := []struct {
		name     string
		step     *pipeline.Step
		expected []string
	}{
		{
			name: "run command (sh -c) without Env PATH",
			step: &pipeline.Step{
				ID:      "step1",
				Image:   "alpine",
				Command: []string{"sh", "-c", "echo hello"},
			},
			expected: []string{
				"--label", "forge.managed=true",
				"--label", "forge.agent_id=test-agent",
				"--label", "forge.run_id=",
				"--label", "forge.job_id=",
				"--workdir", "/workspace",
				"--memory", "2g",
				"--stop-timeout", "10",
				"--volume", ":/workspace:rw", // workspaceDir is empty in this test
				"-e", "FORGE_AGENT_ID=test-agent",
				"-e", "FORGE_PROXY_AGENT_ID=test-agent",
				"alpine",
				"sh", "-c", "export PATH=/workspace/.forge/bin:$PATH; echo hello",
			},
		},
		{
			name: "run command (sh -c) with Env PATH",
			step: &pipeline.Step{
				ID:      "step2",
				Image:   "alpine",
				Command: []string{"sh", "-c", "echo hello"},
				Env:     map[string]string{"PATH": "/usr/bin"},
			},
			expected: []string{
				"--label", "forge.managed=true",
				"--label", "forge.agent_id=test-agent",
				"--label", "forge.run_id=",
				"--label", "forge.job_id=",
				"--workdir", "/workspace",
				"--memory", "2g",
				"--stop-timeout", "10",
				"--volume", ":/workspace:rw",
				"-e", "PATH=/workspace/.forge/bin:/usr/bin",
				"-e", "FORGE_AGENT_ID=test-agent",
				"-e", "FORGE_PROXY_AGENT_ID=test-agent",
				"alpine",
				"sh", "-c", "export PATH=/workspace/.forge/bin:$PATH; echo hello",
			},
		},
		{
			name: "direct command without Env PATH",
			step: &pipeline.Step{
				ID:      "step3",
				Image:   "alpine",
				Command: []string{"go", "version"},
			},
			expected: []string{
				"--label", "forge.managed=true",
				"--label", "forge.agent_id=test-agent",
				"--label", "forge.run_id=",
				"--label", "forge.job_id=",
				"--workdir", "/workspace",
				"--memory", "2g",
				"--stop-timeout", "10",
				"--volume", ":/workspace:rw",
				"-e", "FORGE_AGENT_ID=test-agent",
				"-e", "FORGE_PROXY_AGENT_ID=test-agent",
				"alpine",
				"go", "version",
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := e.buildDockerArgs(tt.step, "", false)
			// Filter out labels and volumes that might vary if needed, but here we expect exact match
			if !reflect.DeepEqual(got, tt.expected) {
				t.Errorf("buildDockerArgs() = %v, want %v", got, tt.expected)
			}
		})
	}
}
