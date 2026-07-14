package agent

import "testing"

// ── evalRuntimeCondition ──────────────────────────────────────────────────────

func TestEvalRuntimeCondition_Empty(t *testing.T) {
	if !evalRuntimeCondition("", nil) {
		t.Error("empty condition should always be true")
	}
}

func TestEvalRuntimeCondition_Equality(t *testing.T) {
	env := map[string]string{"BRANCH": "main", "ENV": "production"}

	cases := []struct {
		expr string
		want bool
	}{
		{"$BRANCH == 'main'", true},
		{"$BRANCH == 'dev'", false},
		{"$ENV == 'production'", true},
		{"$ENV == 'staging'", false},
		{"${BRANCH} == 'main'", true}, // curly-brace syntax
	}
	for _, c := range cases {
		got := evalRuntimeCondition(c.expr, env)
		if got != c.want {
			t.Errorf("evalRuntimeCondition(%q) = %v, want %v", c.expr, got, c.want)
		}
	}
}

func TestEvalRuntimeCondition_Inequality(t *testing.T) {
	env := map[string]string{"ENV": "production"}
	if !evalRuntimeCondition("$ENV != 'staging'", env) {
		t.Error("$ENV != 'staging' should be true when ENV=production")
	}
	if evalRuntimeCondition("$ENV != 'production'", env) {
		t.Error("$ENV != 'production' should be false when ENV=production")
	}
}

func TestEvalRuntimeCondition_Truthy(t *testing.T) {
	env := map[string]string{"GIT_TAG": "v1.0.0", "EMPTY": ""}
	if !evalRuntimeCondition("$GIT_TAG", env) {
		t.Error("non-empty var should be truthy")
	}
	if evalRuntimeCondition("$EMPTY", env) {
		t.Error("empty var should be falsy")
	}
	if evalRuntimeCondition("$UNSET_VAR", env) {
		t.Error("missing var should be falsy")
	}
}

func TestEvalRuntimeCondition_FalsyBang(t *testing.T) {
	env := map[string]string{"GIT_TAG": "", "BRANCH": "main"}
	if !evalRuntimeCondition("!$GIT_TAG", env) {
		t.Error("!$GIT_TAG should be true when GIT_TAG is empty")
	}
	if evalRuntimeCondition("!$BRANCH", env) {
		t.Error("!$BRANCH should be false when BRANCH is non-empty")
	}
}

// ── isSchedulerCondition ──────────────────────────────────────────────────────

func TestIsSchedulerCondition(t *testing.T) {
	scheduler := []string{"", "success()", "failure()", "always()", "tag()", "branch(main)",
		"SUCCESS()", "FAILURE()", "ALWAYS()", "TAG()", "BRANCH(develop)"}
	for _, c := range scheduler {
		if !isSchedulerCondition(c) {
			t.Errorf("isSchedulerCondition(%q) should be true", c)
		}
	}

	runtime := []string{
		"$BRANCH == 'main'",
		"$GIT_TAG != ''",
		"$DEPLOY",
	}
	for _, c := range runtime {
		if isSchedulerCondition(c) {
			t.Errorf("isSchedulerCondition(%q) should be false", c)
		}
	}
}
