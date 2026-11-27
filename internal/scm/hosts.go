package scm

import (
	"net/url"
	"os"
	"strings"
)

// Provider names, as stored on runs and used to choose an API dialect.
const (
	ProviderGitHub = "github"
	ProviderGitLab = "gitlab"
)

// Environment variables that point Forge at self-hosted instances. Each
// holds a base URL such as "https://gitlab.example.com". The public services
// are always recognized as well, so setting one does not disable the other.
const (
	// EnvGitLabURL is the base URL of a self-managed GitLab instance.
	EnvGitLabURL = "FORGE_GITLAB_URL"
	// EnvGitHubURL is the base URL of a GitHub Enterprise Server instance.
	EnvGitHubURL = "FORGE_GITHUB_URL"
)

const (
	publicGitLabURL    = "https://gitlab.com"
	publicGitHubURL    = "https://github.com"
	publicGitHubAPIURL = "https://api.github.com"

	// gitHubEnterpriseAPIPath is where GitHub Enterprise Server serves its
	// REST API, relative to the instance's base URL.
	gitHubEnterpriseAPIPath = "/api/v3"
)

// GitLabURL returns the base URL of the GitLab instance Forge signs users in
// against: FORGE_GITLAB_URL when set, otherwise gitlab.com. It has no
// trailing slash.
func GitLabURL() string {
	return baseURLFromEnv(EnvGitLabURL, publicGitLabURL)
}

// GitHubURL returns the base URL of the GitHub instance Forge signs users in
// against: FORGE_GITHUB_URL when set, otherwise github.com. It has no
// trailing slash.
func GitHubURL() string {
	return baseURLFromEnv(EnvGitHubURL, publicGitHubURL)
}

// GitHubAPIURL returns the REST API base URL that goes with GitHubURL.
func GitHubAPIURL() string {
	return gitHubAPIBase(GitHubURL())
}

// ProviderForRepo reports which provider hosts a repository, judging by the
// URL's host: ProviderGitHub, ProviderGitLab, or "" when the host is neither
// a public service nor a configured instance.
//
// The host must match exactly. A substring test would treat
// "github.com.attacker.example" as GitHub and hand it a token.
func ProviderForRepo(repoURL string) string {
	host := RepoHost(repoURL)
	switch {
	case host == "":
		return ""
	case host == hostOf(publicGitHubURL), host == hostOf(GitHubURL()):
		return ProviderGitHub
	case host == hostOf(publicGitLabURL), host == hostOf(GitLabURL()):
		return ProviderGitLab
	}
	return ""
}

// RepoHost returns the lowercased host, with its port if any, of a
// repository URL. It understands "https://host/owner/repo.git",
// "ssh://git@host/owner/repo.git", and the scp-like "git@host:owner/repo.git".
// It returns "" when no host can be found.
func RepoHost(repoURL string) string {
	repoURL = strings.TrimSpace(repoURL)
	if strings.Contains(repoURL, "://") {
		return hostOf(repoURL)
	}
	// scp-like: [user@]host:path
	hostPart, _, found := strings.Cut(repoURL, ":")
	if !found || strings.Contains(hostPart, "/") {
		return ""
	}
	if _, afterUser, hasUser := strings.Cut(hostPart, "@"); hasUser {
		hostPart = afterUser
	}
	return strings.ToLower(hostPart)
}

// gitHubAPIBaseForRepo returns the REST API base URL for the GitHub instance
// that hosts repoURL: api.github.com for the public service, and
// "<scheme>://<host>/api/v3" for GitHub Enterprise Server.
func gitHubAPIBaseForRepo(repoURL string) string {
	host := RepoHost(repoURL)
	if host == "" || host == hostOf(publicGitHubURL) {
		return publicGitHubAPIURL
	}
	scheme := "https"
	if u, err := url.Parse(strings.TrimSpace(repoURL)); err == nil && (u.Scheme == "http" || u.Scheme == "https") {
		scheme = u.Scheme
	}
	return scheme + "://" + host + gitHubEnterpriseAPIPath
}

// gitHubAPIBase returns the REST API base URL for a GitHub base URL.
func gitHubAPIBase(baseURL string) string {
	if hostOf(baseURL) == hostOf(publicGitHubURL) {
		return publicGitHubAPIURL
	}
	return strings.TrimSuffix(baseURL, "/") + gitHubEnterpriseAPIPath
}

// baseURLFromEnv returns the trimmed value of an environment variable with
// any trailing slash removed, or fallback when it is unset.
func baseURLFromEnv(name, fallback string) string {
	value := strings.TrimSuffix(strings.TrimSpace(os.Getenv(name)), "/")
	if value == "" {
		return fallback
	}
	return value
}

// hostOf returns the lowercased host (with port) of a URL that has a scheme,
// or "" when it cannot be parsed.
func hostOf(rawURL string) string {
	u, err := url.Parse(strings.TrimSpace(rawURL))
	if err != nil {
		return ""
	}
	return strings.ToLower(u.Host)
}
