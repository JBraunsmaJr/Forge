package scm

import "testing"

func TestRepoHost(t *testing.T) {
	tests := []struct {
		repoURL string
		want    string
	}{
		{"https://github.com/org/repo.git", "github.com"},
		{"https://GitLab.Example.COM/group/sub/repo", "gitlab.example.com"},
		{"https://gitlab.example.com:8443/group/repo.git", "gitlab.example.com:8443"},
		{"https://oauth2:token@gitlab.example.com/group/repo.git", "gitlab.example.com"},
		{"ssh://git@gitlab.example.com/group/repo.git", "gitlab.example.com"},
		{"git@github.com:org/repo.git", "github.com"},
		{"gitlab.example.com:group/repo.git", "gitlab.example.com"},
		{"  https://github.com/org/repo  ", "github.com"},
		{"/srv/git/repo.git", ""},
		{"./relative/repo", ""},
		{"", ""},
	}
	for _, tt := range tests {
		if got := RepoHost(tt.repoURL); got != tt.want {
			t.Errorf("RepoHost(%q) = %q; want %q", tt.repoURL, got, tt.want)
		}
	}
}

func TestProviderForRepo_PublicServices(t *testing.T) {
	t.Setenv(EnvGitLabURL, "")
	t.Setenv(EnvGitHubURL, "")

	tests := []struct {
		repoURL string
		want    string
	}{
		{"https://github.com/org/repo.git", ProviderGitHub},
		{"git@github.com:org/repo.git", ProviderGitHub},
		{"https://gitlab.com/group/repo.git", ProviderGitLab},
		{"https://gitlab.example.com/group/repo.git", ""},
		// Lookalike hosts must not be mistaken for the real service.
		{"https://github.com.attacker.example/org/repo.git", ""},
		{"https://attacker.example/github.com/repo.git", ""},
		{"https://notgitlab.com/group/repo.git", ""},
		{"", ""},
	}
	for _, tt := range tests {
		if got := ProviderForRepo(tt.repoURL); got != tt.want {
			t.Errorf("ProviderForRepo(%q) = %q; want %q", tt.repoURL, got, tt.want)
		}
	}
}

func TestProviderForRepo_SelfHosted(t *testing.T) {
	t.Setenv(EnvGitLabURL, "https://gitlab.example.com/")
	t.Setenv(EnvGitHubURL, "https://ghe.example.com")

	tests := []struct {
		repoURL string
		want    string
	}{
		{"https://gitlab.example.com/group/sub/repo.git", ProviderGitLab},
		{"git@gitlab.example.com:group/repo.git", ProviderGitLab},
		{"https://ghe.example.com/org/repo.git", ProviderGitHub},
		// The public services stay recognized alongside the configured ones.
		{"https://gitlab.com/group/repo.git", ProviderGitLab},
		{"https://github.com/org/repo.git", ProviderGitHub},
		{"https://other.example.com/org/repo.git", ""},
		{"https://gitlab.example.com.attacker.example/x/y.git", ""},
	}
	for _, tt := range tests {
		if got := ProviderForRepo(tt.repoURL); got != tt.want {
			t.Errorf("ProviderForRepo(%q) = %q; want %q", tt.repoURL, got, tt.want)
		}
	}
}

func TestBaseURLs(t *testing.T) {
	t.Setenv(EnvGitLabURL, "")
	t.Setenv(EnvGitHubURL, "")
	if got := GitLabURL(); got != "https://gitlab.com" {
		t.Errorf("GitLabURL() default = %q", got)
	}
	if got := GitHubURL(); got != "https://github.com" {
		t.Errorf("GitHubURL() default = %q", got)
	}
	if got := GitHubAPIURL(); got != "https://api.github.com" {
		t.Errorf("GitHubAPIURL() default = %q", got)
	}

	t.Setenv(EnvGitLabURL, "  https://gitlab.example.com/  ")
	t.Setenv(EnvGitHubURL, "https://ghe.example.com/")
	if got := GitLabURL(); got != "https://gitlab.example.com" {
		t.Errorf("GitLabURL() = %q; want the trimmed value without a trailing slash", got)
	}
	if got := GitHubURL(); got != "https://ghe.example.com" {
		t.Errorf("GitHubURL() = %q", got)
	}
	if got := GitHubAPIURL(); got != "https://ghe.example.com/api/v3" {
		t.Errorf("GitHubAPIURL() = %q", got)
	}
}

func TestGitHubAPIBaseForRepo(t *testing.T) {
	tests := []struct {
		repoURL string
		want    string
	}{
		{"https://github.com/org/repo.git", "https://api.github.com"},
		{"git@github.com:org/repo.git", "https://api.github.com"},
		{"https://ghe.example.com/org/repo.git", "https://ghe.example.com/api/v3"},
		{"http://ghe.internal:8080/org/repo", "http://ghe.internal:8080/api/v3"},
		{"git@ghe.example.com:org/repo.git", "https://ghe.example.com/api/v3"},
		{"", "https://api.github.com"},
	}
	for _, tt := range tests {
		if got := gitHubAPIBaseForRepo(tt.repoURL); got != tt.want {
			t.Errorf("gitHubAPIBaseForRepo(%q) = %q; want %q", tt.repoURL, got, tt.want)
		}
	}
}
