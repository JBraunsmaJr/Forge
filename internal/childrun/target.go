// Package childrun describes a pipeline step that starts the pipeline of
// another Forge project as a child run, and validates what the parent may
// pass to it.
package childrun

import (
	"fmt"
	"regexp"
	"strings"
)

// Keys under which a cross-project pipeline step carries its target in the
// step's parameter map. The map already travels from the compiler through
// the scheduler to the agent, so the step needs no field of its own in the
// wire protocol.
const (
	withProject     = "_forge.pipeline.project"
	withRef         = "_forge.pipeline.ref"
	withFallbackRef = "_forge.pipeline.fallback_ref"
)

// Target names the project a pipeline step starts and the branch or tag of
// that project's repository to run.
type Target struct {
	// Project is the name or ID of a Forge project in the same organization
	// as the parent run.
	Project string
	// Ref is a branch or tag name in the child project's repository. Empty
	// means the same branch or tag the parent run is for.
	Ref string
	// FallbackRef is used when Ref does not exist in the child repository,
	// typically the default branch. Empty means the step fails instead.
	FallbackRef string
}

// projectPattern accepts project names and IDs. It is deliberately loose
// about which characters a name may use, and strict about the ones that
// would matter in a URL path or a log line.
var projectPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._ -]{0,127}$`)

// Validate checks a target as written in a pipeline. Ref and FallbackRef may
// still contain ${{ env.NAME }} placeholders at that point; pass
// allowPlaceholders to accept them, and false once they are resolved.
func (t Target) Validate(allowPlaceholders bool) error {
	if !projectPattern.MatchString(t.Project) {
		return fmt.Errorf("pipeline step: %q is not a valid project name", t.Project)
	}
	for field, ref := range map[string]string{"ref": t.Ref, "fallback_ref": t.FallbackRef} {
		if ref == "" {
			continue
		}
		if allowPlaceholders && strings.Contains(ref, "${{") {
			continue
		}
		if !ValidRefName(ref) {
			return fmt.Errorf("pipeline step: %s %q is not a valid branch or tag name", field, ref)
		}
	}
	return nil
}

// ValidRefName reports whether name can be a Git branch or tag name, in
// short form ("main", "release/1.2", "v1.0.0") or as a full ref under
// refs/heads or refs/tags. It follows the rules of git check-ref-format
// closely enough that an accepted name can be passed to Git as an argument.
func ValidRefName(name string) bool {
	if name == "" || len(name) > 255 {
		return false
	}
	if strings.HasPrefix(name, "-") || strings.HasPrefix(name, "/") || strings.HasSuffix(name, "/") ||
		strings.HasSuffix(name, ".") || strings.HasSuffix(name, ".lock") || name == "@" {
		return false
	}
	if strings.Contains(name, "..") || strings.Contains(name, "//") || strings.Contains(name, "@{") {
		return false
	}
	for _, r := range name {
		if r <= ' ' || r == 0x7f || strings.ContainsRune(`~^:?*[\`, r) {
			return false
		}
	}
	for _, segment := range strings.Split(name, "/") {
		if strings.HasPrefix(segment, ".") {
			return false
		}
	}
	return true
}

// AddTo returns a copy of with that also carries the target. The original
// map is not modified; it may be shared with a template.
func (t Target) AddTo(with map[string]string) map[string]string {
	out := make(map[string]string, len(with)+3)
	for k, v := range with {
		out[k] = v
	}
	out[withProject] = t.Project
	if t.Ref != "" {
		out[withRef] = t.Ref
	}
	if t.FallbackRef != "" {
		out[withFallbackRef] = t.FallbackRef
	}
	return out
}

// FromWith reads the target a step carries. The second result is false for
// a pipeline step that runs a pipeline file from its own workspace.
func FromWith(with map[string]string) (Target, bool) {
	project := with[withProject]
	if project == "" {
		return Target{}, false
	}
	return Target{Project: project, Ref: with[withRef], FallbackRef: with[withFallbackRef]}, true
}
