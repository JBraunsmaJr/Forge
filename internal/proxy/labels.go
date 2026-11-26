package proxy

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"strings"
)

// Labels that Forge stamps on every resource it creates.
const (
	labelManaged = "forge.managed"
	labelAgentID = "forge.agent_id"
)

var (
	// errResourceNotFound means the daemon has no such resource. There is
	// nothing to protect, so the request can go through and the daemon will
	// answer 404 itself.
	errResourceNotFound = errors.New("resource not found")

	// errUnsupportedResource means the resource type carries no labels the
	// proxy knows how to read.
	errUnsupportedResource = errors.New("unsupported resource type")
)

// labelScoped reports whether resources of this type carry Forge ownership
// labels, and so can be attributed to an agent.
func labelScoped(resource string) bool {
	switch resource {
	case "containers", "volumes", "networks":
		return true
	}
	return false
}

// ownedByAgent reports whether labels mark a resource as created by agentID.
// Shards of an agent use IDs of the form "<agentID>-<suffix>".
func ownedByAgent(labels map[string]string, agentID string) bool {
	if labels[labelManaged] != "true" {
		return false
	}
	owner := labels[labelAgentID]
	return owner == agentID || strings.HasPrefix(owner, agentID+"-")
}

// getResourceLabels asks the daemon for the labels of one container, volume,
// or network. It returns errResourceNotFound when the daemon has no such
// resource and errUnsupportedResource for any other resource type.
func (s *ProxyServer) getResourceLabels(resource, id string) (map[string]string, error) {
	switch resource {
	case "containers":
		var body struct {
			Config struct {
				Labels map[string]string `json:"Labels"`
			} `json:"Config"`
		}
		if err := s.inspect("/containers/"+url.PathEscape(id)+"/json", &body); err != nil {
			return nil, err
		}
		return body.Config.Labels, nil
	case "volumes", "networks":
		var body struct {
			Labels map[string]string `json:"Labels"`
		}
		if err := s.inspect("/"+resource+"/"+url.PathEscape(id), &body); err != nil {
			return nil, err
		}
		return body.Labels, nil
	default:
		return nil, errUnsupportedResource
	}
}

// inspect performs a GET against the daemon and decodes the JSON answer.
func (s *ProxyServer) inspect(path string, out any) error {
	client := http.Client{
		Transport: &http.Transport{
			DialContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
				return net.Dial("unix", s.DockerSocket)
			},
		},
	}
	defer client.CloseIdleConnections()

	resp, err := client.Get("http://docker" + path)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	switch {
	case resp.StatusCode == http.StatusNotFound:
		return errResourceNotFound
	case resp.StatusCode != http.StatusOK:
		return fmt.Errorf("daemon answered %d", resp.StatusCode)
	}
	return json.NewDecoder(resp.Body).Decode(out)
}
