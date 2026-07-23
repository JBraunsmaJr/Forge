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

type ProxyServer struct {
	DockerSocket string
	SocketDir    string
	mu           sync.Mutex
	listeners    map[string]net.Listener
}

func NewProxyServer(dockerSocket, socketDir string) *ProxyServer {
	return &ProxyServer{
		DockerSocket: dockerSocket,
		SocketDir:    socketDir,
		listeners:    make(map[string]net.Listener),
	}
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

	socketPath := filepath.Join(s.SocketDir, "agent-"+agentID+".sock")
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
		resource, id := s.parsePath(r.URL.Path)

		// Destructive or info-leaking actions on specific resources
		if id != "" && id != "json" && id != "create" && id != "prune" && id != "df" && id != "info" {
			labels, err := s.getResourceLabels(resource, id)
			if err == nil {
				if labels["forge.managed"] != "true" || labels["forge.agent_id"] != agentID {
					// We return a generic forbidden message for security (don't reveal too much)
					// But for now, let's include more info for debugging.
					http.Error(w, fmt.Sprintf("forbidden: access to %s %s denied (proxy agentID: %s, resource agent_id: %s)", resource, id, agentID, labels["forge.agent_id"]), http.StatusForbidden)
					return
				}
			}
		}

		// Prune protection
		if id == "prune" && r.Method == "POST" {
			// Ideally we would rewrite the query to include filters, but for alpha hardening
			// let's just block prune from agents for now to be safe.
			http.Error(w, "forbidden: prune is disabled via proxy", http.StatusForbidden)
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

func (s *ProxyServer) getResourceLabels(resource, id string) (map[string]string, error) {
	switch resource {
	case "containers":
		return s.getContainerLabels(id)
	case "volumes":
		return s.getVolumeLabels(id)
	case "networks":
		return s.getNetworkLabels(id)
	default:
		return nil, fmt.Errorf("unsupported resource type: %s", resource)
	}
}

func (s *ProxyServer) getContainerLabels(containerID string) (map[string]string, error) {
	client := http.Client{
		Transport: &http.Transport{
			DialContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
				return net.Dial("unix", s.DockerSocket)
			},
		},
	}

	resp, err := client.Get("http://docker/containers/" + containerID + "/json")
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("docker returned %d", resp.StatusCode)
	}

	var result struct {
		Config struct {
			Labels map[string]string `json:"Labels"`
		} `json:"Config"`
	}

	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, err
	}

	return result.Config.Labels, nil
}

func (s *ProxyServer) getVolumeLabels(name string) (map[string]string, error) {
	client := http.Client{
		Transport: &http.Transport{
			DialContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
				return net.Dial("unix", s.DockerSocket)
			},
		},
	}

	resp, err := client.Get("http://docker/volumes/" + name)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("docker returned %d", resp.StatusCode)
	}

	var result struct {
		Labels map[string]string `json:"Labels"`
	}

	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, err
	}

	return result.Labels, nil
}

func (s *ProxyServer) getNetworkLabels(id string) (map[string]string, error) {
	client := http.Client{
		Transport: &http.Transport{
			DialContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
				return net.Dial("unix", s.DockerSocket)
			},
		},
	}

	resp, err := client.Get("http://docker/networks/" + id)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("docker returned %d", resp.StatusCode)
	}

	var result struct {
		Labels map[string]string `json:"Labels"`
	}

	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, err
	}

	return result.Labels, nil
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
		if labels != nil && labels["forge.agent_id"] == agentID {
			filtered = append(filtered, c)
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
