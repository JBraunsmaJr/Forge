package gitcache

import (
	"bytes"
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
	dir   string
	locks sync.Map // map[string]*sync.Mutex
}

func New(dir string) (*Cache, error) {
	if err := os.MkdirAll(dir, 0755); err != nil {
		return nil, err
	}
	return &Cache{dir: dir}, nil
}

func (c *Cache) getLock(repoURL string) *sync.Mutex {
	l, _ := c.locks.LoadOrStore(repoURL, &sync.Mutex{})
	return l.(*sync.Mutex)
}

func (c *Cache) RepoDir(repoURL string) string {

	// Normalize URL for consistent hashing.
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

	// 4. Lowercase for consistency.
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
	return c.SyncCommit(repoURL, token, "")
}

func (c *Cache) SyncCommit(repoURL, token, commit string) error {
	lock := c.getLock(repoURL)
	lock.Lock()
	defer lock.Unlock()

	dir := c.RepoDir(repoURL)

	if commit != "" {
		if _, err := os.Stat(dir); err == nil {
			// Check if commit already exists
			checkCmd := exec.Command("git", "-C", dir, "rev-parse", "--verify", commit+"^{commit}")
			if err := checkCmd.Run(); err == nil {
				return nil // Already have it
			}
		}
	}

	authURL := c.authURL(repoURL, token)

	if _, err := os.Stat(dir); os.IsNotExist(err) {
		cmd := exec.Command("git", "clone", "--mirror", authURL, dir)
		if output, err := cmd.CombinedOutput(); err != nil {
			return fmt.Errorf("git clone failed: %w, output: %s", err, string(output))
		}
		return nil
	}

	// Update remote URL in case token changed.
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

func (c *Cache) ListBranches(repoURL string) ([]string, string, error) {
	lock := c.getLock(repoURL)
	lock.Lock()
	defer lock.Unlock()

	dir := c.RepoDir(repoURL)
	if _, err := os.Stat(dir); os.IsNotExist(err) {
		return nil, "", fmt.Errorf("repository not found in cache")
	}

	// List remote branches. In a mirror clone, refs/heads/* are mirrored directly.
	cmd := exec.Command("git", "-C", dir, "for-each-ref", "--format=%(refname:short)", "refs/heads/")
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return nil, "", fmt.Errorf("git for-each-ref failed: %w, output: %s", err, stderr.String())
	}

	branches := strings.Split(strings.TrimSpace(stdout.String()), "\n")
	var filtered []string
	for _, b := range branches {
		b = strings.TrimSpace(b)
		if b != "" {
			filtered = append(filtered, b)
		}
	}

	// Try to find the default branch from HEAD symbolic ref.
	headCmd := exec.Command("git", "-C", dir, "symbolic-ref", "--short", "HEAD")
	var hStdout, hStderr bytes.Buffer
	headCmd.Stdout = &hStdout
	headCmd.Stderr = &hStderr
	defaultBranch := "main"
	if err := headCmd.Run(); err == nil {
		defaultBranch = strings.TrimSpace(hStdout.String())
	}

	return filtered, defaultBranch, nil
}

func (c *Cache) ReadFile(repoURL, commit, path string) ([]byte, error) {
	lock := c.getLock(repoURL)
	lock.Lock()
	defer lock.Unlock()

	dir := c.RepoDir(repoURL)

	// git show <commit>:<path>
	cmd := exec.Command("git", "-C", dir, "show", fmt.Sprintf("%s:%s", commit, path))
	return cmd.Output()
}

func (c *Cache) ResolveCommit(repoURL, branch string) (string, error) {
	lock := c.getLock(repoURL)
	lock.Lock()
	defer lock.Unlock()

	dir := c.RepoDir(repoURL)
	if _, err := os.Stat(dir); os.IsNotExist(err) {
		return "", fmt.Errorf("repository not found in cache")
	}

	// Try origin/branch first (mirror clone usually maps refs/heads/* to refs/heads/*)
	cmd := exec.Command("git", "-C", dir, "rev-parse", "origin/"+branch)
	var out, serr bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &serr
	if err := cmd.Run(); err != nil {
		// Fallback to local branch name
		cmd = exec.Command("git", "-C", dir, "rev-parse", branch)
		out.Reset()
		serr.Reset()
		cmd.Stdout = &out
		cmd.Stderr = &serr
		if err := cmd.Run(); err != nil {
			return "", fmt.Errorf("failed to resolve commit for branch %s: %v", branch, serr.String())
		}
	}
	return strings.TrimSpace(out.String()), nil
}

func (c *Cache) Show(repoURL, ref, path string) ([]byte, error) {
	lock := c.getLock(repoURL)
	lock.Lock()
	defer lock.Unlock()

	dir := c.RepoDir(repoURL)
	if _, err := os.Stat(dir); os.IsNotExist(err) {
		return nil, fmt.Errorf("repository not found in cache")
	}

	// Use git show to extract the file from the mirror.
	// We try both as-is and with refs/heads/ prefix if it looks like a branch.
	cmd := exec.Command("git", "-C", dir, "show", ref+":"+path)
	var out, serr bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &serr
	if err := cmd.Run(); err != nil {
		// Try refs/heads/ prefix
		cmd = exec.Command("git", "-C", dir, "show", "refs/heads/"+ref+":"+path)
		out.Reset()
		serr.Reset()
		cmd.Stdout = &out
		cmd.Stderr = &serr
		if err := cmd.Run(); err != nil {
			return nil, fmt.Errorf("git show failed: %w, output: %s", err, serr.String())
		}
	}

	return out.Bytes(), nil
}

func (c *Cache) ResolveRef(repoURL, name string) (string, error) {
	lock := c.getLock(repoURL)
	lock.Lock()
	defer lock.Unlock()

	dir := c.RepoDir(repoURL)
	if _, err := os.Stat(dir); os.IsNotExist(err) {
		return "", fmt.Errorf("repository not found in cache")
	}

	// Try full ref first
	if strings.HasPrefix(name, "refs/") {
		cmd := exec.Command("git", "-C", dir, "show-ref", "--verify", name)
		if err := cmd.Run(); err == nil {
			return name, nil
		}
	}

	// Try heads
	cmd := exec.Command("git", "-C", dir, "show-ref", "--verify", "refs/heads/"+name)
	if err := cmd.Run(); err == nil {
		return "refs/heads/" + name, nil
	}

	// Try tags
	cmd = exec.Command("git", "-C", dir, "show-ref", "--verify", "refs/tags/"+name)
	if err := cmd.Run(); err == nil {
		return "refs/tags/" + name, nil
	}

	return "", fmt.Errorf("ref %s not found", name)
}

func (c *Cache) WriteArchive(repoURL, commit string, w io.Writer) error {
	lock := c.getLock(repoURL)
	lock.Lock()
	defer lock.Unlock()

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

	/*
			git archive --format=tar.gz <commit>
		    Using git's built-in gzip support for maximum compatibility and reliability.
	*/
	cmd := exec.Command("git", "-C", dir, "archive", "--format=tar.gz", commit)
	cmd.Stdout = w
	var stderr strings.Builder
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("git archive failed: %w, stderr: %s", err, stderr.String())
	}

	return nil
}
