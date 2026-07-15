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
		if resp.Request.Method == "GET" && strings.HasPrefix(resp.Request.URL.Path, "/containers/json") && resp.StatusCode == http.StatusOK {
			return s.filterContainerList(agentID, resp)
		}
		return nil
	}

	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == "DELETE" && strings.HasPrefix(r.URL.Path, "/containers/") {
			containerID := strings.TrimPrefix(r.URL.Path, "/containers/")
			containerID = strings.Split(containerID, "/")[0]

			if containerID != "" && containerID != "json" {
				labels, err := s.getContainerLabels(containerID)
				if err != nil {
					// If container doesn't exist, let it pass to proxy to return 404
					proxy.ServeHTTP(w, r)
					return
				}

				if labels["forge.managed"] != "true" {
					http.Error(w, "not a forge-managed container", http.StatusForbidden)
					return
				}

				if labels["forge.agent_id"] != agentID {
					http.Error(w, "container owned by different agent", http.StatusForbidden)
					return
				}
			}
		}
		proxy.ServeHTTP(w, r)
	})
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

func (s *ProxyServer) filterContainerList(agentID string, resp *http.Response) error {
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
