package changes

import (
	"reflect"
	"strings"
	"testing"

	"github.com/JBraunsmaJr/forge/internal/api"
)

func TestMatch(t *testing.T) {
	cases := []struct {
		pattern, file string
		want          bool
	}{
		{"go.mod", "go.mod", true},
		{"go.mod", "tools/go.mod", false},
		{"*.md", "README.md", true},
		{"*.md", "docs/guide.md", false},
		{"**/*.md", "README.md", true},
		{"**/*.md", "docs/guide.md", true},
		{"**/*.md", "docs/a/b/guide.md", true},
		{"**/*.md", "docs/guide.txt", false},
		{"services/api/**", "services/api/main.go", true},
		{"services/api/**", "services/api/internal/db/db.go", true},
		{"services/api/**", "services/api", true},
		{"services/api/**", "services/apiserver/main.go", false},
		{"services/api/**", "services/web/main.go", false},
		{"services/api/", "services/api/main.go", true},
		{"services/api/", "services/web/main.go", false},
		{"services/*/Dockerfile", "services/api/Dockerfile", true},
		{"services/*/Dockerfile", "services/api/docker/Dockerfile", false},
		{"services/**/Dockerfile", "services/api/docker/Dockerfile", true},
		{"services/**/Dockerfile", "services/Dockerfile", true},
		{"**", "anything/at/all.txt", true},
		{"**/**/x", "x", true},
		{"src/**/test_*.py", "src/pkg/sub/test_api.py", true},
		{"src/**/test_*.py", "src/pkg/sub/api_test.py", false},
		{"file[0-9].txt", "file7.txt", true},
		{"file[0-9].txt", "fileA.txt", false},
		{"docs", "docs/guide.md", false},
		{"[", "[", false}, // malformed patterns never match
	}
	for _, tc := range cases {
		if got := Match(tc.pattern, tc.file); got != tc.want {
			t.Errorf("Match(%q, %q) = %v; want %v", tc.pattern, tc.file, got, tc.want)
		}
	}
}

func TestValidatePattern(t *testing.T) {
	for _, pattern := range []string{"go.mod", "*.md", "**/*.md", "services/api/**", "services/api/", "a/**/b", "file[0-9].txt", "**"} {
		if err := ValidatePattern(pattern); err != nil {
			t.Errorf("ValidatePattern(%q) = %v; want valid", pattern, err)
		}
	}
	for _, pattern := range []string{"", "  ", " src/**", "/src/**", "../other/**", "src/../x", "./src", "src//x", `src\x`, "**.go", "src/**x/y", "file[0-9.txt"} {
		if err := ValidatePattern(pattern); err == nil {
			t.Errorf("ValidatePattern(%q) accepted an invalid pattern", pattern)
		}
	}
	if err := ValidatePatterns([]string{"ok/**", "/bad"}); err == nil || !strings.Contains(err.Error(), "/bad") {
		t.Errorf("ValidatePatterns() = %v; want the bad pattern named", err)
	}
}

func TestSetTouches(t *testing.T) {
	set := Of([]string{"services/api/main.go", "README.md"})
	if !set.Touches([]string{"docs/**", "services/api/**"}) {
		t.Error("a set with a matching file does not touch the patterns")
	}
	if set.Touches([]string{"services/web/**"}) {
		t.Error("a set with no matching file touches the patterns")
	}
	if Of(nil).Touches([]string{"**"}) {
		t.Error("an empty known set touches something")
	}
	if !Unknown().Touches([]string{"services/web/**"}) {
		t.Error("an unknown set must touch everything, so the step runs")
	}
	if (Set{}).Known() {
		t.Error("the zero Set must be unknown")
	}
}

// step is shorthand for a step with dependencies and change patterns.
func step(id string, deps []string, patterns ...string) api.StepDef {
	return api.StepDef{ID: id, DependsOn: deps, Changes: patterns}
}

func dependenciesOf(steps []api.StepDef) map[string][]string {
	out := make(map[string][]string)
	for _, s := range steps {
		if s.Status != api.JobStatusSkipped {
			out[s.ID] = s.DependsOn
		}
	}
	return out
}

func TestApply_SkipsAndKeepsDependentsRunning(t *testing.T) {
	steps := []api.StepDef{
		step("checkout", nil),
		step("build-api", []string{"checkout"}, "services/api/**"),
		step("build-web", []string{"checkout"}, "services/web/**"),
		step("test-api", []string{"build-api"}, "services/api/**"),
		step("release", []string{"test-api", "build-web"}),
	}

	got, skipped := Apply(steps, Of([]string{"services/web/index.ts"}))

	if want := []string{"build-api", "test-api"}; !reflect.DeepEqual(skipped, want) {
		t.Errorf("skipped = %v; want %v", skipped, want)
	}
	for _, s := range got {
		wantSkipped := s.ID == "build-api" || s.ID == "test-api"
		if (s.Status == api.JobStatusSkipped) != wantSkipped {
			t.Errorf("step %s: status %q; skipped should be %v", s.ID, s.Status, wantSkipped)
		}
		if wantSkipped && !strings.Contains(s.SkipReason, "services/api/**") {
			t.Errorf("step %s: reason %q does not name its patterns", s.ID, s.SkipReason)
		}
	}

	// release waited for test-api -> build-api -> checkout. With both
	// skipped it must wait for checkout directly, and still for build-web.
	want := map[string][]string{
		"checkout":  nil,
		"build-web": {"checkout"},
		"release":   {"checkout", "build-web"},
	}
	if deps := dependenciesOf(got); !reflect.DeepEqual(deps, want) {
		t.Errorf("dependencies = %v; want %v", deps, want)
	}

	// The caller's steps are untouched.
	if steps[1].Status != "" || !reflect.DeepEqual(steps[4].DependsOn, []string{"test-api", "build-web"}) {
		t.Error("Apply modified its input")
	}
}

