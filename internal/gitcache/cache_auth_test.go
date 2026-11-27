package gitcache

import (
	"testing"

	"github.com/JBraunsmaJr/forge/internal/scm"
)

func TestAuthURL(t *testing.T) {
	t.Setenv(scm.EnvGitLabURL, "https://gitlab.example.com")
	t.Setenv(scm.EnvGitHubURL, "")
	c := &Cache{}

	tests := []struct {
		name    string
		repoURL string
		token   string
		want    string
	}{
		{"public GitHub", "https://github.com/org/repo.git", "tok", "https://x-access-token:tok@github.com/org/repo.git"},
		{"public GitLab", "https://gitlab.com/group/repo.git", "tok", "https://oauth2:tok@gitlab.com/group/repo.git"},
		{"self-managed GitLab", "https://gitlab.example.com/group/sub/repo.git", "tok", "https://oauth2:tok@gitlab.example.com/group/sub/repo.git"},
		{"no token", "https://gitlab.example.com/group/repo.git", "", "https://gitlab.example.com/group/repo.git"},
		{"unknown host keeps the token to itself", "https://git.other.example/org/repo.git", "tok", "https://git.other.example/org/repo.git"},
		{"lookalike host keeps the token to itself", "https://github.com.attacker.example/org/repo.git", "tok", "https://github.com.attacker.example/org/repo.git"},
		{"plain HTTP is never given a token", "http://gitlab.example.com/group/repo.git", "tok", "http://gitlab.example.com/group/repo.git"},
		{"SSH URL is left alone", "git@gitlab.example.com:group/repo.git", "tok", "git@gitlab.example.com:group/repo.git"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := c.authURL(tt.repoURL, tt.token); got != tt.want {
				t.Errorf("authURL() = %q; want %q", got, tt.want)
			}
		})
	}
}
