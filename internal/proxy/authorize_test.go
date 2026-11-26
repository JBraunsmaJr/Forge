package proxy

import (
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// labelDaemon is a stand-in Docker daemon that answers inspect requests from
// a fixed table and records whether any other request reached it.
type labelDaemon struct {
	socket    string
	forwarded []string // "METHOD path" of each non-inspect request, in order
	lastBody  string
}

// startLabelDaemon serves a daemon in which each named container exists with
// the given labels. Inspecting "broken" yields a server error.
func startLabelDaemon(t *testing.T, containers map[string]map[string]string) *labelDaemon {
	t.Helper()

	dir, err := os.MkdirTemp("", "fpa")
	if err != nil {
		t.Fatalf("creating socket directory: %v", err)
	}
	d := &labelDaemon{socket: filepath.Join(dir, "docker.sock")}

	lis, err := net.Listen("unix", d.socket)
	if err != nil {
		t.Fatalf("listening on %s: %v", d.socket, err)
	}

	srv := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		const prefix, suffix = "/containers/", "/json"
		if r.Method == http.MethodGet && strings.HasPrefix(r.URL.Path, prefix) && strings.HasSuffix(r.URL.Path, suffix) {
			name := strings.TrimSuffix(strings.TrimPrefix(r.URL.Path, prefix), suffix)
			if name == "broken" {
				http.Error(w, "daemon error", http.StatusInternalServerError)
				return
			}
			labels, ok := containers[name]
			if !ok {
				http.NotFound(w, r)
				return
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"Config": map[string]any{"Labels": labels}})
			return
		}
		body, _ := io.ReadAll(r.Body)
		d.forwarded = append(d.forwarded, r.Method+" "+r.URL.Path)
		d.lastBody = string(body)
		w.WriteHeader(http.StatusNoContent)
	})}
	go func() { _ = srv.Serve(lis) }()

	t.Cleanup(func() {
		_ = srv.Close()
		_ = os.RemoveAll(dir)
	})
	return d
}

func TestAuthorize(t *testing.T) {
	const agentID = "agent-1"
	mine := map[string]string{labelManaged: "true", labelAgentID: agentID}

	containers := map[string]map[string]string{
		"mine":      mine,
		"my-shard":  {labelManaged: "true", labelAgentID: agentID + "-shard-2"},
		"theirs":    {labelManaged: "true", labelAgentID: "agent-2"},
		"lookalike": {labelManaged: "true", labelAgentID: "agent-10"},
		"unmanaged": {labelAgentID: agentID},
		"unlabeled": {},
	}

	tests := []struct {
		name        string
		method      string
		path        string
		body        string
		wantForward bool
	}{
		{"own container", http.MethodDelete, "/v1.45/containers/mine", "", true},
		{"own shard's container", http.MethodDelete, "/containers/my-shard", "", true},
		{"another agent's container", http.MethodDelete, "/containers/theirs", "", false},
		{"agent ID that only shares a prefix", http.MethodDelete, "/containers/lookalike", "", false},
		{"container without the managed label", http.MethodDelete, "/containers/unmanaged", "", false},
		{"container with no labels", http.MethodPost, "/containers/unlabeled/stop", "", false},
		{"container whose labels cannot be read", http.MethodDelete, "/containers/broken", "", false},
		{"container that does not exist", http.MethodDelete, "/containers/missing", "", true},
		{"prune", http.MethodPost, "/containers/prune", "", false},
		{"plugin API", http.MethodPost, "/plugins/pull", "", false},
		{"swarm API", http.MethodPost, "/v1.45/swarm/init", "", false},
		{"image pull", http.MethodPost, "/images/create", "", true},
		{"safe create", http.MethodPost, "/containers/create", `{"Image":"alpine"}`, true},
		{"privileged create", http.MethodPost, "/containers/create", `{"HostConfig":{"Privileged":true}}`, false},
		{"create that is not JSON", http.MethodPost, "/containers/create", `not json`, false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			d := startLabelDaemon(t, containers)
			h := NewProxyServer(d.socket, filepath.Dir(d.socket)).Handler(agentID)

			req := httptest.NewRequest(tt.method, tt.path, strings.NewReader(tt.body))
			rec := httptest.NewRecorder()
			h.ServeHTTP(rec, req)

			forwarded := len(d.forwarded) > 0
			if forwarded != tt.wantForward {
				t.Errorf("forwarded = %v (HTTP %d); want %v", forwarded, rec.Code, tt.wantForward)
			}
			if !tt.wantForward && rec.Code != http.StatusForbidden {
				t.Errorf("status = %d; want %d", rec.Code, http.StatusForbidden)
			}
			if tt.wantForward && tt.body != "" && d.lastBody != tt.body {
				t.Errorf("daemon received body %q; want the original %q", d.lastBody, tt.body)
			}
		})
	}
}
