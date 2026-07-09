# Pipeline Reference

Forge pipelines are YAML or JSON files, typically stored at `.forge/pipeline.yml` in your repository root.

---

## File Format

Pipelines can be written in either YAML (`.yml` or `.yaml`) or JSON (`.json`). YAML is recommended for human-authored files; JSON is useful when pipelines are generated programmatically.

```yaml
name: my-pipeline   # required — human-readable run name

steps:
  - id: first-step
    image: alpine:latest
    run: echo "hello"

  - id: second-step
    image: alpine:latest
    depends_on: [first-step]
    run: echo "world"
```

---

## Step Fields

### Required Fields

| Field   | Type   | Description                                                                      |
|---------|--------|----------------------------------------------------------------------------------|
| `id`    | string | Unique step identifier. Used in `depends_on` references and shown in the Web UI. |
| `image` | string | Docker image to run this step in. Not required for `type: pipeline` steps.       |

At least one of `run`, `command`, or `script` is required for non-pipeline steps.

### Execution Fields

| Field           | Type     | Description                                                                                              |
|-----------------|----------|----------------------------------------------------------------------------------------------------------|
| `run`           | string   | Shell command, passed to `sh -c`. Supports multiline with `\|`.                                          |
| `command`       | string[] | Explicit argv, bypasses shell. Use when you need exact argument control.                                 |
| `script`        | string   | Path to a script file in the workspace. Interpreter inferred from extension.                             |
| `workdir`       | string   | Working directory inside the container. Default: `/workspace`.                                           |
| `image`         | string   | Docker image. Pulled fresh if not cached locally.                                                        |
| `docker_socket` | bool     | Mount host Docker socket into the container. Required for steps that run `docker build` or `docker run`. |

### The `script:` Field

The `script:` field runs an external file from the workspace. The interpreter is inferred from the file extension:

| Extension      | Interpreter |
|----------------|-------------|
| `.py`          | `python3`   |
| `.sh`, `.bash` | `sh`        |
| `.js`, `.mjs`  | `node`      |
| `.rb`          | `ruby`      |
| `.ts`          | `ts-node`   |
| (other)        | `sh`        |

```yaml
- id: generate-matrix
  type: generator
  image: python:3.12-slim
  script: scripts/ci/generate-matrix.py   # runs python3 /workspace/scripts/ci/generate-matrix.py
```

Paths are relative to the workspace root. The script runs inside the container with the workspace mounted at `/workspace`.

### Dependency and Flow Fields

| Field        | Type     | Description                                                                                      |
|--------------|----------|--------------------------------------------------------------------------------------------------|
| `depends_on` | string[] | Step IDs this step must wait for. Forge computes the DAG and runs independent steps in parallel. |
| `timeout`    | string   | Maximum duration. Parsed as Go duration: `5m`, `1h30m`, `45s`. Default: 30 minutes.              |
| `type`       | string   | Step type: `task` (default), `generator`, or `pipeline`.                                         |

### Environment and Secrets

| Field     | Type     | Description                                                                   |
|-----------|----------|-------------------------------------------------------------------------------|
| `env`     | map      | Environment variables injected into the container.                            |
| `secrets` | string[] | Secret names fetched from Vault and injected as env vars. Never stored in DB. |

Secret resolution order (highest priority first):
1. Project-scoped: `secret set NAME value --project <id>`
2. Org-scoped: `secret set NAME value --org <id>`
3. Global: `secret set NAME value`
4. Legacy path (backward compatibility)

### Artifacts

```yaml
artifacts:
  upload:
    - path: dist/myapp          # path relative to workspace, glob patterns supported
      name: app-binary          # logical name for download (defaults to basename)
    - path: dist/*.so           # glob: each file uploaded with its basename as name

  download:
    - name: app-binary          # logical name from a prior step's upload
      dest: dist/myapp          # destination path in workspace
```

Artifacts are stored in the configured backend (local filesystem or S3-compatible) and shared across agents. A step on agent-1 can upload an artifact; a step on agent-2 can download it.

---

## Step Types

### `type: task` (default)

A standard step that runs a command in a Docker container.

```yaml
- id: test
  image: golang:1.24-alpine
  run: go test ./... -race
```

### `type: generator`

A generator step runs code that **emits new step definitions** as a JSON array to stdout. Forge adds those steps to the current run and executes them. This enables runtime job generation — no static matrix required.

```yaml
- id: matrix-generator
  type: generator
  image: python:3.12-slim
  script: scripts/ci/generate-matrix.py
```

The script's stdout must be a valid JSON array of step definition objects. Stderr is captured as log output. Any subsequent step with `depends_on: [matrix-generator]` will wait for the generator AND all of its emitted children.

**Generator script output format:**
```json
[
  {
    "id": "build-linux-amd64",
    "image": "golang:1.24-alpine",
    "depends_on": ["matrix-generator"],
    "env": {"GOOS": "linux", "GOARCH": "amd64"},
    "run": "go build -o dist/myapp-linux-amd64 ./cmd/myapp",
    "artifacts": {
      "upload": [{"path": "dist/myapp-linux-amd64", "name": "binary-linux-amd64"}]
    }
  }
]
```

