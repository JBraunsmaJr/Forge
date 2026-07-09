package compiler

import (
	"os"
	"strings"
	"testing"
)

func TestYAMLBasicPipeline(t *testing.T) {
	yaml := `
name: my-pipeline
steps:
  - id: lint
    name: Lint code
    image: golang:1.24-alpine
    run: go vet ./...
    timeout: 5m
`
	p, err := compileYAML(t, yaml)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if p.Name != "my-pipeline" {
		t.Errorf("expected name 'my-pipeline', got %q", p.Name)
	}
	if len(p.Steps) != 1 {
		t.Fatalf("expected 1 step, got %d", len(p.Steps))
	}
	step := p.Steps[0]
	if step.ID != "lint" {
		t.Errorf("expected step id 'lint', got %q", step.ID)
	}
	if step.Image != "golang:1.24-alpine" {
		t.Errorf("expected image 'golang:1.24-alpine', got %q", step.Image)
	}

	if len(step.Command) != 3 || step.Command[0] != "sh" {
		t.Errorf("expected sh -c command, got %v", step.Command)
	}
}

func TestYAMLLiteralBlock(t *testing.T) {
	yaml := `
name: multi-line
steps:
  - id: build
    image: alpine:latest
    run: |
      echo first
      echo second
      echo third
`
	p, err := compileYAML(t, yaml)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	cmd := strings.Join(p.Steps[0].Command, " ")
	if !strings.Contains(cmd, "echo first") || !strings.Contains(cmd, "echo second") {
		t.Errorf("expected multi-line run in command, got %q", cmd)
	}
}

func TestYAMLDependsOnInline(t *testing.T) {
	yaml := `
name: dag
steps:
  - id: a
    image: alpine:latest
    run: echo a
  - id: b
    image: alpine:latest
    run: echo b
    depends_on: [a]
`
	p, err := compileYAML(t, yaml)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(p.Steps[1].DependsOn) != 1 || p.Steps[1].DependsOn[0] != "a" {
		t.Errorf("expected DependsOn=[a], got %v", p.Steps[1].DependsOn)
	}
}

func TestYAMLSecrets(t *testing.T) {
	yaml := `
name: secrets-test
steps:
  - id: deploy
    image: alpine:latest
    run: echo deploying
    secrets: [GITHUB_TOKEN, NPM_TOKEN]
`
	p, err := compileYAML(t, yaml)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(p.Steps[0].Secrets) != 2 {
		t.Errorf("expected 2 secrets, got %v", p.Steps[0].Secrets)
	}
}

func TestYAMLEnvMap(t *testing.T) {
	yaml := `
name: env-test
steps:
  - id: build
    image: alpine:latest
    run: echo $CGO_ENABLED
    env:
      CGO_ENABLED: "0"
      GOOS: linux
`
	p, err := compileYAML(t, yaml)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if p.Steps[0].Env["CGO_ENABLED"] != "0" {
		t.Errorf("expected CGO_ENABLED=0, got %q", p.Steps[0].Env["CGO_ENABLED"])
	}
}

func TestYAMLDockerSocket(t *testing.T) {
	yaml := `
name: dood
steps:
  - id: build-image
    image: docker:27-cli
    run: docker build -t myapp .
    docker_socket: true
`
	p, err := compileYAML(t, yaml)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !p.Steps[0].DockerSocket {
		t.Error("expected DockerSocket=true")
	}
}

func compileYAML(t *testing.T, content string) (*struct {
	Name  string
	Steps []stepSummary
}, error) {
	t.Helper()

	path := t.TempDir() + "/pipeline.yml"
	if err := writeFile(path, []byte(content)); err != nil {
		return nil, err
	}

	p, err := Compile(path)
	if err != nil {
		return nil, err
	}

	result := &struct {
		Name  string
		Steps []stepSummary
	}{Name: p.Name}
	for _, s := range p.Steps {
		result.Steps = append(result.Steps, stepSummary{
			ID:           s.ID,
			Image:        s.Image,
			Command:      s.Command,
			DependsOn:    s.DependsOn,
			Env:          s.Env,
			Secrets:      s.Secrets,
			DockerSocket: s.DockerSocket,
		})
	}
	return result, nil
}

type stepSummary struct {
	ID           string
	Image        string
	Command      []string
	DependsOn    []string
	Env          map[string]string
	Secrets      []string
	DockerSocket bool
}

func writeFile(path string, data []byte) error {
	f, err := os.Create(path)
	if err != nil {
		return err
	}
	defer f.Close()
	_, err = f.Write(data)
	return err
}
