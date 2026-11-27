package gitcache

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"regexp"
	"strings"
)

// ErrNoBase is returned when the changed files cannot be computed because
// there is nothing valid to compare against: the push created the branch,
// or the base commit or branch is not in the cache (for example after a
// history rewrite).
var ErrNoBase = errors.New("no base to compare against")

var (
	commitIDPattern = regexp.MustCompile(`^[0-9a-fA-F]{7,64}$`)
	zeroIDPattern   = regexp.MustCompile(`^0+$`)
)

// ChangedSince returns the files that differ between two commits, as
// slash-separated paths relative to the repository root. It is the right
// comparison for a push: base is the commit the branch pointed at before.
//
// A renamed file is reported under both its old and its new path.
func (c *Cache) ChangedSince(repoURL, base, head string) ([]string, error) {
	if !commitIDPattern.MatchString(head) {
		return nil, fmt.Errorf("head %q is not a commit ID", head)
	}
	if !commitIDPattern.MatchString(base) || zeroIDPattern.MatchString(base) {
		return nil, ErrNoBase
	}
	return c.diffNames(repoURL, base, base, head)
}

// ChangedFromBranch returns the files a commit changes relative to the point
// where its history left the given branch. It is the right comparison for a
// pull or merge request: changes that landed on the target branch in the
// meantime are not counted.
func (c *Cache) ChangedFromBranch(repoURL, branch, head string) ([]string, error) {
	if !commitIDPattern.MatchString(head) {
		return nil, fmt.Errorf("head %q is not a commit ID", head)
	}
	if branch == "" || strings.HasPrefix(branch, "-") || strings.ContainsAny(branch, " \t\n\\:~^?*[") || strings.Contains(branch, "..") {
		return nil, ErrNoBase
	}
	// The cache is a mirror, so branches are under refs/heads. The full ref
	// name also keeps the argument from ever being read as an option.
	branchRef := "refs/heads/" + branch

	mergeBase, err := c.git(repoURL, "merge-base", branchRef, head)
	if err != nil {
		return nil, ErrNoBase
	}
	return c.diffNames(repoURL, branchRef, strings.TrimSpace(string(mergeBase)), head)
}

// diffNames lists the files that differ between from and head, after
// checking that baseRef and head exist in the cache.
func (c *Cache) diffNames(repoURL, baseRef, from, head string) ([]string, error) {
	for _, rev := range []string{baseRef, head} {
		if _, err := c.git(repoURL, "rev-parse", "--verify", "--quiet", rev+"^{commit}"); err != nil {
			if rev == head {
				return nil, fmt.Errorf("commit %s is not in the cache", head)
			}
			return nil, ErrNoBase
		}
	}

	out, err := c.git(repoURL, "diff", "--name-only", "--no-renames", "-z", from, head, "--")
	if err != nil {
		return nil, fmt.Errorf("listing changed files: %w", err)
	}

	var files []string
	for _, name := range bytes.Split(out, []byte{0}) {
		if len(name) > 0 {
			files = append(files, string(name))
		}
	}
	return files, nil
}

// git runs a git command in the cached repository and returns its output.
func (c *Cache) git(repoURL string, args ...string) ([]byte, error) {
	lock := c.getLock(repoURL)
	lock.Lock()
	defer lock.Unlock()

	dir := c.RepoDir(repoURL)
	if _, err := os.Stat(dir); err != nil {
		return nil, fmt.Errorf("repository not found in cache")
	}

	cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("git %s: %w: %s", args[0], err, strings.TrimSpace(stderr.String()))
	}
	return out, nil
}
