package proxy

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestFilterContainerList_ErrorResponse(t *testing.T) {
	s := NewProxyServer("/tmp/docker.sock", "/tmp/proxy")

	// Mock an error response from Docker (e.g. 404 Not Found with JSON object body)
	resp := &http.Response{
		StatusCode: http.StatusNotFound,
		Body:       io.NopCloser(bytes.NewReader([]byte(`{"message": "not found"}`))),
		Header:     make(http.Header),
		Request:    httptest.NewRequest("GET", "/containers/json", nil),
	}

	// This should NOT fail because we now check status code.
	err := s.filterContainerList("agent-1", resp)
	if err != nil {
		t.Fatalf("Expected no error from filterContainerList for non-200 status, got: %v", err)
	}
}

func TestFilterContainerList_Success(t *testing.T) {
	s := NewProxyServer("/tmp/docker.sock", "/tmp/proxy")

	containers := []map[string]any{
		{
			"Id": "c1",
			"Labels": map[string]any{
				"forge.agent_id": "agent-1",
			},
		},
		{
			"Id": "c2",
			"Labels": map[string]any{
				"forge.agent_id": "agent-2",
			},
		},
		{
			"Id": "c3",
			"Labels": map[string]any{
				"forge.agent_id": "agent-1-shard-1",
			},
		},
	}
	body, _ := json.Marshal(containers)

	resp := &http.Response{
		StatusCode: http.StatusOK,
		Body:       io.NopCloser(bytes.NewReader(body)),
		Header:     make(http.Header),
		Request:    httptest.NewRequest("GET", "/containers/json", nil),
	}

	err := s.filterContainerList("agent-1", resp)
	if err != nil {
		t.Fatalf("filterContainerList failed: %v", err)
	}

	newBody, _ := io.ReadAll(resp.Body)
	var filtered []map[string]any
	json.Unmarshal(newBody, &filtered)

	if len(filtered) != 2 {
		t.Errorf("expected 2 containers, got %d", len(filtered))
	}
	if filtered[0]["Id"] != "c1" && filtered[1]["Id"] != "c1" {
		t.Errorf("expected container c1 to be present")
	}
	if filtered[0]["Id"] != "c3" && filtered[1]["Id"] != "c3" {
		t.Errorf("expected container c3 to be present")
	}
}
