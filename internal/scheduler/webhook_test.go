package scheduler

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"github.com/JBraunsmaJr/forge/internal/api"
	"testing"
)

func TestInjectSCMMetadata(t *testing.T) {
	env := make(map[string]string)
	meta := api.WebhookRunMeta{
		RepoURL:   "https://github.com/org/repo",
		RepoName:  "org/repo",
		Branch:    "main",
		CommitSHA: "abc123456789",
		Event:     "pull_request",
		PRNumber:  42,
	}

	injectSCMMetadata(env, meta)

	expected := map[string]string{
		"FORGE_REPO_URL":   "https://github.com/org/repo",
		"FORGE_REPO_NAME":  "org/repo",
		"FORGE_BRANCH":     "main",
		"FORGE_COMMIT_SHA": "abc123456789",
		"FORGE_EVENT":      "pull_request",
		"FORGE_PR_NUMBER":  "42",
	}

	for k, v := range expected {
		if env[k] != v {
			t.Errorf("expected env[%s] = %q, got %q", k, v, env[k])
		}
	}
}

func TestParseGitHubPush(t *testing.T) {
	body := []byte(`{
		"ref": "refs/heads/main",
		"after": "abc1234",
		"head_commit": {
			"message": "feat: hello",
			"author": { "name": "alice" }
		},
		"repository": {
			"clone_url": "https://github.com/org/repo.git",
			"full_name": "org/repo"
		}
	}`)

	branch, sha, _, name, msg, author, err := parseGitHubPush(body)
	if err != nil {
		t.Fatalf("parse failed: %v", err)
	}

	if branch != "main" {
		t.Errorf("branch = %q", branch)
	}
	if sha != "abc1234" {
		t.Errorf("sha = %q", sha)
	}
	if name != "org/repo" {
		t.Errorf("name = %q", name)
	}
	if msg != "feat: hello" {
		t.Errorf("msg = %q", msg)
	}
	if author != "alice" {
		t.Errorf("author = %q", author)
	}
}

func TestParseGitHubPR(t *testing.T) {
	body := []byte(`{
		"action": "opened",
		"pull_request": {
			"number": 101,
			"head": {
				"ref": "feature-branch",
				"sha": "def5678"
			},
			"user": { "login": "bob" },
			"title": "fix: bug"
		},
		"repository": {
			"clone_url": "https://github.com/org/repo.git",
			"full_name": "org/repo"
		}
	}`)

	branch, sha, _, _, _, author, pr, err := parseGitHubPR(body)
	if err != nil {
		t.Fatalf("parse failed: %v", err)
	}

	if branch != "feature-branch" {
		t.Errorf("branch = %q", branch)
	}
	if sha != "def5678" {
		t.Errorf("sha = %q", sha)
	}
	if pr != 101 {
		t.Errorf("pr = %d", pr)
	}
	if author != "bob" {
		t.Errorf("author = %q", author)
	}
}

func TestParseGitLabPush(t *testing.T) {
	body := []byte(`{
		"ref": "refs/heads/main",
		"after": "abc1234",
		"commits": [
			{ "message": "feat: hello", "author": { "name": "alice" } }
		],
		"project": {
			"http_url_to_repo": "https://gitlab.com/org/repo.git",
			"path_with_namespace": "org/repo"
		}
	}`)

	branch, sha, _, name, msg, author, err := parseGitLabPush(body)
	if err != nil {
		t.Fatalf("parse failed: %v", err)
	}

	if branch != "main" {
		t.Errorf("branch = %q", branch)
	}
	if sha != "abc1234" {
		t.Errorf("sha = %q", sha)
	}
	if name != "org/repo" {
		t.Errorf("name = %q", name)
	}
	if msg != "feat: hello" {
		t.Errorf("msg = %q", msg)
	}
	if author != "alice" {
		t.Errorf("author = %q", author)
	}
}

func TestParseGitLabMR(t *testing.T) {
	body := []byte(`{
		"object_kind": "merge_request",
		"object_attributes": {
			"iid": 202,
			"source_branch": "feature-branch",
			"last_commit": {
				"id": "def5678",
				"message": "fix: bug",
				"author": { "name": "bob" }
			},
			"action": "open",
			"title": "Merge me"
		},
		"project": {
			"http_url_to_repo": "https://gitlab.com/org/repo.git",
			"path_with_namespace": "org/repo"
		},
		"user": { "name": "bob" }
	}`)

	branch, sha, _, _, _, author, pr, err := parseGitLabMR(body)
	if err != nil {
		t.Fatalf("parse failed: %v", err)
	}

	if branch != "feature-branch" {
		t.Errorf("branch = %q", branch)
	}
	if sha != "def5678" {
		t.Errorf("sha = %q", sha)
	}
	if pr != 202 {
		t.Errorf("pr = %d", pr)
	}
	if author != "bob" {
		t.Errorf("author = %q", author)
	}
}

func TestVerifyGitHubSig(t *testing.T) {
	secret := "super-secret"
	body := []byte(`{"hello":"world"}`)

	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write(body)
	sig := "sha256=" + hex.EncodeToString(mac.Sum(nil))

	if !verifyGitHubSig(body, secret, sig) {
		t.Error("valid signature rejected")
	}

	if verifyGitHubSig(body, secret, "sha256=wrong") {
		t.Error("invalid signature accepted")
	}
}
