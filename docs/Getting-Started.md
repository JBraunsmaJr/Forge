# Getting Started with Forge

This guide walks through every deployment mode from "run one pipeline on my laptop" to "team-wide distributed CI/CD." Pick the one that matches where you are.

---

## Prerequisites

| Tool                                                  | Version | Purpose                           |
|-------------------------------------------------------|---------|-----------------------------------|
| [Go](https://go.dev/dl/)                              | 1.22+   | Building the Forge binary         |
| [Node.js](https://nodejs.org/)                       | 20+     | Building the Web UI assets        |
| [Docker Desktop](https://docs.docker.com/get-docker/) | Latest  | Running job containers            |
| [Git](https://git-scm.com/)                           | Any     | Webhook git caching               |
| PostgreSQL                                            | 16+     | Job queue (distributed mode only) |

---

## Installation

### CLI Binary

Install the Forge CLI on Linux, macOS, or Windows using the one-liner scripts:

**Linux / macOS:**
```bash
curl -sSL https://forge.dev/install.sh | bash
```

**Windows (PowerShell):**
```powershell
iwr -useb https://forge.dev/install.ps1 | iex
```

### Self-Hosted Core (Docker)

Deploy the Forge scheduler and core services (Postgres, Vault, MinIO) to your own server:

```bash
curl -sSL https://forge.dev/deploy.sh | bash
```

This will:
1.  Check for Docker and Curl.
2.  Create a `forge-server` directory.
3.  Download the production Docker Compose stack.
4.  Generate secure tokens for `FORGE_ROOT_TOKEN` and `FORGE_AGENT_TOKEN`.
5.  Launch the services in the background.

---

## Mode 1: Local Runs

No infrastructure needed. The `forge run` command executes a pipeline directly on your machine using your local Docker daemon.

```bash
# Build
git clone https://github.com/JBraunsmaJr/forge
cd forge

# 1. Build Web UI
cd ui && npm install && npm run build
cd ..

# 2. Build Forge
go build -o forge ./cmd/forge

# Run a pipeline
./forge run examples/docker-ci.json

# Run with automatic re-run on file changes
./forge run examples/docker-ci.json --watch

# Validate without running
./forge validate examples/docker-ci.json
```

**What happens:** Forge compiles the pipeline, builds the DAG, and executes each step in a Docker container sequentially (respecting dependencies). Logs stream to your terminal. Artifacts are stored locally at `.forge/artifacts/`.

**Limitations:** Local mode is single-machine and sequential. For parallel execution across multiple agents, use distributed mode.

---

## Mode 2: Single-Machine Distributed Stack

One machine runs the scheduler and one or more agents. This is the most common setup for small teams.

### Step 1: Start PostgreSQL

```bash
docker run -d --name forge-db \
  -p 5432:5432 \
  -e POSTGRES_DB=forge \
  -e POSTGRES_USER=forge \
  -e POSTGRES_PASSWORD=forge \
  postgres:16-alpine
```

### Step 2: Start Vault (for secrets)

```bash
docker run -d --name forge-vault \
  -p 8200:8200 \
  -e VAULT_DEV_ROOT_TOKEN_ID=forge-dev-token \
  -e VAULT_DEV_LISTEN_ADDRESS=0.0.0.0:8200 \
  hashicorp/vault
```

### Step 3: Build Forge

```bash
# Build UI
cd ui && npm install && npm run build
cd ..

# Build Binary
go build -o forge ./cmd/forge      # Linux/Mac
go build -o forge.exe ./cmd/forge  # Windows (PowerShell)
```

### Step 4: Start the Scheduler

```powershell
$env:FORGE_DB_URL = "postgres://forge:forge@localhost:5432/forge?sslmode=disable"
./forge.exe scheduler
```

On first start, the scheduler creates an admin token and prints it **once**:

```
┌──────────────────────────────────────────────────────────┐
│ First run — root admin token created                     │
│                                                          │
│ Token:  fgt_a3b4c5d6e7f8...                             │
│                                                          │
│ ⚠ This is shown ONCE. Store it securely.                │
└──────────────────────────────────────────────────────────┘

Set for this session:
  $env:FORGE_API_TOKEN = 'fgt_a3b4c5d6e7f8...'
```

Save this token. Open http://localhost:8080 and enter it when prompted. You can now view your runs, manage orgs, and monitor runner health from the dashboard.

### Step 5: Create an Agent Token

Agents should use a token with the `agent` role (limited to job queue operations):

```powershell
$env:FORGE_API_TOKEN = 'fgt_...'   # your admin token
./forge.exe token create my-agent --role agent

# Output:
#   Token (shown once — store this now):
#   fgt_<agent-token>
```

### Step 6: Start an Agent

```powershell
$env:FORGE_API_TOKEN   = 'fgt_<agent-token>'
$env:FORGE_VAULT_ADDR  = "http://localhost:8200"
$env:FORGE_VAULT_TOKEN = "forge-dev-token"

./forge.exe agent
```

### Step 7: Create a Default Org and Submit a Pipeline

```powershell
$env:FORGE_API_TOKEN = 'fgt_<admin-token>'

# Create an org (groups policies and projects)
./forge.exe org create default

# Submit a pipeline
$env:FORGE_ORG = '<org-id-from-above>'
./forge.exe submit examples/docker-ci.json
```

Open http://localhost:8080 to watch the pipeline run in real time.

---

## Mode 3: Docker Compose Stack (Recommended for Teams)

The compose stack provides everything pre-configured: scheduler, 2 agents, PostgreSQL, Vault, and MinIO (S3-compatible artifact storage).

### Step 1: Configure

```bash
cp .env.example .env
# Edit .env if needed — defaults work for local use
```

`.env.example` contains:
```bash
# Admin token (preset for reproducible dev environments)
FORGE_ROOT_TOKEN=forge-dev-admin-token

# Agent WebSocket host (change to your LAN IP for remote browser access)
FORGE_AGENT_HOST=localhost
```

### Step 2: Start the Stack

```bash
docker compose up --build -d
```

This builds the Forge image, starts all services, and runs the `init` container which:
- Creates a `default` org
- Registers the container-security and language-security policies
- Seeds example Vault secrets
- Prints a ready summary

Watch initialization:
```bash
docker compose logs -f init
```

### Step 3: Connect

| Service       | URL                   | Credentials                                |
|---------------|-----------------------|--------------------------------------------|
| Forge Web UI  | http://localhost:8080 | Token: `forge-dev-admin-token`             |
| Vault UI      | http://localhost:8200 | Token: `forge-dev-token`                   |
| MinIO Console | http://localhost:9001 | User: `forgeadmin` / Pass: `forgepassword` |

### Step 4: Submit from Outside the Stack

Install the Forge CLI binary on your development machine and point it at the compose stack:

```powershell
$env:FORGE_API_TOKEN = 'forge-dev-admin-token'
$env:FORGE_ORG       = '<org-id-from-init-output>'

# Submit a pipeline from your local repo
./forge.exe submit .forge/pipeline.yml
```

### Stopping and Resetting

```bash
# Stop (preserves data)
docker compose down

# Full reset (wipes PostgreSQL and MinIO data)
docker compose down -v
```

---

## Mode 4: Production Deployment

For production, replace the compose stack's preset tokens with randomly generated ones and connect to production infrastructure.

### Environment Variables to Change

```bash
# Remove FORGE_ROOT_TOKEN — the scheduler auto-generates a token on first start
# FORGE_ROOT_TOKEN=               # comment this out

# Point to production Vault
FORGE_VAULT_ADDR=https://vault.internal.example.com
FORGE_VAULT_TOKEN=<production-vault-token>

# Point to production S3
FORGE_ARTIFACT_STORE=s3
FORGE_S3_ENDPOINT=                        # empty = AWS S3
FORGE_S3_PUBLIC_URL=https://artifacts.example.com
FORGE_S3_BUCKET=my-forge-artifacts
FORGE_S3_REGION=us-east-1
FORGE_S3_ACCESS_KEY=<access-key>
FORGE_S3_SECRET_KEY=<secret-key>

# Configure retention (defaults to 30 days)
FORGE_RUN_RETENTION=30d
FORGE_RUN_RETENTION_INTERVAL=24h

# Scheduler public URL (for artifact download links)
FORGE_BASE_URL=https://forge.internal.example.com
```

### Retention and Cleanup

Forge automatically cleans up old job runs and their associated artifacts in storage.

- **Background Worker**: The scheduler runs a background process (controlled by `FORGE_RUN_RETENTION_INTERVAL`) that prunes any run older than `FORGE_RUN_RETENTION`.
- **Manual Pruning**: You can manually trigger a cleanup via the CLI: `forge prune 7d`.
- **Storage Cleanup**: Pruning a run automatically deletes its files from Local or S3 storage.

### TLS / Reverse Proxy

Run a reverse proxy (nginx, Caddy, Traefik) in front of the scheduler. Forge itself does not handle TLS. 

See the [HTTPS and Reverse Proxy Guide](HTTPS.md) for detailed setup instructions including Cloudflare and DuckDNS support.

Caddy example:
```
forge.internal.example.com {
  reverse_proxy scheduler:8080
}
```

### Agent WebSocket for Debug Sessions

Agent debug terminals use a reverse connection model. All WebSocket traffic is routed through the scheduler.

1.  Browser connects to the scheduler's address (`/api/v1/debug/{id}/ws`).
2.  Scheduler proxies the session to the appropriate agent.
3.  No agent ports need to be exposed to the internet.
4.  Ensure your reverse proxy (if any) allows WebSocket upgrades for the scheduler.

---

## Setting Up Secrets

Secrets are stored in Vault and fetched by agents at execution time. They are never stored in the scheduler or database.

```powershell
$env:FORGE_VAULT_ADDR  = "http://localhost:8200"
$env:FORGE_VAULT_TOKEN = "forge-dev-token"

# Global secrets (available to all runs)
./forge.exe secret set GITHUB_TOKEN ghp_...

# Org-scoped (shared by all projects in the org)
./forge.exe secret set DATADOG_API_KEY key123 --org <org-id>

# Project-scoped (highest priority, overrides org and global)
./forge.exe secret set DEPLOY_TOKEN ghp_... --project <project-id>
```

Reference secrets in your pipeline:
```yaml
- id: deploy
  image: alpine:latest
  secrets: [DEPLOY_TOKEN, DATADOG_API_KEY]
  run: echo "Token: $DEPLOY_TOKEN"  # injected as env var
```

---

## Connecting a Repository (Webhooks)

Register a repository to trigger pipelines automatically on `git push`:

```powershell
# Register the project
./forge.exe project add my-service https://github.com/myorg/my-service.git

# Output:
#   GitHub webhook URL:
#   http://your-server:8080/api/v1/webhook/github/<project-id>
#   Webhook secret: forge_wh_...
```

In GitHub: **Settings → Webhooks → Add webhook**
- Payload URL: the URL printed above
- Secret: the secret printed above
- Events: Just the push event

For testing without HTTPS, use the **Manual Trigger** button in the Web UI on the project page.

---

## Next Steps

- [Pipeline Reference](Pipeline-Reference.md) — learn every pipeline field
- [Examples Guide](Examples.md) — dynamic matrices, monorepos, pipeline chaining
- [Policy Engine](Policies.md) — enforce org-wide security rules
- [Debug Sessions](Debugging.md) — live terminal for failing jobs