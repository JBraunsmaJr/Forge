package dockerutil

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
)

// EnvContainerRuntime names the environment variable that selects the
// container CLI explicitly. Its value is a command name or a path, for
// example "podman" or "/usr/local/bin/nerdctl". When it is unset, Forge uses
// the first of docker and podman found on PATH.
const EnvContainerRuntime = "FORGE_CONTAINER_RUNTIME"

// Container CLIs Forge knows how to drive, in order of preference. Podman's
// command line is compatible with Docker's for every command Forge runs.
const (
	runtimeDocker = "docker"
	runtimePodman = "podman"
)

var autodetectOrder = []string{runtimeDocker, runtimePodman}

var (
	runtimeOnce sync.Once
	runtimeName string
)

// Runtime returns the container CLI to execute. The choice is made once per
// process and cached.
func Runtime() string {
	runtimeOnce.Do(func() {
		runtimeName = detectRuntime(os.Getenv, exec.LookPath)
	})
	return runtimeName
}

// detectRuntime picks the container CLI: the explicit override if set, else
// the first known CLI on PATH. When none is installed it returns "docker" so
// that the eventual "executable not found" error names a familiar tool.
func detectRuntime(getenv func(string) string, lookPath func(string) (string, error)) string {
	if override := strings.TrimSpace(getenv(EnvContainerRuntime)); override != "" {
		return override
	}
	for _, name := range autodetectOrder {
		if _, err := lookPath(name); err == nil {
			return name
		}
	}
	return runtimeDocker
}

// IsPodman reports whether the selected container CLI is Podman.
func IsPodman() bool {
	return isPodman(Runtime())
}

func isPodman(runtime string) bool {
	base := strings.ToLower(filepath.Base(runtime))
	return strings.TrimSuffix(base, ".exe") == runtimePodman
}

// Command returns an exec.Cmd that runs the container CLI with args.
func Command(args ...string) *exec.Cmd {
	return exec.Command(Runtime(), args...)
}

// CommandContext is like Command but the process is killed when ctx is done.
func CommandContext(ctx context.Context, args ...string) *exec.Cmd {
	return exec.CommandContext(ctx, Runtime(), args...)
}

// Default API socket locations.
const (
	dockerSocketPath      = "/var/run/docker.sock"
	podmanRootSocketPath  = "/run/podman/podman.sock"
	podmanUserSocketInXDG = "podman/podman.sock"
)

// DefaultSocketPath returns where the selected runtime's API socket is
// expected on this host. For Podman that is the per-user socket when it
// exists (rootless), otherwise the system socket. Callers that honor
// DOCKER_HOST should check it first; this is only the fallback.
//
// Podman serves this socket only when its API service is running, for
// example via `systemctl --user enable --now podman.socket`.
func DefaultSocketPath() string {
	return defaultSocketPath(Runtime(), os.Getenv("XDG_RUNTIME_DIR"), fileExists)
}

func defaultSocketPath(runtime, xdgRuntimeDir string, exists func(string) bool) string {
	if !isPodman(runtime) {
		return dockerSocketPath
	}
	if xdgRuntimeDir != "" {
		if userSocket := filepath.Join(xdgRuntimeDir, podmanUserSocketInXDG); exists(userSocket) {
			return userSocket
		}
	}
	return podmanRootSocketPath
}

func fileExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

// Marker files that container engines create inside a container.
var containerMarkerFiles = []string{
	"/.dockerenv",        // Docker
	"/run/.containerenv", // Podman
}
