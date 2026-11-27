// Package changes decides which pipeline steps can be skipped because the
// files they declare an interest in, with `changes:`, were not touched by
// the commits a run is for.
package changes

import (
	"fmt"
	"path"
	"strings"
)

// Match reports whether file, a slash-separated path relative to the
// repository root, matches pattern.
//
// Patterns are anchored at the repository root and matched one path segment
// at a time:
//
//   - "*", "?", and "[...]" match within a single segment, as in path.Match.
//   - "**" matches any number of segments, including none.
//   - A pattern that ends in "/" matches everything below that directory.
//
// So "*.md" matches README.md but not docs/guide.md, "**/*.md" matches both,
// and "services/api/" matches every file under services/api.
func Match(pattern, file string) bool {
	if strings.HasSuffix(pattern, "/") {
		pattern += "**"
	}
	return matchSegments(strings.Split(pattern, "/"), strings.Split(file, "/"))
}

func matchSegments(pattern, file []string) bool {
	for len(pattern) > 0 {
		if pattern[0] == "**" {
			// Collapse consecutive "**" and try every possible split.
			for len(pattern) > 0 && pattern[0] == "**" {
				pattern = pattern[1:]
			}
			if len(pattern) == 0 {
				return true
			}
			for i := 0; i <= len(file); i++ {
				if matchSegments(pattern, file[i:]) {
					return true
				}
			}
			return false
		}
		if len(file) == 0 {
			return false
		}
		if ok, err := path.Match(pattern[0], file[0]); err != nil || !ok {
			return false
		}
		pattern, file = pattern[1:], file[1:]
	}
	return len(file) == 0
}

// ValidatePattern reports why pattern cannot be used in `changes:`, or nil.
func ValidatePattern(pattern string) error {
	switch {
	case strings.TrimSpace(pattern) == "":
		return fmt.Errorf("changes: a pattern is empty")
	case pattern != strings.TrimSpace(pattern):
		return fmt.Errorf("changes: pattern %q has leading or trailing spaces", pattern)
	case strings.HasPrefix(pattern, "/"):
		return fmt.Errorf("changes: pattern %q must be relative to the repository root, without a leading slash", pattern)
	case strings.Contains(pattern, `\`):
		return fmt.Errorf("changes: pattern %q must use forward slashes", pattern)
	}
	for _, segment := range strings.Split(strings.TrimSuffix(pattern, "/"), "/") {
		switch {
		case segment == "":
			return fmt.Errorf("changes: pattern %q has an empty path segment", pattern)
		case segment == "." || segment == "..":
			return fmt.Errorf("changes: pattern %q must not contain %q segments", pattern, segment)
		case segment != "**" && strings.Contains(segment, "**"):
			return fmt.Errorf("changes: in pattern %q, \"**\" must be a whole path segment (write \"**/*.go\", not \"**.go\")", pattern)
		}
		if _, err := path.Match(segment, ""); err != nil {
			return fmt.Errorf("changes: pattern %q is malformed", pattern)
		}
	}
	return nil
}

// ValidatePatterns validates every pattern and returns the first problem.
func ValidatePatterns(patterns []string) error {
	for _, pattern := range patterns {
		if err := ValidatePattern(pattern); err != nil {
			return err
		}
	}
	return nil
}
