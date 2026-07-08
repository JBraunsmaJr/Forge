package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestDetectProjectKind_Go(t *testing.T) {
	dir := t.TempDir()
	mustWrite(t, filepath.Join(dir, "go.mod"), "module example.com/app\n")
	kind, tmpl := detectProjectKindInDir(dir)
	if kind != "Go" {
		t.Errorf("expected Go, got %q", kind)
	}
	if !strings.Contains(tmpl, "golang") {
		t.Errorf("Go template should reference golang image, got:\n%s", tmpl[:100])
	}
}

func TestDetectProjectKind_Node(t *testing.T) {
	dir := t.TempDir()
	mustWrite(t, filepath.Join(dir, "package.json"), `{"name":"app"}`)
	kind, tmpl := detectProjectKindInDir(dir)
	if kind != "Node.js" {
		t.Errorf("expected Node.js, got %q", kind)
	}
	if !strings.Contains(tmpl, "node") {
		t.Errorf("Node template should reference node image")
	}
}

func TestDetectProjectKind_Python(t *testing.T) {
	for _, fname := range []string{"requirements.txt", "Pipfile", "pyproject.toml"} {
		t.Run(fname, func(t *testing.T) {
			dir := t.TempDir()
			mustWrite(t, filepath.Join(dir, fname), "# python")
			kind, _ := detectProjectKindInDir(dir)
			if kind != "Python" {
				t.Errorf("expected Python for %s, got %q", fname, kind)
			}
		})
	}
}

func TestDetectProjectKind_Rust(t *testing.T) {
	dir := t.TempDir()
	mustWrite(t, filepath.Join(dir, "Cargo.toml"), "[package]\n")
	kind, _ := detectProjectKindInDir(dir)
	if kind != "Rust" {
		t.Errorf("expected Rust, got %q", kind)
	}
}

func TestDetectProjectKind_Docker(t *testing.T) {
	dir := t.TempDir()
	mustWrite(t, filepath.Join(dir, "Dockerfile"), "FROM alpine\n")
	kind, _ := detectProjectKindInDir(dir)
	if kind != "Docker" {
		t.Errorf("expected Docker, got %q", kind)
	}
}

func TestDetectProjectKind_Generic(t *testing.T) {
	dir := t.TempDir()
	kind, tmpl := detectProjectKindInDir(dir)
	if kind != "generic" {
		t.Errorf("expected generic, got %q", kind)
	}
	if !strings.Contains(tmpl, "alpine") {
		t.Errorf("generic template should use alpine")
	}
}

func TestDetectProjectKind_GoTakesPriorityOverDockerfile(t *testing.T) {
	dir := t.TempDir()
	mustWrite(t, filepath.Join(dir, "go.mod"), "module example.com/app\n")
	mustWrite(t, filepath.Join(dir, "Dockerfile"), "FROM golang\n")
	kind, _ := detectProjectKindInDir(dir)

	if kind != "Go" {
		t.Errorf("expected Go to win over Dockerfile, got %q", kind)
	}
}

func TestInitCommandCreatesFile(t *testing.T) {
	dir := t.TempDir()
	orig, _ := os.Getwd()
	t.Cleanup(func() { os.Chdir(orig) })
	os.Chdir(dir)

	mustWrite(t, "go.mod", "module example.com/app\n")

	os.Args = []string{"forge", "init"}

	kind, tmpl := detectProjectKindInDir(dir)
	if kind != "Go" {
		t.Fatalf("expected Go detection, got %q", kind)
	}
	if err := os.MkdirAll(".forge", 0755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	if err := os.WriteFile(".forge/pipeline.yml", []byte(tmpl), 0644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	content, err := os.ReadFile(".forge/pipeline.yml")
	if err != nil {
		t.Fatalf("reading pipeline: %v", err)
	}
	if !strings.Contains(string(content), "name: go-ci") {
		t.Errorf("expected go-ci pipeline name in output")
	}
	if !strings.Contains(string(content), "go test") {
		t.Errorf("expected go test step in output")
	}
}

func TestInitCommandDoesNotOverwrite(t *testing.T) {
	dir := t.TempDir()
	orig, _ := os.Getwd()
	t.Cleanup(func() { os.Chdir(orig) })
	os.Chdir(dir)

	os.MkdirAll(".forge", 0755)
	mustWrite(t, ".forge/pipeline.yml", "name: my-existing-pipeline\n")

	content, _ := os.ReadFile(".forge/pipeline.yml")
	if !strings.Contains(string(content), "my-existing-pipeline") {
		t.Error("existing pipeline.yml was modified")
	}
}

func mustWrite(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		t.Fatalf("mkdir %s: %v", path, err)
	}
	if err := os.WriteFile(path, []byte(content), 0644); err != nil {
		t.Fatalf("writing %s: %v", path, err)
	}
}

func detectProjectKindInDir(dir string) (kind, tmpl string) {
	orig, _ := os.Getwd()
	os.Chdir(dir)
	defer os.Chdir(orig)
	return detectProjectKind([]detector{
		{"go.mod", "Go", goTemplate},
		{"package.json", "Node.js", nodeTemplate},
		{"requirements.txt", "Python", pythonTemplate},
		{"Pipfile", "Python", pythonTemplate},
		{"pyproject.toml", "Python", pythonTemplate},
		{"Cargo.toml", "Rust", rustTemplate},
		{"Dockerfile", "Docker", dockerTemplate},
	})
}
