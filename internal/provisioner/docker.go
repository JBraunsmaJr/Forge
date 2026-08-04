package provisioner

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os/exec"
	"strings"
	"time"
)

type DockerFakeProvisioner struct {
	Image         string
	SchedulerURL  string
	Network       string
	AgentID       string
	APIToken      string
	ProxyURL      string
	SocketsVolume string
}

func (p *DockerFakeProvisioner) ScaleUp(ctx context.Context, pool string, n int, labels map[string]string) ([]InstanceID, error) {
	var ids []InstanceID
	for i := 0; i < n; i++ {
		args := []string{"run", "-d"}
		args = append(args, "--label", "forge-pool="+pool)
		args = append(args, "--label", "forge.managed=true")
		if p.AgentID != "" {
			args = append(args, "--label", "forge.agent_id="+p.AgentID)
			args = append(args, "-e", "FORGE_PROXY_AGENT_ID="+p.AgentID)
		}
		args = append(args, "-e", "FORGE_SCHEDULER_URL="+p.SchedulerURL)
		args = append(args, "-e", "FORGE_AGENT_POOL="+pool)
		if p.APIToken != "" {
			args = append(args, "-e", "FORGE_API_TOKEN="+p.APIToken)
		}
		if p.ProxyURL != "" {
			args = append(args, "-e", "FORGE_PROXY_URL="+p.ProxyURL)
		}
		if p.SocketsVolume != "" {
			// Same shared volume + mount path the static agent service in
			// compose.yml uses. Without it, the socket path the agent
			// gets back from registering with the proxy doesn't actually
			// exist inside this container — every docker command then
			// fails with "is the docker daemon running?", proxy
			// registered or not.
			args = append(args, "--volume", p.SocketsVolume+":/run/forge-sockets")
		}
		// Ensure agent knows its own ID is the container ID
		// In ScaleUp we don't know the ID yet, but we can set an env var that the agent reads.
		// Or the agent can just get it from its own hostname if we don't override it.

		if p.Network != "" {
			args = append(args, "--network", p.Network)
		}

		for k, v := range labels {
			args = append(args, "--label", fmt.Sprintf("forge-label-%s=%s", k, v))
			args = append(args, "-e", fmt.Sprintf("FORGE_AGENT_LABEL_%s=%s", strings.ToUpper(k), v))
		}

		args = append(args, p.Image)
		args = append(args, "agent")

		cmd := exec.CommandContext(ctx, "docker", args...)
		out, err := cmd.Output()
		if err != nil {
			var exitError *exec.ExitError
			if errors.As(err, &exitError) {
				return ids, fmt.Errorf("docker run failed: %w: %s", err, strings.TrimSpace(string(exitError.Stderr)))
			}
			return ids, fmt.Errorf("docker run failed: %w", err)
		}
		id := strings.TrimSpace(string(out))
		if len(id) > 12 {
			id = id[:12]
		}
		ids = append(ids, InstanceID(id))
	}
	return ids, nil
}

func (p *DockerFakeProvisioner) ScaleDown(ctx context.Context, ids []InstanceID) error {
	var errs []error
	for _, id := range ids {
		cmd := exec.CommandContext(ctx, "docker", "rm", "-f", string(id))
		if out, err := cmd.CombinedOutput(); err != nil {
			errs = append(errs, fmt.Errorf("docker rm failed for %s: %w: %s", id, err, string(out)))
		}
	}
	return errors.Join(errs...)
}

func (p *DockerFakeProvisioner) ListInstances(ctx context.Context) ([]Instance, error) {
	args := []string{"ps", "-a", "--filter", "label=forge-pool", "--format", "{{.ID}}"}
	if p.AgentID != "" {
		args = append(args, "--filter", "label=forge.agent_id="+p.AgentID)
	}
	cmd := exec.CommandContext(ctx, "docker", args...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return nil, fmt.Errorf("docker ps failed: %w: %s", err, string(out))
	}

	idLines := strings.Fields(strings.TrimSpace(string(out)))
	if len(idLines) == 0 {
		return nil, nil
	}

	var instances []Instance
	for _, id := range idLines {
		cmd := exec.CommandContext(ctx, "docker", "inspect", id, "--format", "{{index .Config.Labels \"forge-pool\"}}|{{.Created}}|{{json .Config.Labels}}")
		out, err := cmd.CombinedOutput()
		if err != nil {
			continue
		}
		parts := strings.SplitN(strings.TrimSpace(string(out)), "|", 3)
		if len(parts) < 3 {
			continue
		}

		pool := parts[0]
		createdStr := parts[1]
		labelsJSON := parts[2]

		created, _ := time.Parse(time.RFC3339Nano, createdStr)

		var allLabels map[string]string
		if err := json.Unmarshal([]byte(labelsJSON), &allLabels); err != nil {
			continue
		}

		labels := make(map[string]string)
		for k, v := range allLabels {
			if after, ok := strings.CutPrefix(k, "forge-label-"); ok {
				labels[after] = v
			}
		}

		instances = append(instances, Instance{
			ID:        InstanceID(id),
			Pool:      pool,
			CreatedAt: created,
			Labels:    labels,
		})
	}
	return instances, nil
}
