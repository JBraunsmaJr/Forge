# Configuration Reference

All Forge components are configured via environment variables. There are no configuration files.

---

## Scheduler

| Variable               | Default                  | Description                                                                                                                        |
|------------------------|--------------------------|------------------------------------------------------------------------------------------------------------------------------------|
| `FORGE_DB_URL`         | —                        | **Required.** PostgreSQL connection string. Format: `postgres://user:pass@host:port/dbname?sslmode=disable`                        |
| `FORGE_ROOT_TOKEN`     | —                        | Pre-set admin token for reproducible environments (compose, CI). If unset, a random token is generated and printed on first start. |
| `FORGE_BASE_URL`       | `http://localhost{addr}` | Public URL of this scheduler. Used to construct artifact download URLs for the local backend.                                      |
| `FORGE_ARTIFACT_STORE` | `local`                  | Artifact backend: `local` or `s3`.                                                                                                 |
| `FORGE_ARTIFACT_DIR`   | `/data/artifacts`        | Directory for local artifact storage.                                                                                              |
| `FORGE_S3_ENDPOINT`    | —                        | S3-compatible endpoint URL. Leave empty for AWS S3. Example: `http://minio:9000`.                                                  |
| `FORGE_S3_BUCKET`      | `forge-artifacts`        | S3 bucket name.                                                                                                                    |
| `FORGE_S3_REGION`      | `us-east-1`              | S3 region.                                                                                                                         |
| `FORGE_S3_ACCESS_KEY`  | —                        | S3 access key ID.                                                                                                                  |
| `FORGE_S3_SECRET_KEY`  | —                        | S3 secret access key.                                                                                                              |

---

## Agent

| Variable              | Default          | Description                                                                                                              |
|-----------------------|------------------|--------------------------------------------------------------------------------------------------------------------------|
| `FORGE_API_TOKEN`     | —                | **Required.** API token for scheduler authentication. Use a token with the `agent` role.                                 |
| `FORGE_VAULT_ADDR`    | —                | Vault server address. Required for steps that use `secrets:`. Example: `http://vault:8200`.                              |
| `FORGE_VAULT_TOKEN`   | —                | Vault authentication token.                                                                                              |
| `FORGE_AGENT_WS_ADDR` | `localhost:8082` | Public host:port for the agent's WebSocket debug terminal server. Set to the machine's LAN IP for remote browser access. |

---

## CLI

| Variable            | Default | Description                                                                                                       |
|---------------------|---------|-------------------------------------------------------------------------------------------------------------------|
| `FORGE_API_TOKEN`   | —       | API token for scheduler requests. Required for `submit`, `status`, `org`, `policy`, `token`, `project`, `secret`. |
| `FORGE_ORG`         | —       | Default org ID. Used as `--org` default for `submit`, `secret`, `policy`.                                         |
| `FORGE_VAULT_ADDR`  | —       | Vault address for `secret` commands.                                                                              |
| `FORGE_VAULT_TOKEN` | —       | Vault token for `secret` commands.                                                                                |

---

## Docker Compose Stack

The compose stack reads from a `.env` file (copy from `.env.example`):

| Variable           | Default                 | Description                                                                                 |
|--------------------|-------------------------|---------------------------------------------------------------------------------------------|
| `FORGE_ROOT_TOKEN` | `forge-dev-admin-token` | Admin token preset for all services. Change for staging/production.                         |
| `FORGE_AGENT_HOST` | `localhost`             | Hostname browsers use to reach agent WebSocket ports. Set to your LAN IP for remote access. |

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
- `FORGE_AGENT_WS_ADDR` — `{FORGE_AGENT_HOST}:8082` / `:8083`

---

## Port Reference

| Port | Service    | Description              |
|------|------------|--------------------------|
| 8080 | Scheduler  | HTTP API + Web UI        |
| 8082 | Agent 1    | WebSocket debug terminal |
| 8083 | Agent 2    | WebSocket debug terminal |
| 5432 | PostgreSQL | Database                 |
| 8200 | Vault      | Secrets storage          |
| 9000 | MinIO      | S3 API                   |
| 9001 | MinIO      | Web console              |