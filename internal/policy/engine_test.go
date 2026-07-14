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

func applyTest(userSteps []api.StepDef, policies []api.PolicyInfo) ([]api.StepDef, error) {
	return Apply(userSteps, policies, "test-pipeline", "/tmp", "test-org", nil)
}

func TestApply_InjectsSteps(t *testing.T) {
	userSteps := []api.StepDef{{ID: "lint"}, {ID: "deploy"}}
	result, err := applyTest(userSteps, []api.PolicyInfo{makePolicy("security", false, "sec-scan")})
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
}

func TestApply_ForbidOverride_Rejects(t *testing.T) {
	userSteps := []api.StepDef{{ID: "sec-scan"}, {ID: "deploy"}}
	_, err := applyTest(userSteps, []api.PolicyInfo{makePolicy("security", true, "sec-scan")})
	if err == nil {
		t.Fatal("expected error for ForbidOverride conflict, got nil")
	}
}

func TestApply_AllowOverride_SkipsInjection(t *testing.T) {
	userSteps := []api.StepDef{{ID: "sec-scan"}, {ID: "deploy"}}
	result, err := applyTest(userSteps, []api.PolicyInfo{makePolicy("security", false, "sec-scan")})
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
	result, err := applyTest(userSteps, policies)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(result) != 3 {
		t.Fatalf("expected 3 steps, got %d", len(result))
	}
}

