package agent

import (
	"reflect"
	"testing"

	"github.com/JBraunsmaJr/forge/internal/api"
	"github.com/JBraunsmaJr/forge/internal/childrun"
)

func TestChildRunRequest(t *testing.T) {
	env := map[string]string{"FORGE_BRANCH": "release/1.2", "FORGE_BUILD_NUMBER": "2026-11.42"}
	target := childrun.Target{Project: "sdk", Ref: "${{ env.FORGE_BRANCH }}", FallbackRef: "main"}
	ref := &api.PipelineRef{
		Wait:          true,
		Variables:     map[string]string{"RELEASE": "${{ env.FORGE_BUILD_NUMBER }}", "MODE": "full"},
		ArtifactsSend: []string{"manifest"},
	}

	got := childRunRequest(target, ref, env)
	want := api.ChildRunRequest{
		Project:       "sdk",
		Ref:           "release/1.2",
		FallbackRef:   "main",
		Variables:     map[string]string{"RELEASE": "2026-11.42", "MODE": "full"},
		ArtifactsSend: []string{"manifest"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("childRunRequest() =\n  %+v\nwant\n  %+v", got, want)
	}
	if ref.Variables["RELEASE"] != "${{ env.FORGE_BUILD_NUMBER }}" {
		t.Error("childRunRequest modified the step's own variables")
	}

	// With no ref of its own, the request leaves the choice to the
	// scheduler, which uses the parent run's branch or tag.
	if got := childRunRequest(childrun.Target{Project: "sdk"}, &api.PipelineRef{}, env); got.Ref != "" || got.Variables != nil {
		t.Errorf("minimal request = %+v; want no ref and no variables", got)
	}
}
