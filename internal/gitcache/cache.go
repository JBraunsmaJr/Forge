package gitcache

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
)

type Cache struct {
	dir string
	mu  sync.Mutex
}

func New(dir string) (*Cache, error) {
	if err := os.MkdirAll(dir, 0755); err != nil {
		return nil, err
	}
	return &Cache{dir: dir}, nil
}

func (c *Cache) RepoDir(repoURL string) string {
	// Normalize URL for consistent hashing
	cleanURL := repoURL
	// 1. Remove credentials
	if idx := strings.Index(cleanURL, "@"); idx != -1 {
		if protoIdx := strings.Index(cleanURL, "://"); protoIdx != -1 {
			cleanURL = cleanURL[:protoIdx+3] + cleanURL[idx+1:]
		}
	}
	// 2. Remove trailing slashes first
	cleanURL = strings.TrimSuffix(cleanURL, "/")
	// 3. Remove .git suffix
	cleanURL = strings.TrimSuffix(cleanURL, ".git")
	// 4. Lowercase for consistency
	cleanURL = strings.ToLower(cleanURL)

	hash := sha256.Sum256([]byte(cleanURL))
	return filepath.Join(c.dir, hex.EncodeToString(hash[:]))
}

func (c *Cache) authURL(repoURL, token string) string {
	if token == "" {
		return repoURL
	}
	if strings.Contains(repoURL, "github.com") {
		return strings.Replace(repoURL, "https://", fmt.Sprintf("https://x-access-token:%s@", token), 1)
	}
	if strings.Contains(repoURL, "gitlab.com") {
		return strings.Replace(repoURL, "https://", fmt.Sprintf("https://oauth2:%s@", token), 1)
	}
	return repoURL
}

func (c *Cache) Sync(repoURL, token string) error {
	c.mu.Lock()
	defer c.mu.Unlock()

	dir := c.RepoDir(repoURL)
	authURL := c.authURL(repoURL, token)

	if _, err := os.Stat(dir); os.IsNotExist(err) {
		cmd := exec.Command("git", "clone", "--mirror", authURL, dir)
		if output, err := cmd.CombinedOutput(); err != nil {
			return fmt.Errorf("git clone failed: %w, output: %s", err, string(output))
		}
		return nil
	}

	// Update remote URL in case token changed
	cmd := exec.Command("git", "-C", dir, "remote", "set-url", "origin", authURL)
	if output, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("git remote set-url failed: %w, output: %s", err, string(output))
	}

	cmd = exec.Command("git", "-C", dir, "remote", "update")
	if output, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("git remote update failed: %w, output: %s", err, string(output))
	}
	return nil
}

func (c *Cache) ReadFile(repoURL, commit, path string) ([]byte, error) {
	dir := c.RepoDir(repoURL)
	// git show <commit>:<path>
	cmd := exec.Command("git", "-C", dir, "show", fmt.Sprintf("%s:%s", commit, path))
	return cmd.Output()
}

func (c *Cache) WriteArchive(repoURL, commit string, w io.Writer) error {
	dir := c.RepoDir(repoURL)
	if _, err := os.Stat(dir); os.IsNotExist(err) {
		return fmt.Errorf("repository not found in cache: %s", repoURL)
	}

	// Verify commit exists before starting the stream.
	// This allows us to return a 404/500 BEFORE any data is written to the ResponseWriter.
	checkCmd := exec.Command("git", "-C", dir, "rev-parse", "--verify", commit+"^{commit}")
	if err := checkCmd.Run(); err != nil {
		return fmt.Errorf("invalid commit or reference: %s", commit)
	}

	// git archive --format=tar.gz <commit>
	// Using git's built-in gzip support for maximum compatibility and reliability.
	cmd := exec.Command("git", "-C", dir, "archive", "--format=tar.gz", commit)
	cmd.Stdout = w
	var stderr strings.Builder
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("git archive failed: %w, stderr: %s", err, stderr.String())
	}

	return nil
}
