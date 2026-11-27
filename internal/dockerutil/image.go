package dockerutil

import (
	"os"
	"strings"
)

// EnvDefaultRegistry names the environment variable that sets the registry
// assumed for image names that do not include one, such as "alpine:3.20".
const EnvDefaultRegistry = "FORGE_DEFAULT_REGISTRY"

const (
	dockerHubRegistry = "docker.io"
	// dockerHubOfficialNamespace is where Docker Hub keeps single-word
	// images: "alpine" is really "docker.io/library/alpine".
	dockerHubOfficialNamespace = "library"
)

// ResolveImage returns the image reference to hand to the container CLI.
//
// Docker assumes Docker Hub for a name with no registry. Podman does not: it
// consults a configured search list, and fails when none is configured. So
// that the same pipeline works under both, Forge spells out the registry
// itself when the CLI is Podman. Under Docker the name is passed through
// unchanged, which keeps image listings and caches as they were.
func ResolveImage(image string) string {
	if !IsPodman() {
		return image
	}
	return qualifyImage(image, defaultRegistry())
}

// defaultRegistry returns FORGE_DEFAULT_REGISTRY, or Docker Hub when unset.
func defaultRegistry() string {
	if registry := strings.TrimSpace(os.Getenv(EnvDefaultRegistry)); registry != "" {
		return strings.TrimSuffix(registry, "/")
	}
	return dockerHubRegistry
}

// qualifyImage adds registry to an image reference that names none, following
// Docker's own rule for telling the two apart: the part before the first "/"
// is a registry if it contains a "." or a ":" or is "localhost". References
// by image ID are returned unchanged.
func qualifyImage(image, registry string) string {
	if image == "" || registry == "" || strings.HasPrefix(image, "sha256:") {
		return image
	}

	first, _, hasPath := strings.Cut(image, "/")
	if !hasPath {
		// A single name such as "alpine:3.20".
		if registry == dockerHubRegistry {
			return registry + "/" + dockerHubOfficialNamespace + "/" + image
		}
		return registry + "/" + image
	}
	if first == "localhost" || strings.ContainsAny(first, ".:") {
		return image
	}
	return registry + "/" + image
}
