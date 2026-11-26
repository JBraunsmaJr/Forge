package proxy

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
)

// EnvAllowedBinds names the environment variable that lists host paths jobs
// may bind-mount, as comma-separated absolute paths. A path allows itself and
// everything beneath it. The default is none.
const EnvAllowedBinds = "FORGE_PROXY_ALLOWED_BINDS"

// maxCreateBodyBytes bounds how much of a container-create request the proxy
// will read in order to inspect it.
const maxCreateBodyBytes = 4 << 20

// createRequest is the part of a Docker container-create request that
// decides whether the new container could take control of the host.
type createRequest struct {
	HostConfig struct {
		Privileged   bool              `json:"Privileged"`
		Binds        []string          `json:"Binds"`
		Mounts       []createMount     `json:"Mounts"`
		NetworkMode  string            `json:"NetworkMode"`
		PidMode      string            `json:"PidMode"`
		IpcMode      string            `json:"IpcMode"`
		UTSMode      string            `json:"UTSMode"`
		UsernsMode   string            `json:"UsernsMode"`
		CgroupnsMode string            `json:"CgroupnsMode"`
		CapAdd       []string          `json:"CapAdd"`
		Devices      []json.RawMessage `json:"Devices"`
		SecurityOpt  []string          `json:"SecurityOpt"`
	} `json:"HostConfig"`
}

type createMount struct {
	Type   string `json:"Type"`
	Source string `json:"Source"`
}

// allowedBindsFromEnv parses EnvAllowedBinds into cleaned absolute paths.
// Entries that are not absolute are ignored.
func allowedBindsFromEnv() []string {
	var prefixes []string
	for _, entry := range strings.Split(os.Getenv(EnvAllowedBinds), ",") {
		entry = strings.TrimSpace(entry)
		if entry == "" || !filepath.IsAbs(entry) {
			continue
		}
		prefixes = append(prefixes, filepath.Clean(entry))
	}
	return prefixes
}

// checkContainerCreate reads a container-create request, applies the create
// policy, and leaves the body readable for the reverse proxy.
func (s *ProxyServer) checkContainerCreate(agentID string, r *http.Request) error {
	body, err := io.ReadAll(io.LimitReader(r.Body, maxCreateBodyBytes+1))
	r.Body.Close()
	if err != nil {
		return errors.New("could not read the container create request")
	}
	if len(body) > maxCreateBodyBytes {
		return errors.New("container create request is too large to inspect")
	}
	r.Body = io.NopCloser(bytes.NewReader(body))
	r.ContentLength = int64(len(body))

	var req createRequest
	if err := json.Unmarshal(body, &req); err != nil {
		return errors.New("container create request is not valid JSON")
	}

	allowedBinds := append([]string{s.agentSocketPath(agentID)}, s.AllowedBinds...)
	return validateCreate(&req, allowedBinds)
}

// validateCreate refuses any setting that would let the new container reach
// the host: privileged mode, host namespaces, added capabilities, host
// devices, disabled confinement, and bind mounts of host paths outside
// allowedBinds.
func validateCreate(req *createRequest, allowedBinds []string) error {
	hc := &req.HostConfig

	if hc.Privileged {
		return errors.New("privileged containers are not allowed")
	}

	namespaces := []struct{ name, mode string }{
		{"network", hc.NetworkMode},
		{"PID", hc.PidMode},
		{"IPC", hc.IpcMode},
		{"UTS", hc.UTSMode},
		{"user", hc.UsernsMode},
		{"cgroup", hc.CgroupnsMode},
	}
	for _, ns := range namespaces {
		if strings.EqualFold(strings.TrimSpace(ns.mode), "host") {
			return fmt.Errorf("sharing the host %s namespace is not allowed", ns.name)
		}
	}

	if len(hc.CapAdd) > 0 {
		return errors.New("adding Linux capabilities is not allowed")
	}
	if len(hc.Devices) > 0 {
		return errors.New("mapping host devices is not allowed")
	}
	for _, opt := range hc.SecurityOpt {
		if disablesConfinement(opt) {
			return fmt.Errorf("security option %q is not allowed", opt)
		}
	}

	for _, bind := range hc.Binds {
		if source, isHostPath := bindSource(bind); isHostPath && !bindAllowed(source, allowedBinds) {
			return fmt.Errorf("bind-mounting host path %q is not allowed", source)
		}
	}
	for _, m := range hc.Mounts {
		switch strings.ToLower(m.Type) {
		case "bind":
			if !bindAllowed(m.Source, allowedBinds) {
				return fmt.Errorf("bind-mounting host path %q is not allowed", m.Source)
			}
		case "npipe":
			return errors.New("named pipe mounts are not allowed")
		}
	}

	return nil
}

// bindSource splits a "source:target[:options]" bind and reports whether the
// source is a host path. A source that is not an absolute path is a named
// volume, which lives in the daemon's own storage.
func bindSource(bind string) (source string, isHostPath bool) {
	source, _, _ = strings.Cut(bind, ":")
	return source, strings.HasPrefix(source, "/")
}

// bindAllowed reports whether source is one of the allowed paths or lies
// beneath one. Both sides are cleaned first, so ".." cannot be used to climb
// out of an allowed directory.
func bindAllowed(source string, allowed []string) bool {
	if !filepath.IsAbs(source) {
		return false
	}
	source = filepath.Clean(source)
	for _, prefix := range allowed {
		if prefix == "" {
			continue
		}
		prefix = filepath.Clean(prefix)
		if source == prefix || prefix == "/" || strings.HasPrefix(source, prefix+"/") {
			return true
		}
	}
	return false
}

// disablesConfinement reports whether a --security-opt value turns off one of
// the kernel confinement layers. Docker accepts both "=" and ":" separators.
func disablesConfinement(opt string) bool {
	normalized := strings.ToLower(strings.ReplaceAll(strings.TrimSpace(opt), ":", "="))
	switch normalized {
	case "seccomp=unconfined", "apparmor=unconfined", "label=disable", "systempaths=unconfined":
		return true
	}
	return false
}