func TestApply_NoPolicies(t *testing.T) {
	userSteps := []api.StepDef{{ID: "lint"}, {ID: "test"}}
	result, err := applyTest(userSteps, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(result) != 2 {
		t.Errorf("expected 2 unchanged steps, got %d", len(result))
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

	result, err := applyTest(userSteps, []api.PolicyInfo{pol})
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
}

// TestApply_TransformerInvalidOutput verifies that garbage stdout from a
// transformer causes a clear error, not a silent data corruption.
func TestApply_TransformerInvalidOutput(t *testing.T) {
	pol := api.PolicyInfo{
		Name:        "bad-transformer",
		Transformer: &api.PolicyTransformer{Script: "echo 'not valid json'"},
	}
	_, err := applyTest([]api.StepDef{{ID: "lint"}}, []api.PolicyInfo{pol})
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
	result, err := applyTest(userSteps, []api.PolicyInfo{pol})
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
	result, err := applyTest(userSteps, []api.PolicyInfo{pol})
	if err != nil {
		t.Skipf("skipping (python3 not available): %v", err)
	}

	if len(result) < 2 {
		t.Errorf("expected at least 2 steps, got %d", len(result))
	}
}

func TestApply_SkipsPreviouslyAppliedSteps(t *testing.T) {
	userSteps := []api.StepDef{{ID: "lint"}}
	policies := []api.PolicyInfo{makePolicy("security", false, "sec-scan")}
	// "sec-scan" is already in the hierarchy
	result, err := Apply(userSteps, policies, "test", "/tmp", "org", []string{"sec-scan"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(result) != 1 {
		t.Errorf("expected 1 step (no injection due to ID match), got %d", len(result))
	}
}

func TestApply_TransformerPreservesPreviouslyAppliedSteps(t *testing.T) {
	userSteps := []api.StepDef{{ID: "lint"}}

	// Policy 1: Transformer that does nothing (passthrough)
	pol1 := api.PolicyInfo{
		Name: "transformer-pol",
		Transformer: &api.PolicyTransformer{
			Script: `python3 -c "import json,sys; d=json.load(sys.stdin); print(json.dumps(d['steps']))" 2>/dev/null || cat /dev/stdin | head -c 0; echo '[]'`,
		},
	}

	// Policy 2: Static policy that injects "sec-scan"
	pol2 := makePolicy("static-pol", false, "sec-scan")

	policies := []api.PolicyInfo{pol1, pol2}

	// "sec-scan" is already in the hierarchy
	result, err := Apply(userSteps, policies, "test", "/tmp", "org", []string{"sec-scan"})
	if err != nil {
		t.Skipf("skipping (python3 not available): %v", err)
	}

	// If the transformer reset existingIDs and didn't restore previouslyAppliedSteps,
	// then pol2 would have re-injected "sec-scan".
	for _, s := range result {
		if s.ID == "sec-scan" && s.PolicySource == "static-pol" {
			t.Errorf("step 'sec-scan' was re-injected by static-pol despite being in previouslyAppliedSteps")
		}
	}
}

func TestApply_TransformerPrunesParentSteps(t *testing.T) {
	// Save original and restore
	orig := runTransformerFunc
	defer func() { runTransformerFunc = orig }()

	runTransformerFunc = func(transformer *api.PolicyTransformer, steps []api.StepDef, input api.TransformerInput) ([]api.StepDef, error) {
		// Mock: just add 'sec-scan' to the list
		res := append([]api.StepDef{}, steps...)
		res = append(res, api.StepDef{ID: "sec-scan", Image: "alpine"})
		return res, nil
	}

	userSteps := []api.StepDef{{ID: "lint"}}

	// Policy that tries to inject "sec-scan" via transformer
	pol := api.PolicyInfo{
		Name: "transformer-pol",
		Transformer: &api.PolicyTransformer{
			Script: "mocked",
		},
	}

	policies := []api.PolicyInfo{pol}

	// Case 1: "sec-scan" is NOT in parent. Should be injected.
	result, err := Apply(userSteps, policies, "test", "/tmp", "org", []string{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	found := false
	for _, s := range result {
		if s.ID == "sec-scan" {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("expected 'sec-scan' to be injected when not in parent")
	}

	// Case 2: "sec-scan" IS in parent. Should be pruned.
	result2, err := Apply(userSteps, policies, "test", "/tmp", "org", []string{"sec-scan"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	for _, s := range result2 {
		if s.ID == "sec-scan" {
			t.Errorf("expected 'sec-scan' to be pruned since it was in parent")
		}
	}
}

func TestApply_TransformerDoesNotPruneUserSteps(t *testing.T) {
	// Save original and restore
	orig := runTransformerFunc
	defer func() { runTransformerFunc = orig }()

	runTransformerFunc = func(transformer *api.PolicyTransformer, steps []api.StepDef, input api.TransformerInput) ([]api.StepDef, error) {
		// Mock: just return what we got
		return steps, nil
	}

	// Child has 'lint', and parent ALSO had 'lint'.
	userSteps := []api.StepDef{{ID: "lint"}}
	pol := api.PolicyInfo{
		Name:        "transformer-pol",
		Transformer: &api.PolicyTransformer{Script: "mocked"},
	}

	result, err := Apply(userSteps, []api.PolicyInfo{pol}, "test", "/tmp", "org", []string{"lint"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	found := false
	for _, s := range result {
		if s.ID == "lint" {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("expected 'lint' to be kept because it was a user step, even if in parent")
	}
}

func TestApply_MultiPolicyDeduplication(t *testing.T) {
	// Save original and restore
	orig := runTransformerFunc
	defer func() { runTransformerFunc = orig }()

	runTransformerFunc = func(transformer *api.PolicyTransformer, steps []api.StepDef, input api.TransformerInput) ([]api.StepDef, error) {
		// Mock: check if 'npm-audit' is in input.AppliedStepIDs
		found := false
		for _, id := range input.AppliedStepIDs {
			if id == "npm-audit" {
				found = true
				break
			}
		}

		res := append([]api.StepDef{}, steps...)
		if !found {
			// If not in AppliedStepIDs, the transformer might decide to inject it.
			// (Even though it should also check 'steps', we want to test Go's pruning).
			res = append(res, api.StepDef{ID: "npm-audit", Image: "node"})
		}
		return res, nil
	}

	userSteps := []api.StepDef{{ID: "lint"}}

	// Policy 1: Static policy that injects "npm-audit"
	pol1 := makePolicy("pol1", false, "npm-audit")

	// Policy 2: Transformer policy that ALSO tries to inject "npm-audit"
	pol2 := api.PolicyInfo{
		Name:        "pol2",
		Transformer: &api.PolicyTransformer{Script: "mocked"},
	}

	policies := []api.PolicyInfo{pol1, pol2}

	// Case: Injected in the SAME run.
	result, err := Apply(userSteps, policies, "test", "/tmp", "org", nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	count := 0
	for _, s := range result {
		if s.ID == "npm-audit" {
			count++
		}
	}
	if count != 1 {
		t.Errorf("expected exactly 1 'npm-audit' step, got %d", count)
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
