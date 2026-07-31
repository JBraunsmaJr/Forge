### Forge Deployment Compose Files

This folder contains predefined Docker Compose files for deploying Forge in a distributed environment.

#### Directory Structure

- `scheduler/`: Contains the compose file for the Forge Scheduler and its dependencies (Postgres, Vault, MinIO).
- `agent/`: Contains the compose file for deploying one or more Forge Agents on a node.
- `autoscaler/`: Contains the compose file for deploying the Forge Autoscaler against a cloud provisioner (Azure VM Scale Sets).

#### 1. Scheduler Deployment

The scheduler deployment includes:
- **Forge Scheduler**: The central coordinator.
- **Postgres**: For storing run history and project metadata.
- **Vault**: For secure secret management.
- **MinIO**: For artifact storage (S3 compatible).

**Quick Start:**

```bash
cd deployments/scheduler
docker compose up -d
```

**Environment Variables:**

| Variable               | Description                                                 | Default                 |
|------------------------|-------------------------------------------------------------|-------------------------|
| `FORGE_BASE_URL`       | The external URL of the scheduler.                          | `http://localhost:8080` |
| `FORGE_AGENT_TOKEN`    | Token used by agents to authenticate with the scheduler.    | `forge-dev-agent-token` |
| `FORGE_ROOT_TOKEN`     | Admin token for CLI/UI access.                              | `forge-dev-admin-token` |
| `FORGE_S3_PUBLIC_URL`  | Publicly reachable URL for MinIO (used for artifact links). | `http://localhost:9000` |
| `FORGE_VAULT_TOKEN`    | Root token for Vault.                                       | -                       |

#### 2. Agent Deployment (Distributed)

To deploy agents on a separate node, they must be able to reach the scheduler via a network-accessible URL.

**Quick Start:**

```bash
cd deployments/agent
# Set the scheduler URL to the IP/DNS of your scheduler node
# Checkout docs/https.md on how to configure HTTPs
export FORGE_SCHEDULER_URL=http://scheduler-node-ip:8080
export FORGE_AGENT_TOKEN=your-secret-agent-token
docker compose up -d --scale agent=3
```

**Environment Variables:**

| Variable              | Description                                                    | Default                 |
|-----------------------|----------------------------------------------------------------|-------------------------|
| `FORGE_SCHEDULER_URL` | The URL of the Forge Scheduler.                                | `http://localhost:8080` |
| `FORGE_AGENT_TOKEN`   | Must match the token configured on the scheduler.              | `forge-dev-agent-token` |
| `AGENT_REPLICAS`      | Number of agents to run (used if using `docker stack deploy`). | `1`                     |
| `FORGE_VAULT_ADDR`    | Address of the Vault server (optional).                        | -                       |
| `FORGE_AGENT_CONCURRENCY` | Max concurrent jobs this agent can run.                    | `1`                     |
| `FORGE_WORKSPACE`     | Path where the agent stores job workspaces.                    | `/tmp/forge`            |

**Note on Scaling:**
To scale your Forge cluster, you have two options:

1. **Horizontal Scaling (Multiple Nodes)**: Run one agent per physical/virtual host. This is the recommended way to scale for high availability and resource isolation.
2. **Vertical Scaling (Concurrent Jobs)**: Increase `FORGE_AGENT_CONCURRENCY` on a single agent. This allows one agent to handle multiple jobs simultaneously.
3. **Container Scaling (Multiple Agents)**: Use the `--scale` flag in Docker Compose to run multiple agent containers on one host. This is now safe and won't cause port conflicts.

**Scaling with Docker Compose:**
You can scale the number of agents on a node using the `--scale` flag. Since agents use a 'Reverse WebSocket' model for debug sessions, they do not need to expose any ports, and you can run multiple agent containers on the same host without port conflicts.

```bash
# Scale to 5 agent containers on one host
docker compose up -d --scale agent=5
```

#### 3. Autoscaler Deployment (Cloud Provisioner)

The autoscaler deployment runs `forge-autoscaler` against a real cloud provisioner (Azure VM Scale Sets) rather than the local Docker-based fake used by the root `compose.yml` dev stack. It provisions and tears down agents automatically based on scheduler queue depth, so it does not run any agents itself and does not need access to a Docker socket.

**Quick Start:**

```bash
cd deployments/autoscaler
cp .env.example .env
# Fill in FORGE_SCHEDULER_URL, FORGE_AGENT_TOKEN, and the FORGE_AZURE_* / AZURE_* values
docker compose up -d
```

**Environment Variables:**

| Variable                     | Description                                                                    | Default                 |
|-------------------------------|------------------------------------------------------------------------------------|----------------------------|
| `FORGE_SCHEDULER_URL`         | The URL of the Forge Scheduler this autoscaler manages agents for.                 | `http://localhost:8080`   |
| `FORGE_AGENT_TOKEN`           | Token used to call the scheduler's agent/queue endpoints. Must match a token configured on the scheduler. | `forge-dev-agent-token`   |
| `FORGE_AUTOSCALER_HOT_POOL_SIZE` | Minimum number of always-on agents.                                              | `0`                        |
| `FORGE_AUTOSCALER_MAX_BURST_SIZE` | Maximum number of burst agents running at once.                                | `10`                       |
| `FORGE_AZURE_SUBSCRIPTION_ID` | Azure subscription containing the VM Scale Sets.                                   | -                          |
| `FORGE_AZURE_RESOURCE_GROUP`  | Resource group containing the VM Scale Sets.                                       | -                          |
| `FORGE_AZURE_HOT_VMSS`        | VM Scale Set name backing the `hot` pool.                                          | -                          |
| `FORGE_AZURE_BURST_VMSS`      | VM Scale Set name backing the `burst` pool.                                        | -                          |
| `AZURE_CLIENT_ID`             | Service principal client ID (Azure SDK default credential chain).                  | -                          |
| `AZURE_CLIENT_SECRET`         | Service principal client secret.                                                   | -                          |
| `AZURE_TENANT_ID`             | Azure AD tenant ID.                                                                | -                          |

See the [Cloud Autoscaling guide](../docs/Cloud-Autoscaling.md) for the full variable reference, the hot/burst pool model, and Prometheus metrics exposed on port `9091`.

---

#### Communication in Distributed Environment

For agents to communicate with the scheduler:
1. Ensure the scheduler node's firewall allows traffic on port `8080` (and `8200` for Vault, `9000` for MinIO if needed).
2. Set `FORGE_SCHEDULER_URL` on the agent node to the reachable address of the scheduler.
3. If agents need to use Vault, ensure `FORGE_VAULT_ADDR` is set and reachable.
