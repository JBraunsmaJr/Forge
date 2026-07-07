# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## What This Project Is

**Forge** is a distributed CI/CD pipeline runner built in Go. The core philosophy: pipeline logic belongs in source code (Go, Python, scripts), not YAML strings. Pipelines are first-class files with IDE support, real diffs, and independent testing.

Key capabilities: dynamic generator steps, pipeline chaining with artifact handoff, live debug terminal sessions into containers, org-wide policy injection, content-addressed caching, scoped secrets (project/org/global), and multi-backend artifact storage (local or S3-compatible).

## Commands

**Build:**
```bash
go build -o forge ./cmd/forge
```

**Run unit tests (fast, no Docker required):**
```bash
go test ./internal/cache/... ./internal/compiler/... ./internal/localenv/... ./internal/policy/... ./internal/secrets/... -race -count=1
```

**Run a single test:**
```bash
go test ./internal/compiler/... -run TestCompileStep -v
```

**Run integration tests (requires Docker):**
```bash
go test ./tests/integration/... -v -timeout=15m -count=1
```

**Local pipeline execution (no infrastructure):**
```bash
./forge run examples/docker-ci.json
```

**Full distributed stack:**
```bash
cp .env.example .env
docker compose up --build -d
# Web UI at http://localhost:8080
```

## Architecture

The system has three deployment modes that share the same binary:

1. **`forge run`** — Single-machine local mode. The CLI compiles the pipeline and hands it directly to the executor. No scheduler or agent needed.

2. **`forge scheduler`** — HTTP server (`:8080`). Owns the job queue in PostgreSQL, applies org policies, serves the Web UI, proxies debug terminal sessions, and handles artifact presigning.

3. **`forge agent`** — Stateless worker. Polls the scheduler for jobs via `POST /api/v1/jobs/lease` (backed by `SELECT … FOR UPDATE SKIP LOCKED` in Postgres), runs them in Docker, streams logs back, and sends heartbeats every 10 seconds. If heartbeats stop for 30 seconds, the scheduler reclaims the job.

### Data flow (distributed mode)

```
forge submit file.json
  → compiler.Compile() → POST /api/v1/runs
    → scheduler applies policies
    → jobs stored in postgres (no-dependency jobs → "queued", others → "pending")
      → agent leases a job
        → secrets fetched from Vault (never stored in DB)
        → artifact dependencies downloaded
        → docker run ... (logs streamed to scheduler → SSE to browser)
        → artifacts uploaded via presigned URL
        → POST /api/v1/jobs/{id}/complete
          → scheduler unblocks dependent jobs
```

### Key packages

| Package | Role |
|---|---|
| `cmd/forge` | CLI entry point and command routing |
| `internal/compiler` | YAML/JSON → `pipeline.Pipeline` IR |
| `internal/pipeline` | Canonical `Pipeline`, `Step`, `StepStatus` types — the shared IR |
| `internal/executor` | Docker container orchestration, CAS cache lookup, log streaming |
| `internal/scheduler` | HTTP server, job queue, SSE, auth, policy application |
| `internal/agent` | Job execution loop, heartbeat, artifact transfer |
| `internal/policy` | Org policy injection (static steps or transformer image) |
| `internal/secrets` | Vault KV v2 client with scoped resolution |
| `internal/artifacts` | Local/S3 backends behind a single `ArtifactStorer` interface |
| `internal/cache` | SHA-256 content-addressed store; hash = image + command + env + input file hashes |
| `internal/store` | PostgreSQL schema and all DB queries |
| `internal/log` | Structured JSON events with secret redaction and ANSI terminal rendering |

### Central types (`internal/pipeline/types.go`, `internal/api/types.go`)

Everything flows through `pipeline.Pipeline` / `pipeline.Step` as the canonical IR. The compiler translates external formats into this; the scheduler, agent, and CLI all consume it. `api.JobSpec` is the wire format sent from scheduler to agent for a single step execution.

### Policy engine (`internal/policy/`)

Two injection modes applied before jobs are stored:
- **Static** — policy declares steps that are prepended to every pipeline
- **Transformer** — policy specifies a Docker image whose stdin receives the full pipeline JSON and stdout returns a modified pipeline

Multiple policies chain sequentially. `ForbidOverride` prevents users from skipping mandatory steps.

### Job queue design

No external queue (Redis, RabbitMQ). PostgreSQL `SELECT … FOR UPDATE SKIP LOCKED` gives lock-free job claiming. The same pattern is used by Sidekiq and Que. Heartbeat + timeout gives at-least-once execution semantics.

### Artifact storage

Pre-signed URL pattern: the scheduler presigns an upload/download URL and returns it to the agent. The agent talks directly to the storage backend (local HTTP server or S3). The scheduler stays off the data path.

## Configuration

All configuration is via environment variables. The authoritative reference is `docs/Configuration.md`.

Critical variables:
- **Scheduler:** `FORGE_DB_URL` (required), `FORGE_ROOT_TOKEN`, `FORGE_ARTIFACT_STORE` (`local`/`s3`), `FORGE_S3_*`
- **Agent:** `FORGE_API_TOKEN` (required), `FORGE_VAULT_ADDR`, `FORGE_VAULT_TOKEN`
- **CLI:** `FORGE_API_TOKEN`, `FORGE_ORG`

## Pipeline definition format

Pipelines are YAML or JSON files compiled by `internal/compiler/compiler.go`. Key fields: `id`, `image`, `run` (inline shell) or `command` (exec array) or `script` (file path), `depends_on`, `env`, `inputs` (for cache hashing), `secrets`, `artifacts` (upload/download), `type` (`task`/`generator`/`pipeline`).

See `docs/Pipeline-Reference.md` and `examples/` for full examples.
