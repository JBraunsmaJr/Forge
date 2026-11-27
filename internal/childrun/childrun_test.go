package childrun

import (
	"reflect"
	"strings"
	"testing"
)

func TestValidRefName(t *testing.T) {
	valid := []string{"main", "release/1.2", "feature/JIRA-123_login", "v1.0.0", "refs/heads/main", "refs/tags/v1.0.0", "a", "user@feature"}
	for _, name := range valid {
		if !ValidRefName(name) {
			t.Errorf("ValidRefName(%q) = false; want true", name)
		}
	}
	invalid := []string{
		"", "-main", "--upload-pack=x", "/main", "main/", "main.", "main.lock", "@",
		"a..b", "a//b", "a@{1}", "a b", "a\tb", "a\nb", "a~1", "a^", "a:b", "a?b", "a*b", "a[b", `a\b`,
		".hidden", "feature/.hidden", strings.Repeat("a", 256),
	}
	for _, name := range invalid {
		if ValidRefName(name) {
			t.Errorf("ValidRefName(%q) = true; want false", name)
		}
	}
}

func TestTargetValidate(t *testing.T) {
	cases := []struct {
		name              string
		target            Target
		allowPlaceholders bool
		wantErr           string
	}{
		{"project only", Target{Project: "sdk"}, false, ""},
		{"project ID", Target{Project: "9f1c2a7e-3b4d-4c5e-8f6a-1b2c3d4e5f60"}, false, ""},
		{"with refs", Target{Project: "sdk", Ref: "release/1.2", FallbackRef: "main"}, false, ""},
		{"placeholder while compiling", Target{Project: "sdk", Ref: "${{ env.FORGE_BRANCH }}"}, true, ""},
		{"placeholder when resolved", Target{Project: "sdk", Ref: "${{ env.FORGE_BRANCH }}"}, false, "ref"},
		{"no project", Target{}, false, "project"},
		{"project with a slash", Target{Project: "team/sdk"}, false, "project"},
		{"project that is an option", Target{Project: "-x"}, false, "project"},
		{"bad ref", Target{Project: "sdk", Ref: "--upload-pack=x"}, false, "ref"},
		{"bad fallback", Target{Project: "sdk", FallbackRef: "a..b"}, false, "fallback_ref"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := tc.target.Validate(tc.allowPlaceholders)
			switch {
			case tc.wantErr == "" && err != nil:
				t.Errorf("Validate() = %v; want valid", err)
			case tc.wantErr != "" && (err == nil || !strings.Contains(err.Error(), tc.wantErr)):
				t.Errorf("Validate() = %v; want an error mentioning %q", err, tc.wantErr)
			}
		})
	}
}

func TestTargetRoundTrip(t *testing.T) {
	own := map[string]string{"unrelated": "kept"}
	target := Target{Project: "sdk", Ref: "release/1.2", FallbackRef: "main"}

	with := target.AddTo(own)
	if len(own) != 1 {
		t.Error("AddTo modified the map it was given")
	}
	if with["unrelated"] != "kept" {
		t.Error("AddTo dropped the step's own parameters")
	}
	got, ok := FromWith(with)
	if !ok || !reflect.DeepEqual(got, target) {
		t.Errorf("FromWith() = %+v, %v; want %+v", got, ok, target)
	}

	if _, ok := FromWith(own); ok {
		t.Error("FromWith found a target in a map that has none")
	}
	if _, ok := FromWith(nil); ok {
		t.Error("FromWith found a target in a nil map")
	}

	minimal, _ := FromWith(Target{Project: "sdk"}.AddTo(nil))
	if minimal != (Target{Project: "sdk"}) {
		t.Errorf("minimal round trip = %+v", minimal)
	}
}

func TestValidateVariables(t *testing.T) {
	if err := ValidateVariables(map[string]string{"RELEASE_BRANCH": "release/1.2", "_x": "", "tag2": "v1"}); err != nil {
		t.Errorf("ValidateVariables() = %v; want valid", err)
	}
	if err := ValidateVariables(nil); err != nil {
		t.Errorf("ValidateVariables(nil) = %v; want valid", err)
	}

	invalid := map[string]map[string]string{
		"not a valid":   {"1BAD": "x"},
		"not a valid ":  {"A-B": "x"},
		"not a valid  ": {"": "x"},
		"reserved":      {"FORGE_COMMIT_SHA": "deadbeef"},
		"reserved ":     {"forge_api_token": "x"},
		"longer than":   {"BIG": strings.Repeat("x", maxVariableValue+1)},
		"NUL":           {"A": "a\x00b"},
	}
	for want, variables := range invalid {
		err := ValidateVariables(variables)
		if err == nil || !strings.Contains(err.Error(), strings.TrimSpace(want)) {
			t.Errorf("ValidateVariables(%v) = %v; want an error mentioning %q", variables, err, strings.TrimSpace(want))
		}
	}

	many := make(map[string]string)
	for i := 0; i <= maxVariables; i++ {
		many["V"+strings.Repeat("A", i)] = "x"
	}
	if err := ValidateVariables(many); err == nil || !strings.Contains(err.Error(), "too many") {
		t.Errorf("ValidateVariables(%d variables) = %v; want too many", len(many), err)
	}
}

func TestApplyVariables(t *testing.T) {
	env := map[string]string{"TARGET": "dev", "FORGE_COMMIT_SHA": "real", "KEEP": "1"}
	ApplyVariables(env, map[string]string{"TARGET": "prod", "NEW": "x", "FORGE_COMMIT_SHA": "forged", "forge_ref": "forged"})

	want := map[string]string{"TARGET": "prod", "FORGE_COMMIT_SHA": "real", "KEEP": "1", "NEW": "x"}
	if !reflect.DeepEqual(env, want) {
		t.Errorf("env = %v; want %v", env, want)
	}
}
