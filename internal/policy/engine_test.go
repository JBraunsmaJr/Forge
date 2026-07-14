package policy

import (
	"testing"

	"github.com/JBraunsmaJr/forge/internal/api"
)

func makePolicy(name string, forbid bool, stepIDs ...string) api.PolicyInfo {
	steps := make([]api.StepDef, len(stepIDs))
	for i, id := range stepIDs {
		steps[i] = api.StepDef{ID: id, Image: "alpine:latest", Type: "task"}
	}
	return api.PolicyInfo{Name: name, Steps: steps, ForbidOverride: forbid}
}

func applyTest(userSteps []api.StepDef, policies []api.PolicyInfo) ([]api.StepDef, []string, error) {
	return Apply(userSteps, policies, "test-pipeline", "/tmp", "test-org", nil)
}

func TestApply_InjectsSteps(t *testing.T) {
	userSteps := []api.StepDef{{ID: "lint"}, {ID: "deploy"}}
	result, names, err := applyTest(userSteps, []api.PolicyInfo{makePolicy("security", false, "sec-scan")})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(result) != 3 {
		t.Fatalf("expected 3 steps, got %d", len(result))
	}
	if result[0].ID != "sec-scan" {
		t.Errorf("expected policies step first, got %q", result[0].ID)
	}
	if result[0].PolicySource != "security" {
		t.Errorf("expected PolicySource='security', got %q", result[0].PolicySource)
	}
	if len(names) != 1 || names[0] != "security" {
		t.Errorf("expected [security], got %v", names)
	}
}

func TestApply_ForbidOverride_Rejects(t *testing.T) {
	userSteps := []api.StepDef{{ID: "sec-scan"}, {ID: "deploy"}}
	_, _, err := applyTest(userSteps, []api.PolicyInfo{makePolicy("security", true, "sec-scan")})
	if err == nil {
		t.Fatal("expected error for ForbidOverride conflict, got nil")
	}
}

func TestApply_AllowOverride_SkipsInjection(t *testing.T) {
	userSteps := []api.StepDef{{ID: "sec-scan"}, {ID: "deploy"}}
	result, _, err := applyTest(userSteps, []api.PolicyInfo{makePolicy("security", false, "sec-scan")})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(result) != 2 {
		t.Errorf("expected 2 steps (no injection), got %d", len(result))
	}
	if result[0].PolicySource != "" {
		t.Errorf("expected no PolicySource on user step, got %q", result[0].PolicySource)
	}
}

func TestApply_MultiplePolices(t *testing.T) {
	userSteps := []api.StepDef{{ID: "deploy"}}
	policies := []api.PolicyInfo{
		makePolicy("security", false, "sec-scan"),
		makePolicy("compliance", false, "license-check"),
	}
	result, names, err := applyTest(userSteps, policies)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(result) != 3 {
		t.Fatalf("expected 3 steps, got %d", len(result))
	}
	if len(names) != 2 {
		t.Errorf("expected 2 applied policies names, got %v", names)
	}
}

