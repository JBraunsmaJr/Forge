// Package imagebuild turns a docker_build step's configuration into the
// container CLI commands that build an image and push it to a registry.
//
// The package only validates input and constructs commands; it runs nothing.
// That keeps the rules for what a pipeline may ask for in one place that can
// be tested without a container runtime.
package imagebuild

import (
	"errors"
	"fmt"
	"path"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

// Config is the configuration of one docker_build step.
type Config struct {
	// Registry is the registry host, with a port if needed, for example
	// "registry.example.com" or "localhost:5000".
	Registry string
	// Repository is the image path within the registry, for example
	// "team/api".
	Repository string
	// Context is the build context directory, relative to the workspace.
	// Empty means the workspace root.
	Context string
	// Dockerfile is the path of the Dockerfile, relative to the workspace.
	// Empty means "Dockerfile" inside the context.
	Dockerfile string
	// Target names a build stage to stop at. Empty builds the last stage.
	Target string
	// Tags are the tags the built image is pushed under. At least one.
	Tags []string
	// BuildArgs are passed to the build as --build-arg NAME=value.
	BuildArgs map[string]string
}

var (
	// registryPattern accepts a host name or IP with an optional port.
	registryPattern = regexp.MustCompile(`^[A-Za-z0-9]([A-Za-z0-9.-]*[A-Za-z0-9])?(:[0-9]{1,5})?$`)

	// repositoryPattern follows the registry rules for an image path:
	// lowercase components separated by "/", each made of letters and digits
	// joined by ".", "_", "__" or runs of "-".
	repositoryPattern = regexp.MustCompile(`^[a-z0-9]+((\.|_|__|-+)[a-z0-9]+)*(/[a-z0-9]+((\.|_|__|-+)[a-z0-9]+)*)*$`)

	// tagPattern is the registry rule for a tag.
	tagPattern = regexp.MustCompile(`^[A-Za-z0-9_][A-Za-z0-9_.-]{0,127}$`)

	// buildArgNamePattern is a shell-style variable name.
	buildArgNamePattern = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

	// targetPattern is a build stage name.
	targetPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.-]*$`)
)

// Validate reports the first problem with the configuration, or nil. Values
// reach a command line, so anything that is not plainly a registry, an image
// path, a tag, or a path inside the workspace is refused.
func (c Config) Validate() error {
	if c.Registry == "" {
		return errors.New("docker_build.registry is required")
	}
	if !registryPattern.MatchString(c.Registry) {
		return fmt.Errorf("docker_build.registry %q is not a registry host", c.Registry)
	}
	if c.Repository == "" {
		return errors.New("docker_build.repository is required")
	}
	if !repositoryPattern.MatchString(c.Repository) {
		return fmt.Errorf("docker_build.repository %q is not a valid image path (lowercase letters, digits, and . _ - separated by /)", c.Repository)
	}
	if len(c.Tags) == 0 {
		return errors.New("docker_build.tags needs at least one tag")
	}
	seen := make(map[string]bool, len(c.Tags))
	for _, tag := range c.Tags {
		if !tagPattern.MatchString(tag) {
			return fmt.Errorf("docker_build.tags: %q is not a valid tag", tag)
		}
		if seen[tag] {
			return fmt.Errorf("docker_build.tags: %q is listed twice", tag)
		}
		seen[tag] = true
	}
	if c.Target != "" && !targetPattern.MatchString(c.Target) {
		return fmt.Errorf("docker_build.target %q is not a valid stage name", c.Target)
	}
	for name := range c.BuildArgs {
		if !buildArgNamePattern.MatchString(name) {
			return fmt.Errorf("docker_build.build_args: %q is not a valid argument name", name)
		}
	}
	if err := checkWorkspacePath("context", c.Context); err != nil {
		return err
	}
	return checkWorkspacePath("dockerfile", c.Dockerfile)
}

// checkWorkspacePath refuses a path that is absolute or that climbs out of
// the workspace. The check is on the path as written, with forward slashes,
// because pipeline files are portable across operating systems.
func checkWorkspacePath(field, value string) error {
	if value == "" {
		return nil
	}
	slashed := strings.ReplaceAll(value, `\`, "/")
	if strings.HasPrefix(slashed, "/") || (len(slashed) > 1 && slashed[1] == ':') {
		return fmt.Errorf("docker_build.%s %q must be relative to the workspace", field, value)
	}
	cleaned := path.Clean(slashed)
	if cleaned == ".." || strings.HasPrefix(cleaned, "../") {
		return fmt.Errorf("docker_build.%s %q must stay inside the workspace", field, value)
	}
	return nil
}

// Reference returns the full image reference for a tag:
// "<registry>/<repository>:<tag>".
func (c Config) Reference(tag string) string {
	return c.Registry + "/" + c.Repository + ":" + tag
}

// References returns the full image reference for every configured tag, in
// the order the tags are listed.
func (c Config) References() []string {
	refs := make([]string, 0, len(c.Tags))
	for _, tag := range c.Tags {
		refs = append(refs, c.Reference(tag))
	}
	return refs
}

// ContextDir returns the absolute build context directory for a workspace.
func (c Config) ContextDir(workspaceDir string) string {
	return filepath.Join(workspaceDir, filepath.FromSlash(c.Context))
}

// DockerfilePath returns the absolute Dockerfile path for a workspace:
// the configured path, or "Dockerfile" in the context when none is set.
func (c Config) DockerfilePath(workspaceDir string) string {
	if c.Dockerfile == "" {
		return filepath.Join(c.ContextDir(workspaceDir), "Dockerfile")
	}
	return filepath.Join(workspaceDir, filepath.FromSlash(c.Dockerfile))
}

// sortedBuildArgs returns the build arguments as NAME=value strings in name
// order, so the same configuration always yields the same command.
func (c Config) sortedBuildArgs() []string {
	names := make([]string, 0, len(c.BuildArgs))
	for name := range c.BuildArgs {
		names = append(names, name)
	}
	sort.Strings(names)
	args := make([]string, 0, len(names))
	for _, name := range names {
		args = append(args, name+"="+c.BuildArgs[name])
	}
	return args
}
