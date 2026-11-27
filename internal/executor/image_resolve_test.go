package executor

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/JBraunsmaJr/forge/internal/pipeline"
)

// A step whose image cannot be resolved must fail before any container
// command runs, with the reason in the step's log.
func TestRunStep_UnresolvableImageFailsBeforeStart(t *testing.T) {
	exec, err := New(t.TempDir(), t.TempDir(), "test-agent", nil)
	if err != nil {
		t.Fatalf("New(): %v", err)
	}

	cases := map[string]map[string]string{
		"registry.example.com/team/api:test-${{ env.NOT_SET }}": nil,
		"alpine:${{ env.TAG }}":                                 {"TAG": "3.20 --privileged"},
	}
	for image, env := range cases {
		step := &pipeline.Step{ID: "test", Image: image, Command: []string{"true"}, Env: env}

		result, err := exec.RunStep(context.Background(), step)
		if err == nil {
			t.Fatalf("image %q: RunStep succeeded; want it refused", image)
		}
		if result == nil || result.Status != pipeline.StatusFailed || result.ExitCode == 0 {
			t.Fatalf("image %q: result = %+v; want a failed step", image, result)
		}
		if step.Image != image {
			t.Errorf("image %q: step image was changed to %q although it did not resolve", image, step.Image)
		}
		log, readErr := os.ReadFile(filepath.Join(exec.LogDir, "test.jsonl"))
		if readErr != nil || !strings.Contains(string(log), "image") {
			t.Errorf("image %q: the step log does not explain the failure: %q (%v)", image, log, readErr)
		}
	}
}
