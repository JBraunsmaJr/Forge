package proxy

import (
	"errors"
	"fmt"
	"net/http"
)

// blockedResources are Docker API areas the proxy never forwards. Each one
// offers a way to run code on the host outside a job's own containers.
var blockedResources = map[string]bool{
	"plugins":  true, // plugins run with host privileges
	"swarm":    true,
	"services": true,
	"nodes":    true,
	"secrets":  true,
	"configs":  true,
	"tasks":    true,
}

// authorize decides whether a request from agentID may be forwarded to the
// daemon. It returns "" to allow, or the reason for refusing.
//
// The rules, in order:
//  1. Blocked API areas are never forwarded.
//  2. Prune is never forwarded: it acts on every agent's resources.
//  3. A request aimed at one container, volume, or network is forwarded only
//     if that resource carries this agent's labels. If the labels cannot be
//     read, ownership is unknown and the request is refused.
//  4. A container-create request must pass the create policy.
func (s *ProxyServer) authorize(agentID string, r *http.Request) string {
	resource, id := s.parsePath(r.URL.Path)

	if blockedResources[resource] {
		return fmt.Sprintf("the %s API is not available through the proxy", resource)
	}

	if id == "prune" && r.Method == http.MethodPost {
		return "prune is disabled via proxy"
	}

	if labelScoped(resource) && targetsOneResource(id) {
		labels, err := s.getResourceLabels(resource, id)
		switch {
		case errors.Is(err, errResourceNotFound):
			// Nothing to protect; the daemon will answer 404.
		case err != nil:
			return fmt.Sprintf("cannot verify the owner of %s %s", resource, id)
		case !ownedByAgent(labels, agentID):
			return fmt.Sprintf("access to %s %s denied", resource, id)
		}
	}

	if resource == "containers" && id == "create" && r.Method == http.MethodPost {
		if err := s.checkContainerCreate(agentID, r); err != nil {
			return err.Error()
		}
	}

	return ""
}

// targetsOneResource reports whether the second path segment names a single
// resource rather than a collection-level action such as list or create.
func targetsOneResource(id string) bool {
	switch id {
	case "", "json", "create", "prune", "df", "info":
		return false
	}
	return true
}
