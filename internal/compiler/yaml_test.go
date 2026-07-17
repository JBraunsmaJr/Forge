package compiler

import (
	"encoding/json"
	"reflect"
	"testing"
)

func TestYAMLToJSON_Comments(t *testing.T) {
	yaml := `
name: test-pipeline
steps:
  - id: step1
    always_run: true # this is a comment
    docker_socket: false # with space
    run: echo "hello" # inline comment for run
`
	jsonData, err := yamlToJSON([]byte(yaml))
	if err != nil {
		t.Fatalf("yamlToJSON failed: %v", err)
	}

	var pipe jsonPipeline
	if err := json.Unmarshal(jsonData, &pipe); err != nil {
		t.Fatalf("failed to unmarshal JSON: %v. JSON: %s", err, string(jsonData))
	}

	if len(pipe.Steps) != 1 {
		t.Fatalf("expected 1 step, got %d", len(pipe.Steps))
	}

	step := pipe.Steps[0]
	if !step.AlwaysRun {
		t.Errorf("expected AlwaysRun to be true, got false")
	}
	if step.DockerSocket {
		t.Errorf("expected DockerSocket to be false, got true")
	}
	// Note: currently run will probably contain the comment if it's not a literal block
	// We should decide if that's acceptable.
}

func TestParseScalar_Comments(t *testing.T) {
	tests := []struct {
		input    string `json:"input,omitempty"`
		expected any    `json:"expected,omitempty"`
	}{
		{"true", true},
		{"true # comment", true},
		{"false # comment", false},
		{"yes # comment", true},
		{"no # comment", false},
		{"123 # comment", "123"}, // it stays string for now as per current impl
		{"\"quoted # string\"", "quoted # string"},
		{"'single # quoted'", "single # quoted"},
		{"[a, b] # comment", []any{"a", "b"}},
	}

	for _, tt := range tests {
		got := parseScalar(tt.input)
		if !reflect.DeepEqual(got, tt.expected) {
			t.Errorf("parseScalar(%q) = %v; want %v", tt.input, got, tt.expected)
		}
	}
}
