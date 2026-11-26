package dockerutil

import (
	"errors"
	"testing"
)

func TestDetectRuntime(t *testing.T) {
	installed := func(names ...string) func(string) (string, error) {
		return func(name string) (string, error) {
			for _, n := range names {
				if n == name {
					return "/usr/bin/" + name, nil
				}
			}
			return "", errors.New("not found")
		}
	}
	env := func(value string) func(string) string {
		return func(key string) string {
			if key == EnvContainerRuntime {
				return value
			}
			return ""
		}
	}

	tests := []struct {
		name     string
		override string
		lookPath func(string) (string, error)
		want     string
	}{
		{"docker only", "", installed("docker"), "docker"},
		{"podman only", "", installed("podman"), "podman"},
		{"both installed prefers docker", "", installed("docker", "podman"), "docker"},
		{"neither installed falls back to docker", "", installed(), "docker"},
		{"override wins over autodetect", "podman", installed("docker", "podman"), "podman"},
		{"override may be a path", "/opt/bin/nerdctl", installed("docker"), "/opt/bin/nerdctl"},
		{"override is trimmed", "  podman  ", installed("docker"), "podman"},
		{"blank override is ignored", "   ", installed("podman"), "podman"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := detectRuntime(env(tt.override), tt.lookPath); got != tt.want {
				t.Errorf("detectRuntime() = %q; want %q", got, tt.want)
			}
		})
	}
}

func TestIsPodman(t *testing.T) {
	tests := []struct {
		runtime string
		want    bool
	}{
		{"podman", true},
		{"/usr/bin/podman", true},
		{"Podman.EXE", true},
		{"docker", false},
		{"/usr/local/bin/nerdctl", false},
		{"podman-remote", false},
	}
	for _, tt := range tests {
		if got := isPodman(tt.runtime); got != tt.want {
			t.Errorf("isPodman(%q) = %v; want %v", tt.runtime, got, tt.want)
		}
	}
}

func TestDefaultSocketPath(t *testing.T) {
	const userSocket = "/run/user/1000/podman/podman.sock"
	present := func(paths ...string) func(string) bool {
		return func(p string) bool {
			for _, candidate := range paths {
				if candidate == p {
					return true
				}
			}
			return false
		}
	}

	tests := []struct {
		name    string
		runtime string
		xdg     string
		exists  func(string) bool
		want    string
	}{
		{"docker", "docker", "/run/user/1000", present(userSocket), dockerSocketPath},
		{"rootless podman with a user socket", "podman", "/run/user/1000", present(userSocket), userSocket},
		{"podman without a user socket", "podman", "/run/user/1000", present(), podmanRootSocketPath},
		{"podman without XDG_RUNTIME_DIR", "podman", "", present(userSocket), podmanRootSocketPath},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := defaultSocketPath(tt.runtime, tt.xdg, tt.exists); got != tt.want {
				t.Errorf("defaultSocketPath() = %q; want %q", got, tt.want)
			}
		})
	}
}
