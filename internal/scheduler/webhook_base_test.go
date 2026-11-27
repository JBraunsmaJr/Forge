package scheduler

import "testing"

func TestGitHubChangeBase(t *testing.T) {
	cases := []struct {
		name, event, body   string
		wantSHA, wantBranch string
	}{
		{"push", "push", `{"ref":"refs/heads/main","before":"aaa111","after":"bbb222"}`, "aaa111", ""},
		{"pull request", "pull_request", `{"action":"synchronize","pull_request":{"head":{"sha":"bbb222"},"base":{"ref":"main"}}}`, "", "main"},
		{"push without before", "push", `{"after":"bbb222"}`, "", ""},
		{"unreadable payload", "push", `{not json`, "", ""},
		{"other event", "release", `{"before":"aaa111"}`, "", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			sha, branch := githubChangeBase(tc.event, []byte(tc.body))
			if sha != tc.wantSHA || branch != tc.wantBranch {
				t.Errorf("githubChangeBase() = %q, %q; want %q, %q", sha, branch, tc.wantSHA, tc.wantBranch)
			}
		})
	}
}

func TestGitLabChangeBase(t *testing.T) {
	cases := []struct {
		name, event, body   string
		wantSHA, wantBranch string
	}{
		{"push", "push", `{"object_kind":"push","before":"aaa111","after":"bbb222"}`, "aaa111", ""},
		{"merge request", "merge_request", `{"object_attributes":{"target_branch":"main","source_branch":"feature"}}`, "", "main"},
		{"merge request without a target", "merge_request", `{"object_attributes":{}}`, "", ""},
		{"unreadable payload", "merge_request", `[]`, "", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			sha, branch := gitlabChangeBase(tc.event, []byte(tc.body))
			if sha != tc.wantSHA || branch != tc.wantBranch {
				t.Errorf("gitlabChangeBase() = %q, %q; want %q, %q", sha, branch, tc.wantSHA, tc.wantBranch)
			}
		})
	}
}
