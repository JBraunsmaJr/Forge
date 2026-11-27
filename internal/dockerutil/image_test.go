package dockerutil

import "testing"

func TestQualifyImage(t *testing.T) {
	tests := []struct {
		name     string
		image    string
		registry string
		want     string
	}{
		{"official image", "alpine", "docker.io", "docker.io/library/alpine"},
		{"official image with tag", "postgres:16-alpine", "docker.io", "docker.io/library/postgres:16-alpine"},
		{"official image with digest", "alpine@sha256:abc123", "docker.io", "docker.io/library/alpine@sha256:abc123"},
		{"user image", "grafana/grafana:11.0.0", "docker.io", "docker.io/grafana/grafana:11.0.0"},
		{"nested path without a registry", "org/team/app:1", "docker.io", "docker.io/org/team/app:1"},

		{"already has a registry", "ghcr.io/org/app:1", "docker.io", "ghcr.io/org/app:1"},
		{"already has Docker Hub", "docker.io/library/alpine", "docker.io", "docker.io/library/alpine"},
		{"registry with a port", "registry:5000/app:1", "docker.io", "registry:5000/app:1"},
		{"localhost registry", "localhost/app:1", "docker.io", "localhost/app:1"},
		{"localhost registry with a port", "localhost:5000/app", "docker.io", "localhost:5000/app"},

		{"internal registry, single name", "alpine:3.20", "registry.example.com", "registry.example.com/alpine:3.20"},
		{"internal registry, user image", "team/app", "registry.example.com", "registry.example.com/team/app"},
		{"internal registry with a path", "alpine", "registry.example.com/mirror", "registry.example.com/mirror/alpine"},

		{"image ID", "sha256:0123456789abcdef", "docker.io", "sha256:0123456789abcdef"},
		{"empty image", "", "docker.io", ""},
		{"empty registry", "alpine", "", "alpine"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := qualifyImage(tt.image, tt.registry); got != tt.want {
				t.Errorf("qualifyImage(%q, %q) = %q; want %q", tt.image, tt.registry, got, tt.want)
			}
		})
	}
}

func TestDefaultRegistry(t *testing.T) {
	t.Setenv(EnvDefaultRegistry, "")
	if got := defaultRegistry(); got != "docker.io" {
		t.Errorf("defaultRegistry() with nothing set = %q; want docker.io", got)
	}
	t.Setenv(EnvDefaultRegistry, "  registry.example.com/mirror/ ")
	if got := defaultRegistry(); got != "registry.example.com/mirror" {
		t.Errorf("defaultRegistry() = %q; want the trimmed value", got)
	}
}