### `type: pipeline`

A pipeline step compiles and submits another pipeline as a child run. The agent acts as an orchestrator — no container is launched. Variables are injected as environment variables into every step of the child pipeline.

```yaml
- id: deploy-staging
  type: pipeline
  pipeline: .forge/deploy.yml    # path relative to workspace root
  wait: true                     # block until child run completes (default: true)
  depends_on: [build]
  variables:
    ENVIRONMENT: staging
    REPLICAS: "3"
  artifacts_send:
    - container-image            # artifact name from current run
  artifacts_receive:
    - deployed-endpoint          # artifact from child run, added to current run
```

**Pipeline step fields:**

| Field               | Type     | Description                                                                                  |
|---------------------|----------|----------------------------------------------------------------------------------------------|
| `pipeline`          | string   | Path to pipeline file, relative to workspace root.                                           |
| `wait`              | bool     | If true (default), block until child run reaches a terminal state.                           |
| `variables`         | map      | Env vars injected into every step of the child pipeline, overriding the step's own env.      |
| `artifacts_send`    | string[] | Artifact names from the parent run copied to the child run's context before it starts.       |
| `artifacts_receive` | string[] | Artifact names from the child run copied back into the parent run after the child completes. |

---

## Complete Example

```yaml
name: build-test-deploy

steps:
  # Parallel: test and lint run simultaneously
  - id: test
    image: golang:1.24-alpine
    timeout: 10m
    run: go test ./... -race -coverprofile=coverage.out

  - id: lint
    image: golangci/golangci-lint:latest
    timeout: 5m
    run: golangci-lint run ./...

  # Build waits for both test AND lint
  - id: build
    image: golang:1.24-alpine
    depends_on: [test, lint]
    timeout: 10m
    env:
      CGO_ENABLED: "0"
      GOOS: linux
    run: go build -ldflags="-s -w" -o dist/app ./cmd/app
    artifacts:
      upload:
        - path: dist/app
          name: app-binary

  # Containerize downloads the binary built above
  - id: containerize
    image: docker:27-cli
    docker_socket: true
    depends_on: [build]
    timeout: 10m
    artifacts:
      download:
        - name: app-binary
          dest: dist/app
    run: |
      chmod +x dist/app
      docker build -t myapp:${GIT_SHA:-dev} .
    artifacts:
      upload:
        - path: image-digest.txt
          name: image-digest

  # Deploy to staging via a reusable child pipeline
  - id: deploy-staging
    type: pipeline
    pipeline: .forge/deploy.yml
    depends_on: [containerize]
    variables:
      ENVIRONMENT: staging
      IMAGE_TAG: "${GIT_SHA:-dev}"
    artifacts_send: [image-digest]
    artifacts_receive: [staging-endpoint]

  # Integration tests against the deployed staging environment
  - id: integration-tests
    image: python:3.12-slim
    depends_on: [deploy-staging]
    timeout: 15m
    secrets: [STAGING_API_KEY]
    artifacts:
      download:
        - name: staging-endpoint
          dest: /tmp/endpoint.txt
    run: |
      pip install --quiet pytest httpx
      ENDPOINT=$(cat /tmp/endpoint.txt)
      pytest tests/integration/ --base-url="$ENDPOINT"

  # Only deploy to production if integration tests pass
  - id: deploy-production
    type: pipeline
    pipeline: .forge/deploy.yml
    depends_on: [integration-tests]
    variables:
      ENVIRONMENT: production
      IMAGE_TAG: "${GIT_SHA:-dev}"
    artifacts_send: [image-digest]
```

---

## YAML Tips

### Multiline scripts

```yaml
run: |
  echo "line 1"
  echo "line 2"
  echo "line 3"
```

### Inline sequences for dependencies and secrets

```yaml
depends_on: [test, lint, security-scan]
secrets: [GITHUB_TOKEN, NPM_TOKEN, DEPLOY_KEY]
```

### Environment variable interpolation

Forge does not interpolate `${}` in pipeline YAML at compile time. Values like `${GIT_SHA:-dev}` are passed literally to `sh -c` and expanded by the shell at runtime. This is intentional — it keeps pipeline files static and version-stable.

---

## Validation

Validate a pipeline file without running it:

```bash
./forge validate .forge/pipeline.yml
./forge validate .forge/pipeline.json
```

This catches syntax errors, missing required fields, circular dependencies, and unknown step types.

---

## Running Pipelines

### Local execution

```bash
./forge run .forge/pipeline.yml
```

### Distributed execution (requires scheduler)

```powershell
$env:FORGE_API_TOKEN = 'fgt_...'
./forge.exe submit .forge/pipeline.yml

# With org (enables policy injection)
$env:FORGE_ORG = '<org-id>'
./forge.exe submit .forge/pipeline.yml

# Check status
./forge.exe status <run-id>
```