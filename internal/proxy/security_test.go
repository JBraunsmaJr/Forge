package proxy

// Security regression tests for the Docker socket proxy. They track gap S8 in
// docs/Threat-Model.md and guard against it reopening. `make test-security`
// runs them on their own.

import (
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
)

const testAgentID = "agent-1"

// fakeDocker is a stand-in Docker daemon listening on a Unix socket.
type fakeDocker struct {
	socket string
	// mutations counts the non-GET requests that reached the daemon.
	mutations atomic.Int64
}

// startFakeDocker serves a daemon whose GET requests are answered by inspect
// and whose other requests are counted and accepted.
func startFakeDocker(t *testing.T, inspect http.HandlerFunc) *fakeDocker {
	t.Helper()

	// Unix socket paths have a short length limit, so avoid t.TempDir, whose
	// paths include the full test name.
	dir, err := os.MkdirTemp("", "fpx")
	if err != nil {
		t.Fatalf("creating socket directory: %v", err)
	}
	d := &fakeDocker{socket: filepath.Join(dir, "docker.sock")}

	lis, err := net.Listen("unix", d.socket)
	if err != nil {
		t.Fatalf("listening on %s: %v", d.socket, err)
	}

	srv := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet {
			inspect(w, r)
			return
		}
		d.mutations.Add(1)
		w.WriteHeader(http.StatusCreated)
	})}
	go func() { _ = srv.Serve(lis) }()

	t.Cleanup(func() {
		_ = srv.Close()
		_ = os.RemoveAll(dir)
	})
	return d
}

// proxyRequest sends one request through the proxy handler for testAgentID.
func proxyRequest(d *fakeDocker, method, path, body string) *httptest.ResponseRecorder {
	h := NewProxyServer(d.socket, filepath.Dir(d.socket)).Handler(testAgentID)
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

// requireBlocked fails the test unless the proxy answered 403 and the request
// never reached the daemon.
func requireBlocked(t *testing.T, d *fakeDocker, rec *httptest.ResponseRecorder) {
	t.Helper()
	if rec.Code != http.StatusForbidden {
		t.Errorf("proxy answered HTTP %d; want %d", rec.Code, http.StatusForbidden)
	}
	if n := d.mutations.Load(); n != 0 {
		t.Errorf("the request reached the Docker daemon (%d mutating requests forwarded)", n)
	}
}

// TestSecurity_S8_ProxyFailsClosed: when the proxy cannot read a resource's
// labels, it cannot know who owns the resource and must deny the request.
func TestSecurity_S8_ProxyFailsClosed(t *testing.T) {
	d := startFakeDocker(t, func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "daemon error", http.StatusInternalServerError)
	})

	rec := proxyRequest(d, http.MethodDelete, "/v1.45/containers/victim?force=1", "")
	requireBlocked(t, d, rec)
}

// TestSecurity_S8_ProxyRejectsHostEscapes: a container-create request that
// would give the new container control of the host must be denied.
func TestSecurity_S8_ProxyRejectsHostEscapes(t *testing.T) {
	cases := []struct {
		name string
		body string
	}{
		{"privileged", `{"Image":"alpine","HostConfig":{"Privileged":true}}`},
		{"host root bind mount", `{"Image":"alpine","HostConfig":{"Binds":["/:/host"]}}`},
		{"docker socket bind mount", `{"Image":"alpine","HostConfig":{"Binds":["/var/run/docker.sock:/var/run/docker.sock"]}}`},
		{"host PID namespace", `{"Image":"alpine","HostConfig":{"PidMode":"host"}}`},
		{"host network", `{"Image":"alpine","HostConfig":{"NetworkMode":"host"}}`},
		{"added capability", `{"Image":"alpine","HostConfig":{"CapAdd":["SYS_ADMIN"]}}`},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			d := startFakeDocker(t, func(w http.ResponseWriter, r *http.Request) {
				http.NotFound(w, r)
			})
			rec := proxyRequest(d, http.MethodPost, "/v1.45/containers/create", tc.body)
			requireBlocked(t, d, rec)
		})
	}
}
