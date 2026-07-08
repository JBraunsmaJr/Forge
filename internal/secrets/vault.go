package secrets

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// Client talks to Vault's KV v2 secrets engine over HTTP.
type Client struct {
	addr  string
	token string
	mount string // KV mount, e.g. "secret"
	http  *http.Client
}

// NewClient creates a Vault client.
func NewClient(addr, token string) *Client {
	return &Client{
		addr:  strings.TrimRight(addr, "/"),
		token: token,
		mount: "secret",
		http:  &http.Client{Timeout: 5 * time.Second},
	}
}

// GetScoped resolves a secret name by walking the scope hierarchy:
// project → org → global → legacy. Returns the first match found.
// Used by agents at job execution time.
func (c *Client) GetScoped(name, orgID, projectID string) (string, error) {

	if projectID != "" {
		if val, err := c.getFromPrefix(projectScopePath(projectID), name); err == nil {
			return val, nil
		}
	}

	if orgID != "" {
		if val, err := c.getFromPrefix(orgScopePath(orgID), name); err == nil {
			return val, nil
		}
	}

	if val, err := c.getFromPrefix("forge/global", name); err == nil {
		return val, nil
	}

	if val, err := c.getFromPrefix("forge", name); err == nil {
		return val, nil
	}

	scopes := "global"
	if orgID != "" {
		scopes = fmt.Sprintf("org %s, %s", orgID, scopes)
	}
	if projectID != "" {
		scopes = fmt.Sprintf("project %s, %s", projectID, scopes)
	}
	return "", fmt.Errorf("secret %q not found in any scope (%s)", name, scopes)
}

// Get fetches a secret from an explicit path prefix.
// prefix examples: "forge/global", "forge/orgs/abc123", "forge/projects/xyz789"
func (c *Client) Get(prefix, name string) (string, error) {
	val, err := c.getFromPrefix(prefix, name)
	if err != nil {
		return "", fmt.Errorf("vault get %s/%s: %w", prefix, name, err)
	}
	return val, nil
}

// Set stores a secret at an explicit path prefix.
func (c *Client) Set(prefix, name, value string) error {
	body, _ := json.Marshal(map[string]any{
		"data": map[string]string{"value": value},
	})
	url := c.dataURL(prefix, name)
	resp, err := c.do("POST", url, bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("vault set %s/%s: %w", prefix, name, err)
	}
	defer resp.Body.Close()
	io.Copy(io.Discard, resp.Body)
	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusNoContent {
		return fmt.Errorf("vault returned %d when storing %s/%s", resp.StatusCode, prefix, name)
	}
	return nil
}

// List returns the secret names at an explicit path prefix.
func (c *Client) List(prefix string) ([]string, error) {
	url := fmt.Sprintf("%s/v1/%s/metadata/%s/", c.addr, c.mount, prefix)
	req, err := http.NewRequest("LIST", url, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("X-Vault-Token", c.token)
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("vault list %s: %w", prefix, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusNotFound {
		return nil, nil
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("vault list returned %d for %s", resp.StatusCode, prefix)
	}
	var result struct {
		Data struct {
			Keys []string `json:"keys"`
		} `json:"data"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, fmt.Errorf("decoding vault list: %w", err)
	}
	return result.Data.Keys, nil
}

// Delete removes a secret and all its versions from Vault.
func (c *Client) Delete(prefix, name string) error {
	url := fmt.Sprintf("%s/v1/%s/metadata/%s/%s", c.addr, c.mount, prefix, name)
	resp, err := c.do("DELETE", url, nil)
	if err != nil {
		return fmt.Errorf("vault delete %s/%s: %w", prefix, name, err)
	}
	defer resp.Body.Close()
	io.Copy(io.Discard, resp.Body)
	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusNoContent {
		return fmt.Errorf("vault delete returned %d for %s/%s", resp.StatusCode, prefix, name)
	}
	return nil
}

// Ping checks Vault is reachable and the token is valid.
func (c *Client) Ping() error {
	resp, err := c.do("GET", c.addr+"/v1/auth/token/lookup-self", nil)
	if err != nil {
		return fmt.Errorf("cannot reach Vault at %s: %w", c.addr, err)
	}
	defer resp.Body.Close()
	io.Copy(io.Discard, resp.Body)
	switch resp.StatusCode {
	case http.StatusOK:
		return nil
	case http.StatusForbidden:
		return fmt.Errorf("Vault token is invalid or expired")
	default:
		return fmt.Errorf("Vault ping returned %d", resp.StatusCode)
	}
}

// GlobalScopePath returns the Vault prefix for global secrets.
func GlobalScopePath() string { return "forge/global" }

// OrgScopePath returns the Vault prefix for org-scoped secrets.
func OrgScopePath(orgID string) string { return orgScopePath(orgID) }

// ProjectScopePath returns the Vault prefix for project-scoped secrets.
func ProjectScopePath(projectID string) string { return projectScopePath(projectID) }

func orgScopePath(orgID string) string         { return "forge/orgs/" + orgID }
func projectScopePath(projectID string) string { return "forge/projects/" + projectID }

// getFromPrefix is the internal read path used by both Get and GetScoped.
// Returns ("", error) when the secret is not found — callers use this to
// continue walking the scope chain.
func (c *Client) getFromPrefix(prefix, name string) (string, error) {
	resp, err := c.do("GET", c.dataURL(prefix, name), nil)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusNotFound {
		return "", fmt.Errorf("not found")
	}
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("vault returned %d", resp.StatusCode)
	}
	// KV v2: { "data": { "data": { "value": "..." }, "metadata": {...} } }
	var result struct {
		Data struct {
			Data map[string]string `json:"data"`
		} `json:"data"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return "", fmt.Errorf("decoding vault response: %w", err)
	}
	val, ok := result.Data.Data["value"]
	if !ok {
		return "", fmt.Errorf("no 'value' field")
	}
	return val, nil
}

// dataURL builds the KV v2 data URL: /v1/{mount}/data/{prefix}/{name}
func (c *Client) dataURL(prefix, name string) string {
	return fmt.Sprintf("%s/v1/%s/data/%s/%s", c.addr, c.mount, prefix, name)
}

// do makes an authenticated HTTP request to Vault.
func (c *Client) do(method, url string, body io.Reader) (*http.Response, error) {
	req, err := http.NewRequest(method, url, body)
	if err != nil {
		return nil, err
	}
	req.Header.Set("X-Vault-Token", c.token)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	return c.http.Do(req)
}
