# CLI Reference

All commands use the `forge` binary. Build it with `go build -o forge ./cmd/forge`.

---

## Global Environment Variables

| Variable            | Description                                          |
|---------------------|------------------------------------------------------|
| `FORGE_API_TOKEN`   | Authentication token for scheduler requests.         |
| `FORGE_ORG`         | Default org ID for commands that accept `--org`.     |
| `FORGE_SCHEDULER_URL`| Default scheduler URL (e.g. `https://forge.dev`).    |
| `FORGE_VAULT_ADDR`  | Vault server address (e.g. `http://localhost:8200`). |
| `FORGE_VAULT_TOKEN` | Vault authentication token.                          |

---

## Pipeline Commands

### `forge run`

Run a pipeline locally without a scheduler.

```bash
forge run <pipeline-file> [--watch] [--secret NAME=VAL] [--env-file .env]
```

Uses the local Docker daemon. Artifacts stored at `.forge/artifacts/`. No authentication required.

| Flag         | Description                                                      |
|--------------|------------------------------------------------------------------|
| `--watch`    | Re-run the pipeline automatically when files in the workspace change. |
| `--secret`   | Inject a secret value (bypasses Vault).                          |
| `--env-file` | Load environment variables from a file.                          |

### `forge submit`

Submit a pipeline to the distributed scheduler.

```bash
forge submit <pipeline-file> [--org <org-id>]
```

| Flag         | Description                                         |
|--------------|-----------------------------------------------------|
| `--org <id>` | Org ID for policy injection. Overrides `FORGE_ORG`. |

**Git Metadata Detection:**
When running `forge submit` from a Git repository, Forge automatically detects the current branch, tag (if any), and commit SHA. This metadata is sent to the scheduler and used for:
- Evaluating `tag()` conditions in pipelines.
- Injecting `FORGE_REF`, `FORGE_COMMIT_SHA`, and `FORGE_COMMIT_TAG` environment variables into jobs.
- Displaying commit information in the Web UI.

Outputs a run ID. Use `forge status <run-id>` to poll completion.

### `forge validate`

Validate a pipeline file without running it. Performs deep linting, cycle detection, and image name validation.

```bash
forge validate <pipeline-file>
```

### `forge rerun`

Rerun a previous run, inheriting its metadata and policies.

```bash
forge rerun <run-id>
```

### `forge status`

Check the status of a submitted run.

```bash
forge status <run-id>
```

### `forge prune`

Manually trigger pruning of old runs and artifacts.

```bash
forge prune [age]
```

- `age`: Optional duration (e.g. `7d`, `24h`, `30m`) or number of days. Defaults to `30d`.

---

## Secret Commands

Secrets are stored in Vault. Resolution order at runtime: project → org → global.

### `forge secret set`

```bash
forge secret set <NAME> <VALUE> [--org <id>] [--project <id>]
```

```bash
forge secret set DATABASE_URL postgres://...          # global
forge secret set DEPLOY_TOKEN ghp_... --org abc123    # org-scoped
forge secret set API_KEY key123 --project xyz789      # project-scoped
```

### `forge secret get`

```bash
forge secret get <NAME> [--org <id>] [--project <id>]
```

### `forge secret list`

```bash
forge secret list [--org <id>] [--project <id>]
```

---

## Organisation Commands

Orgs group projects and policies. Policies defined on an org apply to every pipeline submitted under that org.

### `forge org create`

```bash
forge org create <name>
```

Prints the org ID. Set `FORGE_ORG` to this value for subsequent commands.

### `forge org list`

```bash
forge org list
```

---

## Policy Commands

Policies inject steps into every pipeline submitted under an org. The two types are static (inject specific steps) and transformer (run a Docker image that modifies the pipeline).

### `forge policy create`

```bash
forge policy create <name> --org <org-id> --description "<text>"
```

### `forge policy transformer`

Register a transformer policy (Docker image that transforms the pipeline):

```bash
forge policy transformer <name> \
  --org <org-id> \
  --image forge-security-policies:latest \
  --command python3 /policies/container-security.py \
  --description "Injects Trivy scan after docker build steps"
```

### `forge policy list`

```bash
forge policy list --org <org-id>
```

### `forge policy delete`

```bash
forge policy delete <policy-id> --org <org-id>
```

---

## Token Commands

API tokens authenticate CLI, agent, and browser access to the scheduler.

Roles:
- `admin` — full access to all scheduler endpoints
- `agent` — restricted to job queue operations (lease, heartbeat, complete, log streaming)

### `forge token create`

