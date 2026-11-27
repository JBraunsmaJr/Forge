package scheduler

import "testing"

// TestSecurity_S4_ForkChangesAreRecognized covers threat model gap S4: a
// pull or merge request from a fork must be recognized as such, because its
// pipeline definition comes from someone who cannot push to the repository.
// Anything that cannot be attributed with certainty counts as a fork.
func TestSecurity_S4_ForkChangesAreRecognized(t *testing.T) {
	t.Run("GitHub", func(t *testing.T) {
		tests := []struct {
			name string
			body string
			want bool
		}{
			{"branch in the same repository", `{"pull_request":{"head":{"repo":{"id":10}},"base":{"repo":{"id":10}}}}`, false},
			{"head in another repository", `{"pull_request":{"head":{"repo":{"id":99}},"base":{"repo":{"id":10}}}}`, true},
			{"head repository deleted", `{"pull_request":{"head":{"repo":null},"base":{"repo":{"id":10}}}}`, true},
			{"head repository absent", `{"pull_request":{"base":{"repo":{"id":10}}}}`, true},
			{"base repository absent", `{"pull_request":{"head":{"repo":{"id":10}}}}`, true},
			{"IDs missing", `{"pull_request":{"head":{"repo":{}},"base":{"repo":{}}}}`, true},
			{"not a pull request payload", `{"ref":"refs/heads/main"}`, true},
			{"not JSON", `nope`, true},
			{"empty body", ``, true},
		}
		for _, tt := range tests {
			t.Run(tt.name, func(t *testing.T) {
				if got := githubPRFromFork([]byte(tt.body)); got != tt.want {
					t.Errorf("githubPRFromFork() = %v; want %v", got, tt.want)
				}
			})
		}
	})

	t.Run("GitLab", func(t *testing.T) {
		tests := []struct {
			name string
			body string
			want bool
		}{
			{"branch in the same project", `{"object_attributes":{"source_project_id":7,"target_project_id":7}}`, false},
			{"source in another project", `{"object_attributes":{"source_project_id":8,"target_project_id":7}}`, true},
			{"source project missing", `{"object_attributes":{"target_project_id":7}}`, true},
			{"target project missing", `{"object_attributes":{"source_project_id":7}}`, true},
			{"not a merge request payload", `{"ref":"refs/heads/main"}`, true},
			{"not JSON", `nope`, true},
		}
		for _, tt := range tests {
			t.Run(tt.name, func(t *testing.T) {
				if got := gitlabMRFromFork([]byte(tt.body)); got != tt.want {
					t.Errorf("gitlabMRFromFork() = %v; want %v", got, tt.want)
				}
			})
		}
	})
}

// TestSecurity_S4_ForkRunsAreOffByDefault checks that running changes from
// forks requires an explicit opt-in.
func TestSecurity_S4_ForkRunsAreOffByDefault(t *testing.T) {
	tests := []struct {
		value string
		want  bool
	}{
		{"", false},
		{"0", false},
		{"false", false},
		{"no", false},
		{"maybe", false},
		{"1", true},
		{"true", true},
		{" TRUE ", true},
		{"yes", true},
	}
	for _, tt := range tests {
		t.Setenv(envAllowForkRuns, tt.value)
		if got := forkRunsAllowed(); got != tt.want {
			t.Errorf("forkRunsAllowed() with %s=%q = %v; want %v", envAllowForkRuns, tt.value, got, tt.want)
		}
	}
}
