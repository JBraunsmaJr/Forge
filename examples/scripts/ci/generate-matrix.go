package main

import (
	"encoding/json"
	"fmt"
	"os"
)

// Platform defines a build target
type Platform struct {
	OS    string `json:"os"`
	Arch  string `json:"arch"`
	Image string `json:"image"`
}

// Step defines a Forge pipeline step
type Step struct {
	ID        string            `json:"id"`
	Image     string            `json:"image"`
	DependsOn []string          `json:"depends_on,omitempty"`
	Timeout   string            `json:"timeout,omitempty"`
	Env       map[string]string `json:"env,omitempty"`
	Run       string            `json:"run"`
}

// GenerateMatrix contains the core logic, making it testable.
func GenerateMatrix(platforms []Platform) []Step {
	steps := []Step{}
	for _, p := range platforms {
		steps = append(steps, Step{
			ID:    fmt.Sprintf("build-%s-%s", p.OS, p.Arch),
			Image: p.Image,
			Env: map[string]string{
				"GOOS":   p.OS,
				"GOARCH": p.Arch,
			},
			Run: fmt.Sprintf("go build -o dist/myapp-%s-%s ./cmd/myapp", p.OS, p.Arch),
		})
	}
	return steps
}

func main() {
	// 1. Inputs
	// Read generator context from stdin (optional fallback)
	var input struct {
		PipelineName string            `json:"pipeline_name"`
		Ref          string            `json:"ref"`
		Env          map[string]string `json:"env"`
	}
	dec := json.NewDecoder(os.Stdin)
	if err := dec.Decode(&input); err == nil {
		fmt.Fprintf(os.Stderr, "[info] Read generator context from stdin (pipeline: %s)\n", input.PipelineName)
	}

	platforms := []Platform{
		{"linux", "amd64", "golang:1.26-alpine"},
		{"linux", "arm64", "golang:1.26-alpine"},
		{"windows", "amd64", "golang:1.26-alpine"},
	}

	// 2. Logic
	steps := GenerateMatrix(platforms)

	// 3. Output
	data, err := json.MarshalIndent(steps, "", "  ")
	if err != nil {
		fmt.Fprintf(os.Stderr, "failed to marshal steps: %v\n", err)
		os.Exit(1)
	}
	fmt.Println(string(data))
}
