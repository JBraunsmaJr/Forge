package gitcache

import (
	"errors"
	"strings"
)

// ErrRefNotFound is returned when a branch or tag is not in the cached
// repository.
var ErrRefNotFound = errors.New("branch or tag not found")

// ResolveBranchOrTag finds the branch or tag called name and returns its
// full ref ("refs/heads/main", "refs/tags/v1.0.0") and the commit it points
// at. A branch wins over a tag of the same name. name may also be a full ref
// under refs/heads or refs/tags.
//
// The caller validates name; it is only ever passed to Git as part of a
// full ref name, never as a bare argument.
func (c *Cache) ResolveBranchOrTag(repoURL, name string) (ref, commit string, err error) {
	var candidates []string
	switch {
	case strings.HasPrefix(name, "refs/heads/"), strings.HasPrefix(name, "refs/tags/"):
		candidates = []string{name}
	case name == "" || strings.HasPrefix(name, "refs/"):
		return "", "", ErrRefNotFound
	default:
		candidates = []string{"refs/heads/" + name, "refs/tags/" + name}
	}

	for _, candidate := range candidates {
		// ^{commit} peels an annotated tag to the commit it marks.
		out, gitErr := c.git(repoURL, "rev-parse", "--verify", "--quiet", candidate+"^{commit}")
		if gitErr == nil {
			return candidate, strings.TrimSpace(string(out)), nil
		}
	}
	return "", "", ErrRefNotFound
}
