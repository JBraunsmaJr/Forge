package compiler

import (
	"encoding/json"
	"testing"
)

func TestYAMLParser_QuotedMapKey(t *testing.T) {
	data := []byte(`
versions:
  "1.0.0": "sha256:deadbeef"
`)
	j, err := yamlToJSON(data)
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]any
	json.Unmarshal(j, &m)
	versions := m["versions"].(map[string]any)
	for k := range versions {
		t.Logf("key=%q", k)
	}
	if _, ok := versions["1.0.0"]; !ok {
		t.Errorf("expected unquoted key '1.0.0', got keys: %v", keysOf(versions))
	}
}

func keysOf(m map[string]any) []string {
	var out []string
	for k := range m {
		out = append(out, k)
	}
	return out
}