```bash
forge token create <name> [--role admin|agent]
```

The raw token value is shown exactly once. Store it immediately.

### `forge token list`

```bash
forge token list
```

### `forge token revoke`

```bash
forge token revoke <token-id>
```

---

## Project Commands

Projects connect a source repository to an org and generate webhook URLs.

### `forge project add`

```bash
forge project add <name> <repo-url> [--org <org-id>] [--token <scm-token>] [--pipeline <path>]
```

Prints the webhook URL and a one-time webhook secret. Configure these in your SCM provider's webhook settings.

| Flag                | Description                                                       |
|---------------------|-------------------------------------------------------------------|
| `--org <id>`        | Associate this project with an org (inherits policies).           |
| `--token <token>`   | SCM token for fetching pipeline files from private repos.         |
| `--pipeline <path>` | Pipeline file path in the repo (default: `.forge/pipeline.json`). |

### `forge project update`

Update an existing project's configuration.

```bash
forge project update <project-id-or-name> [--name <new-name>] [--repo <new-repo-url>] [--token <new-scm-token>] [--pipeline <new-path>]
```

### `forge project schedule`

Configure or remove a cron schedule for a project.

```bash
forge project schedule <project-id-or-name> [--cron "<expression>"] [--pipeline <path>] [--delete]
```

- `--cron`: Standard 5-field cron syntax (e.g., `"0 2 * * *"`).
- `--pipeline`: Path to the pipeline file to run on schedule.
- `--delete`: Remove the schedule for this project.

### `forge project list`

```bash
forge project list [--org <org-id>]
```

---

## Report Commands

Convert test-runner output into Forge test reports (used by `test_report:`
steps and `split:` shard planning), or make raw output human-readable.

```bash
# Aggregate `go test -json` output into a Forge test report.
# Output defaults to .forge/test-report.json when omitted.
forge report from-go-test <input.json> [output.json]

# Same, for pytest's --junit-xml output.
forge report from-pytest <junit.xml> [output.json]

# Stream `go test -json` from stdin as classic human-readable output,
# for legible CI logs. Non-JSON lines pass through untouched.
go test -v -json ./... 2>&1 | tee raw.json | forge report stream-go-test
```

## Agent Command

### `forge agent`

Start an agent that polls the scheduler for work.

```bash
forge agent [scheduler-url]
```

Default scheduler URL: `http://localhost:8080`

**Environment variables:**

| Variable              | Default          | Description                                                                                                     |
|-----------------------|------------------|-----------------------------------------------------------------------------------------------------------------|
| `FORGE_API_TOKEN`     | —                | Required. Agent authentication token.                                                                           |
| `FORGE_VAULT_ADDR`    | —                | Vault address for secret fetching.                                                                              |
| `FORGE_VAULT_TOKEN`   | —                | Vault token.                                                                                                    |

---

## Scheduler Command

### `forge scheduler`

Start the scheduler HTTP server.

```bash
forge scheduler [address]
```

Default address: `:8080`

**Environment variables:**

| Variable               | Description                                                                                              |
|------------------------|----------------------------------------------------------------------------------------------------------|
| `FORGE_DB_URL`         | Required. PostgreSQL connection string.                                                                  |
| `FORGE_ROOT_TOKEN`     | Pre-set admin token (for compose/CI environments). If unset, a random token is generated on first start. |
| `FORGE_BASE_URL`       | Public scheduler URL (for artifact download links).                                                      |
| `FORGE_ARTIFACT_STORE` | `local` (default) or `s3`.                                                                               |
| `FORGE_ARTIFACT_DIR`   | Local artifact storage directory (default: `/data/artifacts`).                                           |
| `FORGE_S3_ENDPOINT`    | S3-compatible endpoint URL. Empty = AWS S3.                                                              |
| `FORGE_S3_PUBLIC_URL`  | Public URL for artifacts when S3 endpoint is internal.                                                   |
| `FORGE_S3_BUCKET`      | S3 bucket name.                                                                                          |
| `FORGE_S3_REGION`      | S3 region.                                                                                               |
| `FORGE_S3_ACCESS_KEY`  | S3 access key ID.                                                                                        |
| `FORGE_S3_SECRET_KEY`  | S3 secret access key.                                                                                    |
| `FORGE_RUN_RETENTION`  | How long to keep job runs and artifacts (e.g. `7d`, `30m`). Defaults to `30d`.                           |
| `FORGE_RUN_RETENTION_INTERVAL` | How often to run the background retention worker. Defaults to `24h`.                             |