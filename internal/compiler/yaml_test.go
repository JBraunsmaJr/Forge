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
}

func TestYAMLToJSON_Integers(t *testing.T) {
	yaml := `
name: test-split
steps:
  - id: step1
    image: alpine
    split:
      shards: 3
      history_days: 14
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
	if step.Split == nil {
		t.Fatal("expected Split to be not nil")
	}
	if step.Split.Shards != 3 {
		t.Errorf("expected shards 3, got %d", step.Split.Shards)
	}
	if step.Split.HistoryDays != 14 {
		t.Errorf("expected history_days 14, got %d", step.Split.HistoryDays)
	}
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
		{"123 # comment", 123},
		{"12.34 # comment", 12.34},
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
