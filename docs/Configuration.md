# Configuration Reference

All Forge components are configured via environment variables. There are no configuration files.

---

## Scheduler

| Variable                       | Default                  | Description                                                                                                                                                                                              |
|--------------------------------|--------------------------|----------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------|
| `FORGE_DB_URL`                 | —                        | **Required.** PostgreSQL connection string. Format: `postgres://user:pass@host:port/dbname?sslmode=disable`                                                                                              |
| `FORGE_ROOT_TOKEN`             | —                        | Pre-set admin token for reproducible environments (compose, CI). If unset, a random token is generated and printed on first start.                                                                       |
| `FORGE_BASE_URL`               | `http://localhost{addr}` | Public URL of this scheduler. Used to construct artifact download URLs for the local backend.                                                                                                            |
| `FORGE_GRPC_ADDR`              | `:50051`                 | Listen address for gRPC agent communication.                                                                                                                                                             |
| `FORGE_ARTIFACT_STORE`         | `local`                  | Artifact backend: `local` or `s3`.                                                                                                                                                                       |
| `FORGE_ARTIFACT_DIR`           | `/data/artifacts`        | Directory for local artifact storage.                                                                                                                                                                    |
| `FORGE_S3_ENDPOINT`            | —                        | S3-compatible endpoint URL. Leave empty for AWS S3. Example: `http://minio:9000`.                                                                                                                        |
| `FORGE_S3_PUBLIC_URL`          | —                        | Public URL for artifacts when S3 endpoint is internal. Browsers will use this to view/download. **Note**: If your dashboard is HTTPS, this must also be HTTPS (or left empty to use the built-in proxy). |
| `FORGE_S3_BUCKET`              | `forge-artifacts`        | S3 bucket name.                                                                                                                                                                                          |
| `FORGE_S3_REGION`              | `us-east-1`              | S3 region.                                                                                                                                                                                               |
| `FORGE_S3_ACCESS_KEY`          | —                        | S3 access key ID.                                                                                                                                                                                        |
| `FORGE_S3_SECRET_KEY`          | —                        | S3 secret access key.                                                                                                                                                                                    |
| `FORGE_RUN_RETENTION`          | `30d`                    | How long to keep job runs and artifacts (e.g. `7d`, `24h`, `30m`). Set to `0` to disable.                                                                                                                |
| `FORGE_RUN_RETENTION_INTERVAL` | `24h`                    | How often to run the background retention worker. Defaults to `1h` if retention < 24h.                                                                                                                   |
| `FORGE_PRUNE_SCHEDULE`         | `@daily`                 | Cron-style schedule for `docker system prune` (e.g. `@hourly`, `@daily`, or duration like `12h`).                                                                                                        |
| `FORGE_CACHE_DIR`              | `/data/cache`            | Path to persistent cache storage for distributed caching.                                                                                                                                                |
| `FORGE_LOG_RETENTION_DAYS`     | `30`                     | Log retention in days. Logs older than this will be pruned hourly.                                                                                                                                       |
| `FORGE_GITHUB_CLIENT_ID`      | —                        | GitHub OAuth2 Client ID for SSO.                                                                                                                                                                         |
| `FORGE_GITHUB_CLIENT_SECRET`  | —                        | GitHub OAuth2 Client Secret for SSO.                                                                                                                                                                     |
| `FORGE_GITLAB_CLIENT_ID`      | —                        | GitLab OAuth2 Client ID for SSO.                                                                                                                                                                         |
| `FORGE_GITLAB_CLIENT_SECRET`  | —                        | GitLab OAuth2 Client Secret for SSO.                                                                                                                                                                     |
| `FORGE_DOCKER_NETWORK`         | —                        | The network that policy transformer containers will be attached to. Must match the network the scheduler container is on.                                                                                |
| `FORGE_PUBLIC_URL`            | `http://localhost:8080`  | The public URL of the scheduler, used for OAuth callback redirects.                                                                                                                                      |
| `FORGE_UI_URL`                | `http://localhost:8080`  | The URL to redirect back to after successful SSO login.                                                                                                                                                  |

---

## Agent

