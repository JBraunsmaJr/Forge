package changes

// Set is the list of files changed by the commits a run is for. The zero
// value is an unknown set, which is what a run has when there is nothing to
// compare against: a manual run, the first push of a branch, or a history
// rewrite that removed the previous commit.
type Set struct {
	known bool
	files []string
}

// Unknown returns a set for a run whose changed files cannot be determined.
func Unknown() Set {
	return Set{}
}

// Of returns the set of the given changed files, which may be empty.
func Of(files []string) Set {
	return Set{known: true, files: files}
}

// Known reports whether the changed files could be determined.
func (s Set) Known() bool {
	return s.known
}

// Len returns the number of changed files in a known set.
func (s Set) Len() int {
	return len(s.files)
}

// Touches reports whether any changed file matches any of patterns. An
// unknown set touches everything: when Forge cannot tell what changed, the
// step runs.
func (s Set) Touches(patterns []string) bool {
	if !s.known {
		return true
	}
	for _, file := range s.files {
		for _, pattern := range patterns {
			if Match(pattern, file) {
				return true
			}
		}
	}
	return false
}
