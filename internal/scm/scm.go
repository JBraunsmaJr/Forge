package scm

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// PostStatus reports a commit status to GitHub or GitLab.
// state should be one of: "pending", "success", "failure", "error".
func PostStatus(provider, repoURL, sha, token, state, targetURL, description, context string) error {
	if token == "" {
		fmt.Printf("[scm] skipping status report for %s: no token provided\n", repoURL)
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

// CreateRelease creates a new release in GitHub or GitLab.
// Returns (releaseID, uploadURL, error).
func CreateRelease(provider, repoURL, token, tag, name, body string) (string, string, error) {
	if token == "" {
		return "", "", fmt.Errorf("SCM token required for release")
	}

	switch provider {
	case "github":
		return createGitHubRelease(repoURL, token, tag, name, body)
	case "gitlab":
		return createGitLabRelease(repoURL, token, tag, name, body)
	default:
		return "", "", fmt.Errorf("unsupported provider: %s", provider)
	}
}

func createGitHubRelease(repoURL, token, tag, name, body string) (string, string, error) {
	repoPath := getRepoPath(repoURL)
	apiURL := fmt.Sprintf("https://api.github.com/repos/%s/releases", repoPath)

	payload := map[string]any{
		"tag_name": tag,
		"name":     name,
		"body":     body,
	}

	resp, err := doRequestWithResponse("POST", apiURL, token, payload)
	if err != nil {
		return "", "", err
	}

	var data struct {
		ID        int    `json:"id"`
		UploadURL string `json:"upload_url"`
	}
	if err := json.Unmarshal(resp, &data); err != nil {
		return "", "", err
	}

	// GitHub returns upload_url as a template: "https://.../assets{?name,label}"
	uploadURL := data.UploadURL
	if idx := strings.Index(uploadURL, "{"); idx != -1 {
		uploadURL = uploadURL[:idx]
	}

	return fmt.Sprintf("%d", data.ID), uploadURL, nil
}

func createGitLabRelease(repoURL, token, tag, name, body string) (string, string, error) {
	repoPath := getRepoPath(repoURL)
	projectID := strings.ReplaceAll(repoPath, "/", "%2F")
	baseURL := "https://gitlab.com"
	if idx := strings.Index(repoURL, "://"); idx != -1 {
		rest := repoURL[idx+3:]
		if slashIdx := strings.Index(rest, "/"); slashIdx != -1 {
			baseURL = repoURL[:idx+3] + rest[:slashIdx]
		}
	}

	apiURL := fmt.Sprintf("%s/api/v4/projects/%s/releases", baseURL, projectID)

	payload := map[string]any{
		"tag_name":    tag,
		"name":        name,
		"description": body,
	}

	err := doRequest("POST", apiURL, token, payload)
	if err != nil {
		return "", "", err
	}

	return tag, "", nil // GitLab uses tag as identifier, no separate upload URL
}

// UploadAsset uploads a file to an existing release.
func UploadAsset(provider, repoURL, token, uploadURL, releaseID, filename, contentType string, size int64, content io.Reader) error {
	switch provider {
	case "github":
		return uploadGitHubAsset(token, uploadURL, filename, contentType, size, content)
	case "gitlab":
		return uploadGitLabAsset(repoURL, token, releaseID, filename, contentType, size, content)
	default:
		return fmt.Errorf("unsupported provider: %s", provider)
	}
}

func uploadGitHubAsset(token, uploadURL, filename, contentType string, size int64, content io.Reader) error {
	apiURL := fmt.Sprintf("%s?name=%s", uploadURL, url.QueryEscape(filename))

	var body io.Reader = content
	if size == 0 {
		body = http.NoBody
	}

	req, err := http.NewRequest("POST", apiURL, body)
	if err != nil {
		return err
	}

	if contentType == "" {
		contentType = "application/octet-stream"
	}

	req.ContentLength = size
	req.Header.Set("Content-Type", contentType)
	req.Header.Set("Content-Length", strconv.FormatInt(size, 10))
	req.Header.Set("User-Agent", "Forge-CI")
	req.Header.Set("Authorization", "Bearer "+token)

	// Ensure we don't use chunked encoding
	req.TransferEncoding = []string{"identity"}

	client := &http.Client{Timeout: 5 * time.Minute}
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 400 {
		body, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("GitHub asset upload failed (%d): %s", resp.StatusCode, string(body))
	}

	return nil
}

func uploadGitLabAsset(repoURL, token, tag, filename, contentType string, size int64, content io.Reader) error {
	repoPath := getRepoPath(repoURL)
	projectID := strings.ReplaceAll(repoPath, "/", "%2F")
	baseURL := "https://gitlab.com"
	if idx := strings.Index(repoURL, "://"); idx != -1 {
		rest := repoURL[idx+3:]
		if slashIdx := strings.Index(rest, "/"); slashIdx != -1 {
			baseURL = repoURL[:idx+3] + rest[:slashIdx]
		}
	}

	// 1. Upload the file
	_ = fmt.Sprintf("%s/api/v4/projects/%s/uploads", baseURL, projectID)

	// GitLab expects multipart/form-data for uploads
	// TODO: Gitlab implementation

	return fmt.Errorf("GitLab asset upload not yet implemented")
}

func doRequest(method, url, token string, payload any) error {
	_, err := doRequestWithResponse(method, url, token, payload)
	return err
}

func doRequestWithResponse(method, url, token string, payload any) ([]byte, error) {
	var body io.Reader
	if payload != nil {
		data, err := json.Marshal(payload)
		if err != nil {
			return nil, err
		}
		body = bytes.NewReader(data)
	}

	req, err := http.NewRequest(method, url, body)
	if err != nil {
		return nil, err
	}

	if payload != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	req.Header.Set("User-Agent", "Forge-CI")
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("PRIVATE-TOKEN", token)

	client := &http.Client{Timeout: 30 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	respBody, _ := io.ReadAll(resp.Body)
	if resp.StatusCode >= 400 {
		return nil, fmt.Errorf("SCM API returned HTTP %d: %s", resp.StatusCode, string(respBody))
	}

	return respBody, nil
}
