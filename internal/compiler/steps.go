package compiler

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"sort"
	"strconv"
	"strings"
)

// StepResolver resolves a non-local uses reference into ordinary pipeline
// steps. Keeping this interface small makes registry access replaceable for
// private registries and keeps compilation deterministic in tests.
type StepResolver interface {
	Resolve(JSONStep) ([]JSONStep, error)
}

// HTTPStepResolver reads a registry.yml and the referenced step.yml over HTTP.
// RegistryURL should point at the registry checkout (for example the main
// branch of a raw GitHub repository).
type HTTPStepResolver struct {
	RegistryURL string
	Client      *http.Client
}

func NewHTTPStepResolver(registryURL string) *HTTPStepResolver {
	return &HTTPStepResolver{RegistryURL: strings.TrimRight(registryURL, "/"), Client: http.DefaultClient}
}

func NewHTTPStepResolverFromEnv() *HTTPStepResolver {
	url := os.Getenv("FORGE_STEP_REGISTRY_URL")
	if url == "" {
		url = "https://raw.githubusercontent.com/forge-steps/community/main"
	}
	return NewHTTPStepResolver(url)
}

type registryFile struct {
	Version int                      `json:"version"`
	Steps   map[string]registryEntry `json:"steps"`
}

type registryEntry struct {
	Latest   string            `json:"latest"`
	Versions map[string]string `json:"versions"`
	Inputs   []registryInput   `json:"inputs"`
}

type registryInput struct {
	Name     string `json:"name"`
	Required bool   `json:"required"`
	Default  any    `json:"default"`
}

func (r *HTTPStepResolver) get(url string) ([]byte, error) {
	client := r.Client
	if client == nil {
		client = http.DefaultClient
	}
	resp, err := client.Get(url)
	if err != nil {
		return nil, fmt.Errorf("fetching %s: %w", url, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("fetching %s: HTTP %s", url, resp.Status)
	}
	b, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if err != nil {
		return nil, fmt.Errorf("reading %s: %w", url, err)
	}
	return b, nil
}

func (r *HTTPStepResolver) Resolve(js JSONStep) ([]JSONStep, error) {
	if js.With == nil {
		js.With = make(map[string]string)
	}
	parts := strings.SplitN(js.Uses, "@", 2)
	if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
		return nil, fmt.Errorf("invalid step reference %q (expected registry/step@version)", js.Uses)
	}
	name := parts[0]
	ref := parts[1]
	path := strings.SplitN(name, "/", 2)
	if len(path) != 2 || path[0] == "" || path[1] == "" {
		return nil, fmt.Errorf("invalid step reference %q (expected registry/step@version)", js.Uses)
	}
	// The first component identifies the registry. The default public registry
	// is also accepted under the historical forge-community name.
	if path[0] != "forge-steps" && path[0] != "forge-community" && path[0] != "internal" {
		return nil, fmt.Errorf("unknown step registry %q", path[0])
	}

	registryData, err := r.get(r.RegistryURL + "/registry.yml")
	if err != nil {
		return nil, err
	}
	registryJSON, err := yamlToJSON(registryData)
	if err != nil {
		return nil, fmt.Errorf("parsing registry.yml: %w", err)
	}
	var registry registryFile
	if err := json.Unmarshal(registryJSON, &registry); err != nil {
		return nil, fmt.Errorf("parsing registry.yml: %w", err)
	}
	entry, ok := registry.Steps[path[1]]
	if !ok {
		return nil, fmt.Errorf("step %q is not published in the registry", path[1])
	}

	if ref == "latest" {
		ref = entry.Latest
	}
	expected, ok := entry.Versions[ref]
	if !ok {
		// A major reference (for example @v2) is intentionally supported as
		// a convenience alias. Pick the greatest published v2.x version.
		var candidates []string
		for version := range entry.Versions {
			if strings.HasPrefix(version, ref+".") {
				candidates = append(candidates, version)
			}
		}
		if len(candidates) > 0 {
			sort.Slice(candidates, func(i, j int) bool { return versionParts(candidates[i]) > versionParts(candidates[j]) })
			ref = candidates[0]
			expected, ok = entry.Versions[ref]
		}
	}
	if !ok {
		return nil, fmt.Errorf("step %s has no published version %q", path[1], ref)
	}
	stepBase := r.RegistryURL
	// The default public registry uses raw GitHub URLs. Fetch the immutable
	// version ref rather than the branch used for registry.yml. Custom mirrors
	// can expose versioned content from their configured base and are left
	// untouched.
	if strings.Contains(stepBase, "raw.githubusercontent.com/") {
		segments := strings.Split(stepBase, "/")
		if len(segments) > 0 {
			segments[len(segments)-1] = ref
			stepBase = strings.Join(segments, "/")
		}
	}
	stepURL := stepBase + "/steps/" + path[1] + "/step.yml"
	stepData, err := r.get(stepURL)
	if err != nil {
		return nil, err
	}
	actual := sha256.Sum256(stepData)
	want := strings.TrimPrefix(strings.ToLower(strings.TrimSpace(expected)), "sha256:")
	if hex.EncodeToString(actual[:]) != want {
		return nil, fmt.Errorf("step %s@%s failed SHA-256 verification", name, ref)
	}
	if err := validateStepInputs(stepData, js.With, name); err != nil {
		return nil, err
	}

	for _, in := range entry.Inputs {
		if _, supplied := js.With[in.Name]; supplied {
			continue
		}
		if in.Default != nil {
			if value, ok := in.Default.(string); ok {
				js.With[in.Name] = value
			} else {
				js.With[in.Name] = fmt.Sprint(in.Default)
			}
		} else if in.Required {
			return nil, fmt.Errorf("step %s requires input %q", name, in.Name)
		}
	}
	// step_id is deliberately expanded before template parsing; it is the
	// stable caller-visible namespace for every injected step.
	template := strings.ReplaceAll(string(stepData), "${{ step_id }}", js.ID)
	return ResolveTemplateData(js, []byte(template), "step.yml")
}

func versionParts(v string) string {
	v = strings.TrimPrefix(v, "v")
	parts := strings.Split(v, ".")
	for len(parts) < 3 {
		parts = append(parts, "0")
	}
	var out strings.Builder
	for _, part := range parts[:3] {
		n, _ := strconv.Atoi(part)
		fmt.Fprintf(&out, "%08d", n)
	}
	return out.String()
}

func validateStepInputs(data []byte, supplied map[string]string, stepName string) error {
	jsonData, err := yamlToJSON(data)
	if err != nil {
		return fmt.Errorf("parsing step %s: %w", stepName, err)
	}
	var raw struct {
		Inputs map[string]struct {
			Type     string `json:"type"`
			Required bool   `json:"required"`
			Default  any    `json:"default"`
			Enum     []any  `json:"enum"`
		} `json:"inputs"`
	}
	if err := json.Unmarshal(jsonData, &raw); err != nil {
		return fmt.Errorf("parsing step %s: %w", stepName, err)
	}
	for name, input := range raw.Inputs {
		value, ok := supplied[name]
		if !ok && input.Default != nil {
			value, ok = fmt.Sprint(input.Default), true
			supplied[name] = value
		}
		if input.Required && (!ok || value == "") {
			return fmt.Errorf("step %s requires input %q", stepName, name)
		}
		if !ok || len(input.Enum) == 0 {
			continue
		}
		valid := false
		for _, candidate := range input.Enum {
			if fmt.Sprint(candidate) == value {
				valid = true
				break
			}
		}
		if !valid {
			return fmt.Errorf("step %s input %q must be one of the declared enum values", stepName, name)
		}
	}
	return nil
}
