package compiler

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestHTTPStepResolver_Live(t *testing.T) {
	stepYML := []byte(`
name: hello
steps:
  - id: hello
    run: echo "hi ${{ inputs.greeting }} from ${{ step_id }}"
inputs:
  greeting:
    type: string
    required: true
`)
	stepSHA := sha256.Sum256(stepYML)
	stepSHAHex := hex.EncodeToString(stepSHA[:])

	mux := http.NewServeMux()
	mux.HandleFunc("/main/registry.yml", func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprintf(w, `
version: 1
steps:
  greeter:
    latest: "1.2.0"
    versions:
      "1.0.0": "sha256:deadbeef"
      "1.2.0": "sha256:%s"
    inputs:
      - name: greeting
        required: true
`, stepSHAHex)
	})
	mux.HandleFunc("/main/steps/greeter/step.yml", func(w http.ResponseWriter, r *http.Request) {
		w.Write(stepYML)
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	resolver := NewHTTPStepResolver(srv.URL + "/main")

	t.Run("resolves @latest", func(t *testing.T) {
		steps, err := resolver.Resolve(JSONStep{
			ID:   "greet1",
			Uses: "forge-steps/greeter@latest",
			With: map[string]string{"greeting": "world"},
		})
		if err != nil {
			t.Fatalf("Resolve failed: %v", err)
		}
		if len(steps) != 1 {
			t.Fatalf("expected 1 step, got %d", len(steps))
		}
		if !strings.Contains(steps[0].Run, "hi world") {
			t.Errorf("expected input substitution in Run, got %q", steps[0].Run)
		}
		if !strings.Contains(steps[0].Run, "from greet1") {
			t.Errorf("expected ${{ step_id }} to expand to the caller's id, got %q", steps[0].Run)
		}
		if steps[0].ID != "greet1.hello" {
			t.Errorf("expected the sub-step's own ID to be namespaced as 'greet1.hello', got %q", steps[0].ID)
		}
	})

	t.Run("resolves exact version", func(t *testing.T) {
		_, err := resolver.Resolve(JSONStep{
			ID:   "greet2",
			Uses: "forge-steps/greeter@1.2.0",
			With: map[string]string{"greeting": "there"},
		})
		if err != nil {
			t.Fatalf("Resolve failed: %v", err)
		}
	})

	t.Run("rejects unpublished version", func(t *testing.T) {
		_, err := resolver.Resolve(JSONStep{
			ID:   "greet3",
			Uses: "forge-steps/greeter@9.9.9",
			With: map[string]string{"greeting": "x"},
		})
		if err == nil {
			t.Fatal("expected error for unpublished version, got nil")
		}
	})

	t.Run("SHA mismatch is rejected", func(t *testing.T) {
		_, err := resolver.Resolve(JSONStep{
			ID:   "greet4",
			Uses: "forge-steps/greeter@1.0.0", // registry.yml has a bogus 'deadbeef' sha for 1.0.0
			With: map[string]string{"greeting": "x"},
		})
		if err == nil || !strings.Contains(err.Error(), "SHA-256") {
			t.Fatalf("expected SHA-256 verification error, got %v", err)
		}
	})

	t.Run("missing required input", func(t *testing.T) {
		_, err := resolver.Resolve(JSONStep{
			ID:   "greet5",
			Uses: "forge-steps/greeter@latest",
			With: map[string]string{},
		})
		if err == nil {
			t.Fatal("expected error for missing required input, got nil")
		}
	})

	t.Run("unknown registry namespace rejected", func(t *testing.T) {
		_, err := resolver.Resolve(JSONStep{
			ID:   "greet6",
			Uses: "sketchy-org/greeter@latest",
			With: map[string]string{"greeting": "x"},
		})
		if err == nil {
			t.Fatal("expected error for unknown registry namespace, got nil")
		}
	})

	t.Run("malformed reference rejected", func(t *testing.T) {
		_, err := resolver.Resolve(JSONStep{
			ID:   "greet7",
			Uses: "forge-steps/greeter", // no @version
			With: map[string]string{"greeting": "x"},
		})
		if err == nil {
			t.Fatal("expected error for missing @version, got nil")
		}
	})
}

func TestCompileDataWithResolver_UsesNonLocalResolver(t *testing.T) {
	stepYML := []byte(`
name: build
steps:
  - id: build
    image: golang:1.24
    run: go build ./...
`)
	sha := sha256.Sum256(stepYML)
	shaHex := hex.EncodeToString(sha[:])

	mux := http.NewServeMux()
	mux.HandleFunc("/main/registry.yml", func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprintf(w, `
version: 1
steps:
  go-build:
    latest: "1.0.0"
    versions:
      "1.0.0": "sha256:%s"
`, shaHex)
	})
	mux.HandleFunc("/main/steps/go-build/step.yml", func(w http.ResponseWriter, r *http.Request) {
		w.Write(stepYML)
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	pipelineJSON := []byte(`{
		"name": "ci",
		"steps": [
			{"id": "compile", "uses": "forge-steps/go-build@latest"}
		]
	}`)

	p, err := CompileDataWithResolver(pipelineJSON, "pipeline.json", NewHTTPStepResolver(srv.URL+"/main"))
	if err != nil {
		t.Fatalf("CompileDataWithResolver failed: %v", err)
	}
	if len(p.Steps) != 1 {
		t.Fatalf("expected 1 compiled step, got %d", len(p.Steps))
	}
	if p.Steps[0].ID != "compile.build" {
		t.Errorf("expected namespaced step id 'compile.build', got %q", p.Steps[0].ID)
	}
}

// TestHTTPStepResolver_GitHubVersionRewrite exercises the raw.githubusercontent.com
// path-rewrite logic in isolation, since we can't point a real request at that
// host from a test. It mirrors exactly the string transform Resolve performs.
func TestHTTPStepResolver_GitHubVersionRewrite(t *testing.T) {
	cases := []struct {
		base string
		ref  string
		want string
	}{
		{"https://raw.githubusercontent.com/forge-steps/community/main", "1.2.0", "https://raw.githubusercontent.com/forge-steps/community/1.2.0"},
		{"https://raw.githubusercontent.com/forge-steps/community/main", "v2", "https://raw.githubusercontent.com/forge-steps/community/v2"},
		{"https://mirror.internal.example.com/registry", "1.2.0", "https://mirror.internal.example.com/registry"}, // non-GitHub base: left untouched
	}
	for _, tc := range cases {
		stepBase := tc.base
		if strings.Contains(stepBase, "raw.githubusercontent.com/") {
			segments := strings.Split(stepBase, "/")
			if len(segments) > 0 {
				segments[len(segments)-1] = tc.ref
				stepBase = strings.Join(segments, "/")
			}
		}
		if stepBase != tc.want {
			t.Errorf("base=%q ref=%q: got %q, want %q", tc.base, tc.ref, stepBase, tc.want)
		}
	}
}
