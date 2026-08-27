# Cloud Autoscaling

Instead of running a fixed pool of self-hosted agents, Forge can automatically provision and tear down agents in response to queue depth. The **autoscaler** (`forge-autoscaler`) is a standalone service that watches the scheduler's queue and agent registry, then calls a **cloud provisioner** to scale compute up or down.

---

## Concepts

### Hot pool vs. burst pool

The autoscaler manages two independent pools of agents:

| Pool    | Purpose                                                                                       | Scale-down behavior                                                                                                          |
|---------|-------------------------------------------------------------------------------------------------|---------------------------------------------------------------------------------------------------------------------------------|
| `hot`   | A floor of always-on agents so the first job in a queue doesn't wait for a cold VM to boot.     | Never scaled below `FORGE_AUTOSCALER_HOT_POOL_SIZE`.                                                                            |
| `burst` | Extra capacity spun up when the queue is deeper than currently available agent capacity.        | Drained and torn down once idle (0 active jobs) for `FORGE_AUTOSCALER_IDLE_TIMEOUT`, or immediately if it never registers with the scheduler within 10 minutes of being created (orphan cleanup). |

On every control-loop tick (`FORGE_AUTOSCALER_POLL_INTERVAL`), the autoscaler:

1. Lists instances known to the cloud provisioner.
2. Lists agents currently registered with the scheduler (`GET /api/v1/agents`) and the current queue depth (`GET /api/v1/queue/depth`).
3. Tops up the hot pool if it's under its configured floor.
4. Scales up the burst pool if `queue depth > available capacity`, subject to a cooldown (`FORGE_AUTOSCALER_SCALE_UP_DELAY`) and the `FORGE_AUTOSCALER_MAX_BURST_SIZE` ceiling.
5. Drains (`POST /api/v1/agents/{id}/drain`) and tears down burst instances that have been idle past `FORGE_AUTOSCALER_IDLE_TIMEOUT`.

### Cloud provisioners

The autoscaler is provider-agnostic — it drives any `CloudProvisioner` implementation with three methods: `ScaleUp`, `ScaleDown`, `ListInstances`. Two ship today:

| Provider (`FORGE_AUTOSCALER_PROVIDER`) | Backing infrastructure                                          | Intended use                                            |
|------------------------------------------|--------------------------------------------------------------------|-------------------------------------------------------------|
| `docker-fake` (default)                  | Runs additional `forge agent` containers on the same Docker host as the autoscaler | Local development and testing of the control loop only — it does not provision real cloud capacity. |
| `azure`                                  | Azure Virtual Machine Scale Sets (VMSS)                            | Production cloud provisioning.                              |

> ⚠️ `docker-fake` requires access to the host's Docker socket and is only meant for exercising hot/burst-pool logic locally. For a real deployment, use the `azure` provider — see [Running against Azure](#running-against-azure-production) below.

---

## Configuration

### Core autoscaler settings

| Variable                          | Default                  | Description                                                                                 |
|-------------------------------------|---------------------------|-------------------------------------------------------------------------------------------------|
| `FORGE_SCHEDULER_URL`               | `http://localhost:8080`   | The scheduler the autoscaler reports to and reads queue/agent state from.                       |
| `FORGE_API_TOKEN`                   | —                          | Token used to call the scheduler's agent/queue endpoints. Use a token with the `agent` role.    |
| `FORGE_AUTOSCALER_PROVIDER`         | `docker-fake`              | Which `CloudProvisioner` to use: `docker-fake` or `azure`.                                      |
| `FORGE_AUTOSCALER_HOT_POOL_SIZE`    | `0`                        | Minimum number of always-on agents.                                                             |
| `FORGE_AUTOSCALER_MAX_BURST_SIZE`   | `10`                       | Maximum number of burst agents that can be running at once.                                     |
| `FORGE_AUTOSCALER_IDLE_TIMEOUT`     | `5m`                       | How long a burst agent must be idle (0 active jobs) before it's drained and torn down.          |
| `FORGE_AUTOSCALER_POLL_INTERVAL`    | `10s`                      | How often the control loop runs.                                                                 |
| `FORGE_AUTOSCALER_SCALE_UP_DELAY`   | `1m`                       | Cooldown between burst scale-up events, to avoid thrashing.                                     |
| `FORGE_AUTOSCALER_METRICS_PORT`     | `9091`                     | Port the Prometheus `/metrics` endpoint listens on.                                             |

Durations use Go's duration syntax (`30s`, `5m`, `1h`).

### `docker-fake` provider settings

| Variable                          | Default                    | Description                                                                                    |
|-------------------------------------|-------------------------------|-----------------------------------------------------------------------------------------------------|
| `FORGE_AUTOSCALER_DOCKER_IMAGE`     | `forge-fake-agent:latest`     | Image to run for each provisioned "instance".                                                       |
| `FORGE_AUTOSCALER_DOCKER_NETWORK`   | —                              | Docker network the spawned agent containers join. Should match the network the scheduler is on.     |
| `FORGE_PROXY_AGENT_ID`              | —                              | Optional. If set, stamped onto spawned containers as the `forge.agent_id` label and `FORGE_PROXY_AGENT_ID` env var. |

### `azure` provider settings

