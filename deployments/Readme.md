### Forge Deployment Compose Files

This folder contains predefined Docker Compose files for deploying Forge in a distributed environment.

#### Directory Structure

- `scheduler/`: Contains the compose file for the Forge Scheduler and its dependencies (Postgres, Vault, MinIO).
- `agent/`: Contains the compose file for deploying one or more Forge Agents on a node.

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
| `VAULT_DEV_ROOT_TOKEN` | Root token for Vault.                                       | `forge-dev-token`       |

#### 2. Agent Deployment (Distributed)

To deploy agents on a separate node, they must be able to reach the scheduler via a network-accessible URL.

**Quick Start:**

```bash
cd deployments/agent
# Set the scheduler URL to the IP/DNS of your scheduler node
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

**Note on Scaling:**
You can scale the number of agents on a node using the `--scale` flag with `docker compose`:
```bash
docker compose up -d --scale agent=5
```

#### Communication in Distributed Environment

For agents to communicate with the scheduler:
1. Ensure the scheduler node's firewall allows traffic on port `8080` (and `8200` for Vault, `9000` for MinIO if needed).
2. Set `FORGE_SCHEDULER_URL` on the agent node to the reachable address of the scheduler.
3. If agents need to use Vault, ensure `FORGE_VAULT_ADDR` is set and reachable.