| Variable                   | Default                 | Description                                                                                                                 |
|----------------------------|-------------------------|-----------------------------------------------------------------------------------------------------------------------------|
| `FORGE_API_TOKEN`          | —                       | **Required.** API token for scheduler authentication. Use a token with the `agent` role. See [Roles & Permissions](Roles.md) for details. |
| `FORGE_SCHEDULER_URL`      | `http://localhost:8080` | The URL of the scheduler. Used for all agent-scheduler communication. Switch to `https://` for secure connections.          |
| `FORGE_VAULT_ADDR`         | —                       | Vault server address. Required for steps that use `secrets:`. Example: `http://vault:8200`.                                 |
| `FORGE_VAULT_TOKEN`        | —                       | Vault authentication token.                                                                                                 |
| `FORGE_PROXY_URL`         | —                       | Optional. The URL of the Forge Docker Proxy (e.g. `http://proxy:9090`). If set, the agent will use a proxied Docker socket. |
| `FORGE_GRPC_ADDR`          | —                       | Optional. Explicit `host:port` for the gRPC session (e.g. `scheduler:50051`). If unset, derived from `FORGE_SCHEDULER_URL`. |
| `FORGE_DOCKER_MAX_GB`      | `50`                    | Max GB Docker is allowed to use before LRU eviction triggers.                                                               |
| `FORGE_DOCKER_MAX_PERCENT` | `80`                    | Max disk usage percentage before LRU eviction triggers.                                                                     |
| `FORGE_DOCKER_NETWORK`     | —                       | **Important for containerized agents.** The network that job containers will join. Use this to ensure jobs can reach reachable IPs or internal service names.                                |

---

## Agent gRPC Connection

The agent connects to the scheduler via gRPC. The connection details are determined as follows:

1.  If `FORGE_GRPC_ADDR` is set, the agent uses that address. It must be in `host:port` format. If it starts with `http://` or `https://`, the scheme is stripped.
2.  If `FORGE_GRPC_ADDR` is NOT set, the agent derives the address from `FORGE_SCHEDULER_URL`:
    - `https://forge.dev` -> `forge.dev:443` (Secure gRPC enabled)
    - `http://scheduler:8080` -> `scheduler:50051` (Insecure gRPC)
    - `https://forge.dev:8443` -> `forge.dev:8443` (Secure gRPC)

Forge uses gRPC keepalives (10s pings) to maintain connections through proxies and load balancers.

---

## CLI

| Variable              | Default                 | Description                                                                                                       |
|-----------------------|-------------------------|-------------------------------------------------------------------------------------------------------------------|
| `FORGE_API_TOKEN`     | —                       | API token for scheduler requests. Required for `submit`, `status`, `org`, `policy`, `token`, `project`, `secret`. |
| `FORGE_SCHEDULER_URL` | `http://localhost:8080` | Default scheduler URL for all commands.                                                                           |
| `FORGE_ORG`           | —                       | Default org ID. Used as `--org` default for `submit`, `secret`, `policy`.                                         |
| `FORGE_VAULT_ADDR`    | —                       | Vault address for `secret` commands.                                                                              |
| `FORGE_VAULT_TOKEN`   | —                       | Vault token for `secret` commands.                                                                                |

---

## Injected Environment Variables

Forge automatically injects several environment variables into every job container. These can be used in your `run` scripts or condition expressions.

| Variable              | Description                                                             |
|-----------------------|-------------------------------------------------------------------------|
| `FORGE_REF`           | The full Git reference (e.g., `refs/heads/main` or `refs/tags/v1.0.0`). |
| `FORGE_BRANCH`        | The Git branch name (derived from `FORGE_REF`).                         |
| `FORGE_COMMIT_TAG`    | The Git tag name, if the run was triggered by a tag.                    |
| `FORGE_COMMIT_SHA`    | The full 40-character commit SHA.                                       |
| `FORGE_RUN_ID`        | The unique identifier for the current run.                              |
| `FORGE_JOB_ID`        | The unique identifier for the current job.                              |
| `FORGE_STEP_ID`       | The logical ID of the step from the pipeline YAML.                      |
| `FORGE_PROJECT_ID`    | The unique identifier for the project.                                  |
| `FORGE_SCHEDULER_URL` | The URL of the scheduler.                                               |

---

## Docker Compose Stack

The compose stack reads from a `.env` file (copy from `.env.example`):