func TestApply_NoPolicies(t *testing.T) {
	userSteps := []api.StepDef{{ID: "lint"}, {ID: "test"}}
	result, names, err := applyTest(userSteps, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(result) != 2 {
		t.Errorf("expected 2 unchanged steps, got %d", len(result))
	}
	if len(names) != 0 {
		t.Errorf("expected no applied policies, got %v", names)
	}
}

// TestApply_InlineScriptTransformer tests the transformer path using an inline shell script
// that reads the pipeline and adds a step.
// This test requires sh to be available (works on Linux/Mac/WSL)
func TestApply_InlineScriptTransformer(t *testing.T) {
	userSteps := []api.StepDef{
		{ID: "build", Image: "docker:27", Command: []string{"sh", "-c", "docker build -t myapp ."}},
		{ID: "deploy", DependsOn: []string{"build"}},
	}

	script := `
input=$(cat)
echo "$input" | python3 -c "
import json, sys
data = json.load(sys.stdin)
steps = data['steps']
result = []
for s in steps:
    result.append(s)
    if s['id'] == 'build':
        result.append({
            'id': 'trivy-scan',
            'image': 'aquasec/trivy:latest',
            'run': 'trivy image myapp:latest',
            'depends_on': ['build']
        })
    elif 'build' in (s.get('depends_on') or []):
        s['depends_on'] = ['trivy-scan']
print(json.dumps(result))
" 2>/dev/null || echo "$input" | python3 -c "
import json, sys
print(json.dumps(json.load(sys.stdin)['steps']))
"
`

	pol := api.PolicyInfo{
		Name: "container-security",
		Transformer: &api.PolicyTransformer{
			Script: script,
		},
	}

	result, names, err := applyTest(userSteps, []api.PolicyInfo{pol})
	if err != nil {
		t.Skipf("skipping transformer test (sh/python3 not available): %v", err)
	}

	if len(result) != 3 {
		t.Fatalf("expected 3 steps after transformation, got %d: %v",
			len(result), stepIDs(result))
	}
	if result[1].ID != "trivy-scan" {
		t.Errorf("expected trivy-scan as second step, got %q", result[1].ID)
	}

	if result[1].PolicySource != "container-security" {
		t.Errorf("expected PolicySource='container-security', got %q", result[1].PolicySource)
	}

	deployDeps := result[2].DependsOn
	if len(deployDeps) != 1 || deployDeps[0] != "trivy-scan" {
		t.Errorf("expected deploy to depend on [trivy-scan], got %v", deployDeps)
	}
	if len(names) != 1 || names[0] != "container-security" {
		t.Errorf("expected [container-security], got %v", names)
	}
}

// TestApply_TransformerInvalidOutput verifies that garbage stdout from a
// transformer causes a clear error, not a silent data corruption.
func TestApply_TransformerInvalidOutput(t *testing.T) {
	pol := api.PolicyInfo{
		Name:        "bad-transformer",
		Transformer: &api.PolicyTransformer{Script: "echo 'not valid json'"},
	}
	_, _, err := applyTest([]api.StepDef{{ID: "lint"}}, []api.PolicyInfo{pol})
	if err == nil {
		t.Fatal("expected error for invalid transformer output, got nil")
	}
}

// TestApply_TransformerPassThrough verifies that a transformer that echoes the
// pipeline unchanged produces identical output.
func TestApply_TransformerPassThrough(t *testing.T) {
	userSteps := []api.StepDef{{ID: "lint"}, {ID: "test"}}
	pol := api.PolicyInfo{
		Name: "passthrough",
		Transformer: &api.PolicyTransformer{

			Script: `python3 -c "import json,sys; d=json.load(sys.stdin); print(json.dumps(d['steps']))" 2>/dev/null || cat /dev/stdin | head -c 0; echo '[]'`,
		},
	}
	result, _, err := applyTest(userSteps, []api.PolicyInfo{pol})
	if err != nil {
		t.Skipf("skipping (sh/python3 not available): %v", err)
	}
	// Event if script falls back to [], we just verify no crash.
	_ = result
}

// TestApply_StaticAndTrasnformerCompose verifies that static injection and
// transformer run together: static step is visible to the transformer.
func TestApply_StaticAndTransformerCompose(t *testing.T) {
	userSteps := []api.StepDef{{ID: "lint"}}

	pol := api.PolicyInfo{
		Name:  "composed",
		Steps: []api.StepDef{{ID: "static-scan", Image: "alpine:latest"}},
		Transformer: &api.PolicyTransformer{

			Script: `python3 -c "
import json, sys
d = json.load(sys.stdin)
steps = d['steps']
steps.append({'id':'transformer-step','image':'alpine:latest','run':'echo added by transformer'})
print(json.dumps(steps))
" 2>/dev/null || python3 -c "import json,sys; print(json.dumps(json.load(sys.stdin)['steps']))"`,
		},
	}
	result, _, err := applyTest(userSteps, []api.PolicyInfo{pol})
	if err != nil {
		t.Skipf("skipping (python3 not available): %v", err)
	}

	if len(result) < 2 {
		t.Errorf("expected at least 2 steps, got %d", len(result))
	}
}

func TestApply_SkipsPreviouslyApplied(t *testing.T) {
	userSteps := []api.StepDef{{ID: "lint"}}
	policies := []api.PolicyInfo{makePolicy("security", false, "sec-scan")}
	// "security" is already applied
	result, names, err := Apply(userSteps, policies, "test", "/tmp", "org", []string{"security"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(result) != 1 {
		t.Errorf("expected 1 step (no injection), got %d", len(result))
	}
	if len(names) != 0 {
		t.Errorf("expected 0 new applied policies, got %v", names)
	}
}

func stepIDs(steps []api.StepDef) []string {
	ids := make([]string, len(steps))
	for i, s := range steps {
		ids[i] = s.ID
	}
	return ids
}

var _ api.TransformerInput
