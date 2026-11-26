package proxy

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
)

// ProxyServer gives each agent its own Docker API socket and forwards only
// the requests that agent is allowed to make. See authorize for the rules.
type ProxyServer struct {
	DockerSocket string
	SocketDir    string
	// AllowedBinds lists host paths that jobs may bind-mount, in addition to
	// the agent's own proxy socket. Each path allows itself and everything
	// beneath it.
	AllowedBinds []string

	mu        sync.Mutex
	listeners map[string]net.Listener
}

// NewProxyServer returns a proxy in front of dockerSocket that creates agent
// sockets in socketDir. AllowedBinds is read from FORGE_PROXY_ALLOWED_BINDS.
func NewProxyServer(dockerSocket, socketDir string) *ProxyServer {
	return &ProxyServer{
		DockerSocket: dockerSocket,
		SocketDir:    socketDir,
		AllowedBinds: allowedBindsFromEnv(),
		listeners:    make(map[string]net.Listener),
	}
}

// agentSocketPath returns the path of the socket the proxy serves for agentID.
func (s *ProxyServer) agentSocketPath(agentID string) string {
	return filepath.Join(s.SocketDir, "agent-"+agentID+".sock")
}

// RestoreRegistrations recreates listeners for every agent socket file
// already present in SocketDir. The proxy's registration state lives only
// in memory, but the socket files live in a shared volume — so after a
// proxy restart (redeploy, crash) agents were left pointing at socket
// files nobody was listening on, failing with "is the docker daemon
// running" until the agent itself restarted. The socket filename encodes
// the agent ID (agent-<id>.sock), so state can be rebuilt without any
// agent involvement.
func (s *ProxyServer) RestoreRegistrations() {
	entries, err := os.ReadDir(s.SocketDir)
	if err != nil {
		return // dir may not exist yet; Register creates it on demand
	}
	for _, e := range entries {
		name := e.Name()
		if !strings.HasPrefix(name, "agent-") || !strings.HasSuffix(name, ".sock") {
			continue
		}
		agentID := strings.TrimSuffix(strings.TrimPrefix(name, "agent-"), ".sock")
		if agentID == "" {
			continue
		}
		if _, err := s.Register(agentID); err != nil {
			fmt.Printf("[proxy] failed to restore socket for agent %s: %v\n", agentID, err)
		} else {
			fmt.Printf("[proxy] restored socket for agent %s\n", agentID)
		}
	}
}

func (s *ProxyServer) Register(agentID string) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	socketPath := s.agentSocketPath(agentID)
	if _, ok := s.listeners[agentID]; ok {
		return socketPath, nil
	}

	if err := os.MkdirAll(s.SocketDir, 0755); err != nil {
		return "", err
	}

	_ = os.Remove(socketPath)

	l, err := net.Listen("unix", socketPath)
	if err != nil {
		return "", err
	}
	s.listeners[agentID] = l

	handler := s.Handler(agentID)
	go http.Serve(l, handler)

	return socketPath, nil
}

func (s *ProxyServer) Handler(agentID string) http.Handler {
	u, _ := url.Parse("http://docker")
	proxy := httputil.NewSingleHostReverseProxy(u)
	proxy.Transport = &http.Transport{
		DialContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
			return net.Dial("unix", s.DockerSocket)
		},
	}

	originalDirector := proxy.Director
	proxy.Director = func(req *http.Request) {
		originalDirector(req)
		req.URL.Scheme = "http"
		req.URL.Host = "docker"
	}

	proxy.ModifyResponse = func(resp *http.Response) error {
		path := resp.Request.URL.Path
		resource, id := s.parsePath(path)

		if resp.StatusCode == http.StatusOK && resp.Request.Method == "GET" && resource == "containers" && (id == "json" || id == "") {
			return s.filterContainerList(agentID, resp)
		}
		return nil
	}

	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if reason := s.authorize(agentID, r); reason != "" {
			http.Error(w, "forbidden: "+reason, http.StatusForbidden)
			return
		}
		proxy.ServeHTTP(w, r)
	})
}

func (s *ProxyServer) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path == "/register" && r.Method == "POST" {
		var req struct {
			AgentID string `json:"agent_id"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		socketPath, err := s.Register(req.AgentID)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]string{"socket_path": socketPath})
		return
	}
	http.NotFound(w, r)
}

func (s *ProxyServer) parsePath(path string) (resource, id string) {
	parts := strings.Split(strings.TrimPrefix(path, "/"), "/")
	if len(parts) > 0 && strings.HasPrefix(parts[0], "v") {
		parts = parts[1:]
	}
	if len(parts) > 0 {
		resource = parts[0]
	}
	if len(parts) > 1 {
		id = parts[1]
	}
	return
}

func (s *ProxyServer) filterContainerList(agentID string, resp *http.Response) error {
	if resp.StatusCode != http.StatusOK {
		return nil
	}
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return err
	}
	resp.Body.Close()

	var containers []map[string]any
	if err := json.Unmarshal(body, &containers); err != nil {
		return err
	}

	filtered := make([]map[string]any, 0)
	for _, c := range containers {
		labels, _ := c["Labels"].(map[string]any)
		if labels != nil {
			lID, _ := labels["forge.agent_id"].(string)
			if lID == agentID || strings.HasPrefix(lID, agentID+"-") {
				filtered = append(filtered, c)
			}
		}
	}

	newBody, _ := json.Marshal(filtered)
	resp.Body = io.NopCloser(bytes.NewReader(newBody))
	resp.ContentLength = int64(len(newBody))
	resp.Header.Set("Content-Length", fmt.Sprint(len(newBody)))

	return nil
}

func (s *ProxyServer) Shutdown() {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, l := range s.listeners {
		l.Close()
	}
}
