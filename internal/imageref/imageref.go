// Package imageref validates the container image references that pipeline
// steps run in, and resolves the ${{ env.NAME }} placeholders they may
// contain.
//
// A placeholder is what lets a step run an image that an earlier step in
// the same run built and pushed under a per-run tag, for example
// "registry.example.com/team/api:test-${{ env.FORGE_BUILD_NUMBER }}".
package imageref

import (
	"fmt"
	"regexp"
	"strings"

	"github.com/JBraunsmaJr/forge/internal/placeholder"
)

// maxLength bounds a reference. Registries limit names to 255 characters and
// tags to 128; a digest adds 72.
const maxLength = 512

// The parts of a reference: [host[:port]/]path[:tag][@sha256:digest].
//
// The grammar is deliberately a little wider than the registry
// specification (it accepts uppercase letters in the path, which engines
// reject on their own) so that it never refuses a reference an engine would
// run. What it must guarantee is that a reference cannot be mistaken for a
// command-line option or carry shell or whitespace characters.
const (
	component = `[A-Za-z0-9]+(?:[._-]+[A-Za-z0-9]+)*`
	port      = `(?::[0-9]{1,5})?`
	tag       = `(?::[A-Za-z0-9_][A-Za-z0-9_.-]{0,127})?`
	digest    = `(?:@sha256:[a-f0-9]{64})?`
)

var (
	referencePattern = regexp.MustCompile(`^` + component + port + `(?:/` + component + `)*` + tag + digest + `$`)
	pinnedPattern    = regexp.MustCompile(`(?::[^/:@]+|@sha256:[a-f0-9]{64})$`)
)

// Validate reports whether ref is a well-formed image reference with no
// placeholders left in it.
func Validate(ref string) error {
	switch {
	case ref == "":
		return fmt.Errorf("image reference is empty")
	case len(ref) > maxLength:
		return fmt.Errorf("image reference is longer than %d characters", maxLength)
	case !referencePattern.MatchString(ref):
		return fmt.Errorf("invalid image name: %q", ref)
	}
	return nil
}

// ValidateTemplate checks a reference as written in a pipeline, where it may
// still contain ${{ env.NAME }} placeholders. Each placeholder is treated as
// standing for ordinary name characters; the resolved reference is checked
// in full by Resolve when the step runs.
func ValidateTemplate(ref string) error {
	if !placeholder.HasEnv(ref) {
		return Validate(ref)
	}
	if err := Validate(placeholder.ReplaceEnv(ref, "x")); err != nil {
		return fmt.Errorf("invalid image name: %q", ref)
	}
	return nil
}

// Resolve expands the placeholders in ref from env and validates the result.
// A reference without placeholders is returned unchanged and unchecked: it
// was validated when the pipeline was compiled, and step types that run no
// container carry an internal marker here instead of a reference.
func Resolve(ref string, env map[string]string) (string, error) {
	if !placeholder.HasEnv(ref) {
		return ref, nil
	}
	resolved := placeholder.ExpandEnv(ref, env)
	if missing := placeholder.EnvNames(resolved); len(missing) > 0 {
		return "", fmt.Errorf("image %q refers to %s, which this step does not have",
			ref, strings.Join(missing, ", "))
	}
	if err := Validate(resolved); err != nil {
		return "", fmt.Errorf("image %q resolves to %q, which is not a valid image reference", ref, resolved)
	}
	return resolved, nil
}

// Pinned reports whether ref names a specific tag other than "latest", or a
// digest. A reference with a placeholder in its tag counts as pinned: it is
// resolved to a specific tag when the step runs.
func Pinned(ref string) bool {
	ref = placeholder.ReplaceEnv(ref, "x")
	return pinnedPattern.MatchString(ref) && !strings.HasSuffix(ref, ":latest")
}
