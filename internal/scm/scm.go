package scm

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"
)

// PostStatus reports a commit status to GitHub or GitLab.
// state should be one of: "pending", "success", "failure", "error".
func PostStatus(provider, repoURL, sha, token, state, targetURL, description, context string) error {
	if token == "" {
		return nil // No token, no status
	}

	if context == "" {
		context = "forge/ci"
	}

	switch provider {
	case "github":
		return postGitHubStatus(repoURL, sha, token, state, targetURL, description, context)
	case "gitlab":
		return postGitLabStatus(repoURL, sha, token, state, targetURL, description, context)
	default:
		return nil // Generic or unsupported provider
	}
}

func postGitHubStatus(repoURL, sha, token, state, targetURL, description, context string) error {
	repoPath := getRepoPath(repoURL)
	if repoPath == "" {
		return fmt.Errorf("could not parse repo path from %s", repoURL)
	}

	apiURL := fmt.Sprintf("https://api.github.com/repos/%s/statuses/%s", repoPath, sha)

	payload := map[string]string{
		"state":       state,
		"target_url":  targetURL,
		"description": description,
		"context":     context,
	}

	return doRequest("POST", apiURL, token, payload)
}

func postGitLabStatus(repoURL, sha, token, state, targetURL, description, context string) error {
	// Map GitHub states to GitLab states
	glState := state
	switch state {
	case "failure":
		glState = "failed"
	case "error":
		glState = "canceled"
	}

	repoPath := getRepoPath(repoURL)
	if repoPath == "" {
		return fmt.Errorf("could not parse repo path from %s", repoURL)
	}

	// GitLab project ID can be the URL-encoded path
	projectID := strings.ReplaceAll(repoPath, "/", "%2F")

	// Derive base API URL from repoURL to support self-hosted instances
	baseURL := "https://gitlab.com"
	if idx := strings.Index(repoURL, "://"); idx != -1 {
		rest := repoURL[idx+3:]
		if slashIdx := strings.Index(rest, "/"); slashIdx != -1 {
			baseURL = repoURL[:idx+3] + rest[:slashIdx]
		}
	}

	apiURL := fmt.Sprintf("%s/api/v4/projects/%s/statuses/%s", baseURL, projectID, sha)

	payload := map[string]string{
		"state":       glState,
		"target_url":  targetURL,
		"description": description,
		"name":        context,
	}

	return doRequest("POST", apiURL, token, payload)
}

func getRepoPath(repoURL string) string {
	repoURL = strings.TrimSuffix(repoURL, ".git")
	repoURL = strings.TrimSuffix(repoURL, "/")

	// Handle http(s)://
	if idx := strings.Index(repoURL, "://"); idx != -1 {
		path := repoURL[idx+3:]
		if slashIdx := strings.Index(path, "/"); slashIdx != -1 {
			return path[slashIdx+1:]
		}
	}

	// Handle git@host:path
	if idx := strings.Index(repoURL, ":"); idx != -1 {
		// Ensure we don't pick up the protocol colon
		if !strings.Contains(repoURL[:idx], "://") {
			return repoURL[idx+1:]
		}
	}

	return ""
}

func doRequest(method, url, token string, payload any) error {
	data, err := json.Marshal(payload)
	if err != nil {
		return err
	}

	req, err := http.NewRequest(method, url, bytes.NewReader(data))
	if err != nil {
		return err
	}

	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("PRIVATE-TOKEN", token)

	client := &http.Client{Timeout: 5 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 400 {
		return fmt.Errorf("SCM API returned HTTP %d", resp.StatusCode)
	}

	return nil
}