| Variable                        | Description                                                                                     |
|------------------------------------|------------------------------------------------------------------------------------------------------|
| `FORGE_AZURE_SUBSCRIPTION_ID`      | **Required.** Azure subscription containing the scale sets.                                          |
| `FORGE_AZURE_RESOURCE_GROUP`       | **Required.** Resource group containing the scale sets.                                              |
| `FORGE_AZURE_HOT_VMSS`             | Name of the VM Scale Set scaled for the `hot` pool. Required if `FORGE_AUTOSCALER_HOT_POOL_SIZE > 0`. |
| `FORGE_AZURE_BURST_VMSS`           | Name of the VM Scale Set scaled for the `burst` pool. Required for burst scaling.                    |

Authentication uses the Azure SDK's [`DefaultAzureCredential`](https://learn.microsoft.com/azure/developer/go/azure-sdk-authentication) chain, so the autoscaler will pick up credentials from environment variables, a managed identity, or the Azure CLI — whichever is available first. For an unattended deployment (e.g. [`deployments/autoscaler/compose.yml`](https://github.com/JBraunsmaJr/Forge/blob/main/deployments/autoscaler/compose.yml)), use a service principal:

| Variable              | Description                                       |
|-------------------------|------------------------------------------------------|
| `AZURE_CLIENT_ID`       | Service principal (app registration) client ID.      |
| `AZURE_CLIENT_SECRET`   | Service principal client secret.                     |
| `AZURE_TENANT_ID`       | Azure AD tenant ID.                                  |

The service principal only needs permission to read and update the target VM Scale Sets — a scoped role (e.g. `Virtual Machine Contributor`) on the resource group is sufficient; it does not need subscription-wide `Contributor`.

Each hot/burst VM Scale Set should run an image or custom-data script that starts the Forge agent (`forge agent <scheduler-url>`) on boot, pointed at the same scheduler the autoscaler is configured against.

---

## Metrics & Alerting

The autoscaler exposes Prometheus metrics on `FORGE_AUTOSCALER_METRICS_PORT` (default `9091`) at `/metrics`:

| Metric                                      | Type    | Labels               | Description                                                                                                            |
|------------------------------------------------|---------|-------------------------|------------------------------------------------------------------------------------------------------------------------------|
| `forge_autoscaler_pool_size`                    | Gauge   | `pool`                  | Current instance count per pool.                                                                                             |
| `forge_autoscaler_max_pool_size`                | Gauge   | `pool`                  | Configured ceiling per pool.                                                                                                 |
| `forge_autoscaler_scale_events_total`           | Counter | `pool`, `direction`     | Scale-up/down events since start.                                                                                            |
| `forge_autoscaler_observed_queue_depth`         | Gauge   | —                       | Last observed scheduler queue depth.                                                                                         |
| `forge_autoscaler_provisioner_errors_total`     | Counter | `operation`             | Provisioner call failures by operation (`list`, `scale_up_hot`, `scale_up_burst`, `scale_down`, `scale_down_orphan`).        |

A Grafana panel set is provisioned automatically from [`grafana/provisioning/dashboards/forge-dashboard.json`](https://github.com/JBraunsmaJr/Forge/blob/main/grafana/provisioning/dashboards/forge-dashboard.json) when running with [`compose.metrics.yml`](https://github.com/JBraunsmaJr/Forge/blob/main/compose.metrics.yml). A default Prometheus alert, `BurstPoolPinnedAtMax`, fires when the burst pool has been pinned at its configured maximum for 15 minutes while jobs are still queued — see [`prometheus/alerts.yml`](https://github.com/JBraunsmaJr/Forge/blob/main/prometheus/alerts.yml).

---

## Running locally (`docker-fake`)

The root [`compose.yml`](https://github.com/JBraunsmaJr/Forge/blob/main/compose.yml) already includes an `autoscaler` service wired to `docker-fake`, so it works out of the box:

```bash
docker compose up --build -d
```

This is useful for exercising hot-pool/burst-pool logic and the scheduler's drain endpoint without a cloud account. It reuses the local Docker daemon and the `forge` network created by the rest of the stack.

## Running against Azure (production)

For a real deployment, run the autoscaler as its own service — pointed at an Azure resource group containing your hot/burst VM Scale Sets — using the dedicated deployment compose file:

```bash
cd deployments/autoscaler
cp .env.example .env   # fill in the scheduler URL, token, and Azure details
docker compose up -d
```

See [`deployments/autoscaler/compose.yml`](https://github.com/JBraunsmaJr/Forge/blob/main/deployments/autoscaler/compose.yml) and the [Deployments guide](https://github.com/JBraunsmaJr/Forge/blob/main/deployments/Readme.md) for the full environment variable list.

Unlike `docker-fake`, the `azure` provider only makes outbound calls to Azure Resource Manager — it does not need a Docker socket, so this deployment doesn't mount one.

> The compose file pulls `FORGE_AUTOSCALER_IMAGE` (default `ghcr.io/jbraunsmajr/forge/forge-autoscaler:latest`). If that image isn't published to a registry reachable from your host yet, build it from [`deployments/autoscaler/Dockerfile`](https://github.com/JBraunsmaJr/Forge/blob/main/deployments/autoscaler/Dockerfile) and push it somewhere reachable, or point `FORGE_AUTOSCALER_IMAGE` at a locally built tag.