| Variable           | Default                 | Description                                                                 |
|--------------------|-------------------------|-----------------------------------------------------------------------------|
| `FORGE_ROOT_TOKEN` | `forge-dev-admin-token` | Admin token preset for all services. Change for staging/production.         |

### Compose service environment summary

**scheduler:**
- `FORGE_DB_URL` — points to the `postgres` service
- `FORGE_ROOT_TOKEN` — from `.env`
- `FORGE_BASE_URL` — `http://localhost:8080`
- `FORGE_ARTIFACT_STORE=s3` — uses MinIO
- `FORGE_S3_*` — MinIO credentials

**agent-1 / agent-2:**
- `FORGE_API_TOKEN` — same as `FORGE_ROOT_TOKEN` (dev convenience; use a separate agent token in production)
- `FORGE_VAULT_ADDR` — points to the `vault` service
- `FORGE_VAULT_TOKEN` — `forge-dev-token`
- `FORGE_PROXY_URL` — `http://proxy:9090` (enables alpha-hardening)

**proxy:**
- Management server on `:9090`
- Proxies Docker API from `/var/run/docker.sock` to per-agent Unix sockets in `/run/forge-sockets`

---

## Port Reference

| 8080 | Scheduler  | HTTP API + Web UI        |
| 50051| Scheduler  | gRPC Agent Communication  |
| 5432 | PostgreSQL | Database                 |
| 8200 | Vault      | Secrets storage          |
| 9000 | MinIO      | S3 API                   |
| 9001 | MinIO      | Web console              |

---

## SSO / OAuth2 Configuration

To enable Single Sign-On, you must register Forge as an OAuth application with your provider and set the following environment variables in the scheduler.

### GitHub Setup

1.  Go to **Settings > Developer Settings > OAuth Apps > New OAuth App**.
2.  **Homepage URL**: Your `FORGE_PUBLIC_URL` (e.g., `https://forge.example.com`).
3.  **Authorization callback URL**: `{FORGE_PUBLIC_URL}/api/v1/auth/callback/github`.
4.  Copy the **Client ID** and **Client Secret** to `FORGE_GITHUB_CLIENT_ID` and `FORGE_GITHUB_CLIENT_SECRET`.

### GitLab Setup

1.  Go to **User Settings > Applications**.
2.  **Name**: Forge CI.
3.  **Redirect URI**: `{FORGE_PUBLIC_URL}/api/v1/auth/callback/gitlab`.
4.  **Scopes**: Select `read_user` and `openid`.
5.  Copy the **Application ID** and **Secret** to `FORGE_GITLAB_CLIENT_ID` and `FORGE_GITLAB_CLIENT_SECRET`.

### Important Notes

*   `FORGE_PUBLIC_URL` must match the base URL used in the provider's configuration. It defaults to `http://localhost:8080`.
*   If your Forge instance is behind a proxy/TLS, ensure `FORGE_PUBLIC_URL` uses `https://`.
*   After logging in via SSO, Forge creates a session cookie. The `FORGE_UI_URL` determines where the browser is redirected after a successful handshake (usually your dashboard home).

---

## Distributed Deployment

When deploying Forge across multiple hosts, keep the following configuration rules in mind:

### 1. Scheduler Accessibility
Agents must be able to reach the scheduler via both HTTP and gRPC.
- Set `FORGE_SCHEDULER_URL` on agents to the scheduler's public address (e.g., `http://10.0.0.5:8080` or `https://forge.example.com`).
- Ensure port `50051` is open on the scheduler host if not using a unified load balancer.

### 2. Artifact Access (Minio/S3)
If using MinIO on the same host as the scheduler, the agent (on a different host) won't be able to reach `http://minio:9000`.
- Set `FORGE_S3_PUBLIC_URL` on the **scheduler** to an address reachable by the agents (e.g., `http://10.0.0.5:9000`).

### 3. Docker Networking
If running both Agent and Job containers in Docker:
- Set `FORGE_DOCKER_NETWORK` on the agent to the name of the network where it should place job containers.
- If the agent itself is in a container, it must use `--volumes-from <agent-container-name>` (handled automatically if `/.dockerenv` exists) to share the Docker socket, OR you must mount `/var/run/docker.sock`.

### 4. Secrets (Vault)
- Set `FORGE_VAULT_ADDR` on agents to an address reachable from the agent's host.