func TestApply_NoLiveStepDependsOnASkippedStep(t *testing.T) {
	steps := []api.StepDef{
		step("a", nil),
		step("b", []string{"a"}, "x/**"),
		step("c", []string{"a", "b"}, "x/**"),
		step("d", []string{"c", "b"}),
		step("e", []string{"d", "c"}, "y/**"),
		step("f", []string{"e"}),
	}
	got, _ := Apply(steps, Of([]string{"unrelated.txt"}))

	skipped := make(map[string]bool)
	for _, s := range got {
		if s.Status == api.JobStatusSkipped {
			skipped[s.ID] = true
		}
	}
	for _, s := range got {
		if skipped[s.ID] {
			continue
		}
		seen := make(map[string]bool)
		for _, dep := range s.DependsOn {
			if skipped[dep] {
				t.Errorf("step %s still depends on skipped step %s", s.ID, dep)
			}
			if seen[dep] {
				t.Errorf("step %s lists dependency %s twice", s.ID, dep)
			}
			seen[dep] = true
		}
	}
	// f waited for e, which waited for d and c; c waited for a and b.
	want := map[string][]string{"a": nil, "d": {"a"}, "f": {"d", "a"}}
	if deps := dependenciesOf(got); !reflect.DeepEqual(deps, want) {
		t.Errorf("dependencies = %v; want %v", deps, want)
	}
}

func TestApply_RunsEverythingWhenItCannotTell(t *testing.T) {
	steps := []api.StepDef{
		step("build-api", nil, "services/api/**"),
		step("release", []string{"build-api"}),
	}

	t.Run("unknown changes", func(t *testing.T) {
		got, skipped := Apply(steps, Unknown())
		if len(skipped) != 0 || !reflect.DeepEqual(got, steps) {
			t.Errorf("Apply with unknown changes skipped %v", skipped)
		}
	})
	t.Run("matching change", func(t *testing.T) {
		_, skipped := Apply(steps, Of([]string{"services/api/main.go"}))
		if len(skipped) != 0 {
			t.Errorf("Apply skipped %v although a matching file changed", skipped)
		}
	})
	t.Run("steps without patterns always run", func(t *testing.T) {
		_, skipped := Apply([]api.StepDef{step("lint", nil)}, Of(nil))
		if len(skipped) != 0 {
			t.Errorf("Apply skipped %v, a step with no changes: patterns", skipped)
		}
	})
}

func TestPreserveSkips(t *testing.T) {
	original := []api.StepDef{
		step("checkout", nil),
		{ID: "build-api", DependsOn: []string{"checkout"}, Changes: []string{"services/api/**"}, With: map[string]string{"keep": "me"}},
		step("release", []string{"build-api"}),
	}
	applied, _ := Apply(original, Of([]string{"docs/guide.md"}))
	if original[1].With[SkippedKey] != "" {
		t.Fatal("Apply marked the caller's parameter map")
	}
	if applied[1].With["keep"] != "me" {
		t.Error("Apply dropped the step's own parameters")
	}

	// A rerun loads the stored steps, which have no SkipReason and no
	// Changes, and clears every status.
	rerun := make([]api.StepDef, len(applied))
	for i, s := range applied {
		rerun[i] = api.StepDef{ID: s.ID, DependsOn: s.DependsOn, With: s.With, Status: ""}
	}
	// A step skipped for another reason must not be preserved.
	rerun[2].Status = ""

	PreserveSkips(rerun)

	if rerun[1].Status != api.JobStatusSkipped || rerun[1].SkipReason != applied[1].SkipReason {
		t.Errorf("build-api after PreserveSkips = %q, %q; want it skipped with its original reason", rerun[1].Status, rerun[1].SkipReason)
	}
	if rerun[0].Status != "" || rerun[2].Status != "" {
		t.Errorf("PreserveSkips changed steps that were not skipped by changes: %q, %q", rerun[0].Status, rerun[2].Status)
	}
	if !reflect.DeepEqual(rerun[2].DependsOn, []string{"checkout"}) {
		t.Errorf("release depends on %v; want the rewired dependency kept", rerun[2].DependsOn)
	}
}
