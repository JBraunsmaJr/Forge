package imagebuild

import (
	"encoding/base64"
	"encoding/json"
	"path/filepath"
	"regexp"
)

// CLI identifies the container command line the build runs with. Docker and
// Podman accept the same build and push commands but take registry
// credentials, and report the pushed digest, in different ways.
type CLI int

const (
	Docker CLI = iota
	Podman
)

// AuthFileName is the name of the credentials file inside the directory
// passed to BuildCommand and PushCommand. Docker requires this exact name.
const AuthFileName = "config.json"

// AuthFile returns the content of a credentials file that logs in to one
// registry. Docker and Podman read the same format. Passing credentials in a
// file keeps them off the command line, where other processes on the host
// could read them, and out of the user's own login state.
func AuthFile(registry, username, password string) ([]byte, error) {
	type entry struct {
		Auth string `json:"auth"`
	}
	credentials := base64.StdEncoding.EncodeToString([]byte(username + ":" + password))
	return json.MarshalIndent(map[string]map[string]entry{
		"auths": {registry: {Auth: credentials}},
	}, "", "  ")
}

// BuildCommand returns the arguments, after the CLI's own name, that build
// the image and tag it with every configured reference.
//
// authDir is a directory holding AuthFileName, used to pull private base
// images; pass "" to build without credentials. workspaceDir is the root the
// configuration's paths are relative to.
func BuildCommand(cli CLI, cfg Config, workspaceDir, authDir string) []string {
	args := globalAuthArgs(cli, authDir)
	args = append(args, "build")
	args = append(args, commandAuthArgs(cli, authDir)...)
	args = append(args, "--file", cfg.DockerfilePath(workspaceDir))
	if cfg.Target != "" {
		args = append(args, "--target", cfg.Target)
	}
	for _, buildArg := range cfg.sortedBuildArgs() {
		args = append(args, "--build-arg", buildArg)
	}
	for _, ref := range cfg.References() {
		args = append(args, "--tag", ref)
	}
	return append(args, cfg.ContextDir(workspaceDir))
}

// PushCommand returns the arguments that push one image reference.
//
// digestFile is where Podman is asked to write the pushed image's digest;
// it is ignored for Docker, whose digest is read from the command's output
// with ParsePushDigest. Pass "" to skip it.
func PushCommand(cli CLI, reference, authDir, digestFile string) []string {
	args := globalAuthArgs(cli, authDir)
	args = append(args, "push")
	args = append(args, commandAuthArgs(cli, authDir)...)
	if cli == Podman && digestFile != "" {
		args = append(args, "--digestfile", digestFile)
	}
	return append(args, reference)
}

// globalAuthArgs returns the credential flags that go before the subcommand.
// Docker takes a configuration directory there.
func globalAuthArgs(cli CLI, authDir string) []string {
	if cli == Docker && authDir != "" {
		return []string{"--config", authDir}
	}
	return nil
}

// commandAuthArgs returns the credential flags that go after the subcommand.
// Podman takes the credentials file there.
func commandAuthArgs(cli CLI, authDir string) []string {
	if cli == Podman && authDir != "" {
		return []string{"--authfile", filepath.Join(authDir, AuthFileName)}
	}
	return nil
}

// digestPattern matches an image digest.
var digestPattern = regexp.MustCompile(`sha256:[a-f0-9]{64}`)

// ParsePushDigest extracts the image digest from the output of a push, or
// from the content of a Podman digest file. It returns "" when there is none.
// When several digests appear, the last one is the image's: Docker prints
// layer digests first and the manifest digest on its final line.
func ParsePushDigest(output string) string {
	matches := digestPattern.FindAllString(output, -1)
	if len(matches) == 0 {
		return ""
	}
	return matches[len(matches)-1]
}
