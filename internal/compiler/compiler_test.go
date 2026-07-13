package compiler

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/JBraunsmaJr/forge/internal/pipeline"
)

// writeTempPipeline writes a pipeline JSON to a temp file and returns its path.
func writeTempPipeline(t *testing.T, p jsonPipeline) string {
	t.Helper() // marks this as a helper so failure lines point to the caller

	data, err := json.Marshal(p)
	if err != nil {
		t.Fatalf("marshal test pipeline: %v", err)
	}

	dir := t.TempDir() // automatically cleaned up after test
	path := filepath.Join(dir, "pipeline.json")
	if err := os.WriteFile(path, data, 0644); err != nil {
		t.Fatalf("write test pipeline: %v", err)
	}
	return path
}

// TestCompile_ValidPipeline is the happy-path test.
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

// TestCompile_Conditions verifies that condition and always_run fields are parsed correctly.
func TestCompile_Conditions(t *testing.T) {
	path := writeTempPipeline(t, jsonPipeline{
		Name: "test-pipeline",
		Steps: []jsonStep{
			{
				ID:        "always-run",
				Image:     "alpine:latest",
				Run:       "echo always",
				AlwaysRun: true,
			},
			{
				ID:        "on-failure",
				Image:     "alpine:latest",
				Run:       "echo failure",
				Condition: "failure()",
			},
		},
	})

	p, err := Compile(path)
	if err != nil {
		t.Fatalf("expected no error, got: %v", err)
	}

	if p.Steps[0].AlwaysRun != true {
		t.Errorf("expected AlwaysRun true for step 0")
	}
	if p.Steps[1].Condition != "failure()" {
		t.Errorf("expected Condition 'failure()' for step 1, got %q", p.Steps[1].Condition)
	}
}

func TestCompile_MatrixExpansion(t *testing.T) {
	path := writeTempPipeline(t, jsonPipeline{
		Name: "matrix-pipeline",
		Steps: []jsonStep{
			{
				ID:    "build",
				Image: "golang:alpine",
				Matrix: map[string][]string{
					"os": {"linux", "windows"},
				},
				Run: "echo ${{ matrix.os }}",
			},
			{
				ID:        "release",
				Type:      "release",
				DependsOn: []string{"build"},
				Release: jsonRelease{
					Tag: "v1",
				},
			},
		},
	})

	p, err := Compile(path)
	if err != nil {
		t.Fatalf("Compile failed: %v", err)
	}

	// Should have 2 build jobs + 1 release job = 3 jobs
	if len(p.Steps) != 3 {
		t.Fatalf("expected 3 steps, got %d", len(p.Steps))
	}

	// Release job should depend on BOTH build jobs
	var releaseStep *pipeline.Step
	for _, s := range p.Steps {
		if s.ID == "release" {
			releaseStep = s
			break
		}
	}

	if releaseStep == nil {
		t.Fatal("release step not found")
	}

	if len(releaseStep.DependsOn) != 2 {
		t.Errorf("expected 2 dependencies, got %d: %v", len(releaseStep.DependsOn), releaseStep.DependsOn)
	}

	foundLinux := false
	foundWindows := false
	for _, d := range releaseStep.DependsOn {
		if d == "build-linux" {
			foundLinux = true
		}
		if d == "build-windows" {
			foundWindows = true
		}
	}
	if !foundLinux || !foundWindows {
		t.Errorf("missing expected dependencies: %v", releaseStep.DependsOn)
	}
}

// TestCompile_ErrorCases uses t.Run to create subtests - one per error scenario.
func TestCompile_ErrorCases(t *testing.T) {
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
		Steps: []jsonStep{{Image: "alpine:latest", Run: "echo hello"}},
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

// TEstCompile_RunWrappedInSh verifies that a `run` string is wrapped in sh -c.
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
