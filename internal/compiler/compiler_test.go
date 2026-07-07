// Package compiler_test contains tests for the pipeline compiler.
//
// Go testing conventions vs C#:
//   - Test files end in _test.go and are in the same package
//   - Test functions are named TestXxx(t *testing.T) — no attributes needed
//   - t.Fatal() ≈ Assert.Fail() + stop the test
//   - t.Error() ≈ Assert.Fail() but keep running
//   - t.Run("name", func) ≈ [TestCase] / subtest in NUnit
//   - No test class needed — tests are just functions
package compiler

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

// writeTempPipeline writes a pipeline JSON to a temp file and returns its path.
// The test cleans up via t.Cleanup — equivalent to [TearDown] in NUnit.
func writeTempPipeline(t *testing.T, p jsonPipeline) string {
	t.Helper() // marks this as a helper so failure lines point to the caller

	data, err := json.Marshal(p)
	if err != nil {
		t.Fatalf("marshal test pipeline: %v", err)
	}

	dir := t.TempDir() // automatically cleaned up after the test
	path := filepath.Join(dir, "pipeline.json")
	if err := os.WriteFile(path, data, 0644); err != nil {
		t.Fatalf("write test pipeline: %v", err)
	}
	return path
}

// TestCompile_ValidPipeline is the happy-path test.
// In C# NUnit: [Test] public void Compile_ValidPipeline_ReturnsExpectedSteps()
func TestCompile_ValidPipeline(t *testing.T) {
	path := writeTempPipeline(t, jsonPipeline{
		Name: "test-pipeline",
		Steps: []jsonStep{
			{
				ID:    "lint",
				Image: "alpine:latest",
				Run:   "echo hello",
			},
			{
				ID:        "test",
				Image:     "alpine:latest",
				Run:       "echo world",
				DependsOn: []string{"lint"},
			},
		},
	})

	p, err := Compile(path)
	if err != nil {
		t.Fatalf("expected no error, got: %v", err)
	}

	if p.Name != "test-pipeline" {
		t.Errorf("expected name 'test-pipeline', got %q", p.Name)
	}
	if len(p.Steps) != 2 {
		t.Fatalf("expected 2 steps, got %d", len(p.Steps))
	}

	// Check that depends_on was preserved correctly.
	testStep := p.Steps[1]
	if len(testStep.DependsOn) != 1 || testStep.DependsOn[0] != "lint" {
		t.Errorf("expected test step to depend on lint, got %v", testStep.DependsOn)
	}
}

// TestCompile_ErrorCases uses t.Run to create subtests — one per error scenario.
// C# NUnit equivalent: [TestCase(...)] attributes.
func TestCompile_ErrorCases(t *testing.T) {
	// t.Run creates a subtest. You can run a specific one with:
	//   go test -run TestCompile_ErrorCases/missing_name
	t.Run("missing name", func(t *testing.T) {
		path := writeTempPipeline(t, jsonPipeline{
			Steps: []jsonStep{{ID: "x", Run: "echo"}},
		})
		_, err := Compile(path)
		if err == nil {
			t.Error("expected error for missing name, got nil")
		}
	})

	t.Run("empty steps", func(t *testing.T) {
		path := writeTempPipeline(t, jsonPipeline{Name: "p"})
		_, err := Compile(path)
		if err == nil {
			t.Error("expected error for empty steps, got nil")
		}
	})

	t.Run("step missing run and command", func(t *testing.T) {
		path := writeTempPipeline(t, jsonPipeline{
			Name:  "p",
			Steps: []jsonStep{{ID: "x", Image: "alpine"}},
		})
		_, err := Compile(path)
		if err == nil {
			t.Error("expected error for step with no run/command, got nil")
		}
	})

	t.Run("unknown dependency", func(t *testing.T) {
		path := writeTempPipeline(t, jsonPipeline{
			Name: "p",
			Steps: []jsonStep{
				{ID: "a", Run: "echo", DependsOn: []string{"does-not-exist"}},
			},
		})
		_, err := Compile(path)
		if err == nil {
			t.Error("expected error for unknown dependency, got nil")
		}
	})

	t.Run("invalid timeout", func(t *testing.T) {
		path := writeTempPipeline(t, jsonPipeline{
			Name: "p",
			Steps: []jsonStep{
				{ID: "a", Run: "echo", Timeout: "not-a-duration"},
			},
		})
		_, err := Compile(path)
		if err == nil {
			t.Error("expected error for invalid timeout, got nil")
		}
	})

	t.Run("file does not exist", func(t *testing.T) {
		_, err := Compile("/does/not/exist/pipeline.json")
		if err == nil {
			t.Error("expected error for missing file, got nil")
		}
	})
}

// TestCompile_Defaults checks that unspecified fields get sane defaults.
func TestCompile_Defaults(t *testing.T) {
	path := writeTempPipeline(t, jsonPipeline{
		Name:  "p",
		Steps: []jsonStep{{Image: "alpine:latest", Run: "echo hello"}}, // no ID, image, or timeout
	})

	p, err := Compile(path)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	step := p.Steps[0]

	if step.ID == "" {
		t.Error("expected a default ID to be generated")
	}
	if step.WorkDir != "/workspace" {
		t.Errorf("expected default workdir '/workspace', got %q", step.WorkDir)
	}
}

// TestCompile_RunWrappedInSh verifies that a 'run' string is wrapped in sh -c.
// This lets pipeline authors write shell idioms like pipes and &&.
func TestCompile_RunWrappedInSh(t *testing.T) {
	path := writeTempPipeline(t, jsonPipeline{
		Name:  "p",
		Steps: []jsonStep{{Image: "alpine:latest", Run: "echo a && echo b"}},
	})

	p, err := Compile(path)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	cmd := p.Steps[0].Command
	if len(cmd) != 3 || cmd[0] != "sh" || cmd[1] != "-c" {
		t.Errorf("expected [sh -c <script>], got %v", cmd)
	}
}
