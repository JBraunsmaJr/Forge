package imagebuild

import (
	"encoding/json"
	"fmt"

	"github.com/JBraunsmaJr/forge/internal/placeholder"
)

// StepType is the pipeline step type that builds and pushes an image.
const StepType = "docker_build"

// WithKey is the key under which a docker_build step's configuration travels
// in the step's generic parameter map, from the compiler through the
// scheduler to the agent. Using the existing map means the step needs no
// field of its own in the wire protocol or the database.
const WithKey = "_forge.docker_build"

// Encode serializes the configuration for transport under WithKey.
func (c Config) Encode() (string, error) {
	data, err := json.Marshal(c)
	if err != nil {
		return "", fmt.Errorf("encoding docker_build configuration: %w", err)
	}
	return string(data), nil
}

// Decode reads a configuration written by Encode.
func Decode(encoded string) (Config, error) {
	var c Config
	if encoded == "" {
		return c, fmt.Errorf("docker_build configuration is missing")
	}
	if err := json.Unmarshal([]byte(encoded), &c); err != nil {
		return c, fmt.Errorf("decoding docker_build configuration: %w", err)
	}
	return c, nil
}

// ExpandEnv returns a copy of the configuration with every ${{ env.NAME }}
// placeholder replaced by the value of NAME in env. A placeholder that names
// a variable not in env is left as written, so that Validate reports it
// instead of the build silently using an empty value.
func (c Config) ExpandEnv(env map[string]string) Config {
	expand := func(value string) string { return placeholder.ExpandEnv(value, env) }

	out := Config{
		Registry:   expand(c.Registry),
		Repository: expand(c.Repository),
		Context:    expand(c.Context),
		Dockerfile: expand(c.Dockerfile),
		Target:     expand(c.Target),
	}
	if c.Tags != nil {
		out.Tags = make([]string, len(c.Tags))
		for i, tag := range c.Tags {
			out.Tags[i] = expand(tag)
		}
	}
	if c.BuildArgs != nil {
		out.BuildArgs = make(map[string]string, len(c.BuildArgs))
		for name, value := range c.BuildArgs {
			out.BuildArgs[name] = expand(value)
		}
	}
	return out
}
