package glob

import (
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

// Glob supports ** for recursive matching and filters out directories.
// It resolves patterns relative to root.
func Glob(root, pattern string) ([]string, error) {
	// Ensure root is absolute to avoid issues with relative paths
	absRoot, err := filepath.Abs(root)
	if err != nil {
		absRoot = root
	}

	if !strings.Contains(pattern, "**") {
		fullPattern := pattern
		if !filepath.IsAbs(pattern) {
			fullPattern = filepath.Join(absRoot, pattern)
		}
		matches, err := filepath.Glob(fullPattern)
		if err != nil {
			return nil, err
		}
		var files []string
		for _, m := range matches {
			info, err := os.Stat(m)
			if err == nil && !info.IsDir() {
				files = append(files, m)
			}
		}
		return files, nil
	}

	// Recursive glob: split at **
	parts := strings.SplitN(pattern, "**", 2)
	prefix := parts[0]
	suffix := parts[1]

	searchRoot := absRoot
	if prefix != "" {
		if filepath.IsAbs(prefix) {
			searchRoot = prefix
		} else {
			searchRoot = filepath.Join(absRoot, prefix)
		}
	}
	searchRoot = filepath.Clean(searchRoot)

	var matches []string
	err = filepath.WalkDir(searchRoot, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil // skip inaccessible
		}
		if d.IsDir() {
			return nil
		}

		// If no suffix or just a slash, everything matches
		if suffix == "" || suffix == "/" || suffix == "/*" {
			matches = append(matches, path)
			return nil
		}

		// Simplified suffix matching
		rel, err := filepath.Rel(searchRoot, path)
		if err != nil {
			return nil
		}

		// If suffix starts with /, remove it for matching
		cleanSuffix := strings.TrimPrefix(suffix, "/")

		// Use filepath.Match on the filename if suffix is just a file pattern (no slashes)
		if !strings.Contains(cleanSuffix, "/") && !strings.Contains(cleanSuffix, "\\") {
			ok, _ := filepath.Match(cleanSuffix, filepath.Base(path))
			if ok {
				matches = append(matches, path)
			}
			return nil
		}

		// More complex suffix matching: match the end of the relative path
		// We convert to slashes for consistent matching
		relSlash := filepath.ToSlash(rel)
		suffixSlash := filepath.ToSlash(cleanSuffix)

		// If the suffix has no wildcards, simple HasSuffix works
		if !strings.ContainsAny(suffixSlash, "*?[]") {
			if strings.HasSuffix(relSlash, suffixSlash) {
				matches = append(matches, path)
			}
			return nil
		}

		// Fallback: match the last part of the path against the suffix pattern
		// This handles cases like **/src/*.go
		ok, _ := filepath.Match(suffixSlash, relSlash)
		if ok {
			matches = append(matches, path)
		} else {
			// Try to match against the tail of the path
			// e.g. rel="a/b/c.go", suffix="b/*.go"
			// This is not perfect but covers common CI patterns
			parts := strings.Split(relSlash, "/")
			for i := range parts {
				tail := strings.Join(parts[i:], "/")
				if ok, _ := filepath.Match(suffixSlash, tail); ok {
					matches = append(matches, path)
					break
				}
			}
		}

		return nil
	})
	return matches, err
}
