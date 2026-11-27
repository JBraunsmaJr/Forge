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
| `image` | string | Container image to run this step in. May contain `${{ env.VAR }}`; see [Running an image built earlier in the run](#running-an-image-built-earlier-in-the-run). Not required for `type: pipeline`, `type: approval`, `type: docker_build`, or `type: docker_publish` steps. |
| `uses`  | string | Path to a step template file. Reuses definition from another file.               |

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

#### `test_report`

`test_report: <path>` names a workspace-relative JSON file the step produces
describing per-file test timings (generate it with `forge report
from-go-test` / `from-pytest`). The agent uploads it after the job; it feeds
the flaky-test view and drives `split:` shard planning.

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
  script: scripts/ci/generate_matrix.py   # runs python3 /workspace/scripts/ci/generate_matrix.py
```

Paths are relative to the workspace root. The script runs inside the container with the workspace mounted at `/workspace`.

### Dependency and Flow Fields

| Field        | Type     | Description                                                                                      |
|--------------|----------|--------------------------------------------------------------------------------------------------|
| `depends_on` | string[] | Step IDs this step must wait for. Forge computes the DAG and runs independent steps in parallel. |
| `inputs`     | string[] | Glob patterns that affect this step's cache key. If undefined, caching is disabled.               |
| `timeout`    | string   | Maximum duration. Parsed as Go duration: `5m`, `1h30m`, `45s`. Default: 30 minutes.              |
| `type`       | string   | Step type: `task` (default), `generator`, `pipeline`, or `approval`.                             |
| `condition`  | string   | CEL expression that must be true for the step to run (e.g., `success()`, `failure()`).            |
| `changes`    | string[] | File patterns. The step is skipped when none of the changed files matches. See [Running a step only when files changed](#running-a-step-only-when-files-changed-changes). |
| `always_run` | bool     | Shorthand for `condition: always()`. Step runs even if dependencies fail.                        |
| `matrix`     | map      | Parallel execution with multiple values. Expands into multiple steps.                            |

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

### Build Numbers

Every run is assigned a build number: a stable, configurable identifier available to every step in that run as two environment variables:

| Variable              | Description                                                              |
|------------------------|--------------------------------------------------------------------------|
| `FORGE_BUILD_NUMBER`   | The rendered build number, e.g. `2026-08.14` or `2.4.87`.                |
| `FORGE_BUILD_COUNTER`  | The raw monotonic counter the build number was rendered from, e.g. `14`. |

Both are resolvable anywhere `${{ env.* }}` already works (a step's `image:`, `release:`, `matrix:`, `docker_build:` and `docker_publish:` blocks, and `env:`/`secrets:` steps), and are set on every step in the run — including steps expanded from a `matrix:` block, which all share their parent run's one build number.

**Local execution.** Running `forge run` with no scheduler/project context sets `FORGE_BUILD_NUMBER=local` and `FORGE_BUILD_COUNTER=0` so pipelines referencing these variables don't fail outside CI. `local` is not a value any configured format can ever render, so it's always distinguishable from a real, scheduler-assigned build number.

**Reruns and child pipelines.** Rerunning a run reuses that run's original build number rather than minting a new one. A child run created by a `type: pipeline` step inherits its parent run's build number.

#### Format strings

Each (project, pipeline name) has a configurable format string, defaulting to `%year%-%month%.%counter%`. A format combines literal text with tokens:

| Token          | Renders as                                             |
|----------------|---------------------------------------------------------|
| `%counter%`    | The raw counter, e.g. `87`.                              |
| `%counter:N%`  | The counter, zero-padded to N digits, e.g. `%counter:3%` → `009`. |
| `%year%`       | Current UTC year, e.g. `2026`.                           |
| `%month%`      | Current UTC month, zero-padded, e.g. `08`.                |
| `%day%`        | Current UTC day of month, zero-padded, e.g. `04`.          |
| `%major%`      | The pipeline's configured major version.                  |
| `%minor%`      | The pipeline's configured minor version.                  |

A format must contain exactly one `%counter%`/`%counter:N%` token; unknown or malformed tokens are rejected when the format is saved, not when a run is submitted.

The counter is scoped to (project, pipeline, version key), where the version key is the format's rendered output with the counter token removed. Whenever a run's version key differs from the last-recorded one for that scope (e.g. the calendar month rolled over, or the major/minor version changed), the counter for the new version key restarts at 1. A format with no date or major/minor token (e.g. `1.4.%counter%`) has a version key that never changes, so it keeps a single, ever-incrementing counter — identical to a pipeline with no versioning scheme at all.

Examples:

| Format                        | Renders as (counter=14, major=2, minor=4, 2026-08-04 UTC) |
|--------------------------------|-------------------------------------------------------------|
| `%year%-%month%.%counter%`     | `2026-08.14`                                                 |
| `%major%.%minor%.%counter%`    | `2.4.14`                                                      |
| `1.4.%counter%`                | `1.4.14`                                                      |
| `%counter:3%`                  | `014`                                                         |

#### Major/minor version management

The major/minor version consumed by `%major%`/`%minor%` can be set explicitly, or derived automatically from a pushed git tag matching `vMAJOR.MINOR` or `vMAJOR.MINOR.PATCH` — restricted to a configurable branch/ref filter (default: the project's default branch) so a tag pushed on a stale or feature branch can't change the version used by mainline builds. An explicit set can override a tag-derived value at any time, and vice versa: whichever happened most recently wins. Every change is recorded with actor (user, or "tag push" plus the tag ref) and timestamp in the audit log.

#### CLI

```bash
# View / set the build-number format for a pipeline
forge project get-build-format <project-id> <pipeline-name>
forge project set-build-format <project-id> <pipeline-name> '%year%-%month%.%counter%'

# View / set the major/minor version
forge project get-version <project-id> <pipeline-name>
forge project set-version <project-id> <pipeline-name> <major> <minor>

# Restrict which branches' tag pushes may update the tag-derived version
forge project set-version-tag-filter <project-id> <pipeline-name> <branch-filter>

# A run's build number is included in its status output
forge status <run-id>
```

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
    - name: forge-*             # wildcard: downloads all artifacts matching the pattern
      dest: bin/                # destination directory
```

Artifacts are stored in the configured backend (local filesystem or S3-compatible) and shared across agents. A step on agent-1 can upload an artifact; a step on agent-2 can download it. Both `upload` and `download` support wildcard matching for managing groups of files.

### Caching

Forge supports Content Addressable Storage (CAS) for pipeline steps. If a step's inputs haven't changed, Forge can skip execution and reuse the result from a previous run.

To enable caching, you must explicitly declare the files that affect the step using the `inputs` field:

```yaml
- id: build-ui
  image: node:22-alpine
  inputs:
    - ui/**
    - package.json
    - package-lock.json
  run: |
    cd ui
    npm install
    npm run build
```

The "task hash" for a step is computed from:
- The container image name
- The command and environment variables (see below)
- The SHA-256 hash of all files matched by the `inputs` globs

#### Determinism and Environment Variables

A step is skipped when its task hash matches an earlier successful run, on any agent, for any commit. For that to work, the hash must not contain values that differ from run to run without changing what the step produces. Forge sorts the variables it sets into two groups.

**Never part of the hash.** These describe where and when the step runs:

- `FORGE_EVENT`: The event type (e.g., `push`, `pull_request`, or empty for manual).
- `FORGE_PR_NUMBER`: The pull request number.
- `FORGE_API_TOKEN`: A short-lived token issued for this one job. It can download the job's own project source and nothing else; it is not the agent's token. See [job tokens](Roles.md#job-tokens).
- `FORGE_SCHEDULER_URL`: The URL of the scheduler.
- `FORGE_RUN_ID` / `FORGE_JOB_ID` / `FORGE_RUN_NAME`: Unique identifiers for the current execution.
- `FORGE_REPO_URL` / `FORGE_REPO_NAME`: Repository metadata (which may differ in format between triggers).
- `FORGE_AGENT_ID` / `FORGE_PROXY_AGENT_ID`: The agent that happens to run the step.

**Part of the hash only if the step mentions them.** These identify the commit or the run:

- `FORGE_COMMIT_SHA`, `FORGE_COMMIT_TAG`, `FORGE_BRANCH`, `FORGE_REF`
- `FORGE_BUILD_NUMBER`, `FORGE_BUILD_COUNTER`

A step mentions a variable when its name appears in the step's `run` command, its `entrypoint`, a `with:` value, the value of another `env:` variable, or as a placeholder in its `image:`. A step that stamps a version into a binary depends on the build number and says so:

```yaml
- id: build
  image: golang:1.26
  inputs: ["**/*.go", "go.mod", "go.sum"]
  run: go build -ldflags "-X main.version=$FORGE_BUILD_NUMBER" ./...
```

That step runs again for every build number. A lint step with the same inputs that never mentions the build number is skipped until a matching file changes.

Forge cannot see a variable that is only read inside a script file. If `./build.sh` reads `$FORGE_COMMIT_SHA` on its own, pass it explicitly so the step mentions it:

```yaml
  run: ./build.sh "$FORGE_COMMIT_SHA"
```

All other environment variables, including resolved secrets, are always part of the hash.

!!! warning "List every input"
    Because a new commit no longer forces a step to run, `inputs` must name every file the step reads. A step with `inputs: ["src/**"]` that also reads `go.mod` is skipped when only `go.mod` changes. When in doubt, use a wider pattern.

If `inputs` is not defined, caching is disabled for that step, and it will always re-run.

#### Artifact Caching
Forge automatically caches and restores artifacts produced by a cached step. When a cache hit occurs, Forge identifies all artifacts that were uploaded in the original run and "bridges" them to the current run. This ensures that downstream steps that depend on those artifacts continue to work seamlessly even when the producer step is skipped.

!!! note "Artifact Persistence"
    Artifact restoration depends on the original artifacts still being present in the storage backend. If the artifacts from the source run have been deleted (e.g., via a cleanup policy), Forge will discard the cache hit and re-run the step to regenerate them.

---

## Advanced Features

### Matrix Builds

Matrix builds allow you to run the same step multiple times with different variables. Forge expands the matrix at compile time into multiple distinct steps.

```yaml
- id: test
  image: node:${{ matrix.version }}
  matrix:
    version: [18, 20, 22]
    os: [linux, macos]
  run: |
    echo "Running on ${{ matrix.os }} with Node ${{ matrix.version }}"
    npm test
```

Variables are accessed via `${{ matrix.key }}`. Forge generates step IDs like `test-18-linux`, `test-20-linux`, etc. 

Any step that depends on a matrix step will automatically wait for **all** expanded instances of that matrix step to complete. Artifacts, environment variables, and release configurations within a matrix step also support `${{ matrix.key }}` interpolation.

### Test Splitting (`split:`)

Fan a slow test step out into N parallel shards, with test files distributed
by historical runtime so each shard finishes at roughly the same time.

```yaml
- id: integration-tests
  image: golang:1.26-alpine
  timeout: 10m
  test_report: .forge/test-report.json
  split:
    shards: 3
    history_days: 14      # look-back window for timing data (default 14)
    min_history_runs: 2   # runs required before a file's timing is trusted (default 3)
    fallback: single      # cold-start behavior: "single" (default) or "round-robin"
  run: |
    set -eo pipefail
    go build -o /usr/local/bin/forge ./cmd/forge
    if [ "$FORGE_TEST_SHARD_EMPTY" = "1" ]; then
      echo "no timing history yet; deferring to shard 0"; exit 0
    fi
    TEST_PKGS=""
    for p in $(echo "$FORGE_TEST_FILES" | tr ',' ' '); do TEST_PKGS="$TEST_PKGS ./$p"; done
    if [ -z "$TEST_PKGS" ]; then TEST_PKGS="./..."; fi
    go test -v -json $TEST_PKGS 2>&1 | tee /tmp/go-test.json | forge report stream-go-test
    forge report from-go-test /tmp/go-test.json .forge/test-report.json
```

At submit time the step expands into `<id>-shard-1..N` plus a fan-in step
that keeps the original ID, so downstream `depends_on` works unchanged.

**How timing history works.** The step's command writes a machine-readable
report to the `test_report:` path (see `forge report` in the CLI reference);
the agent uploads it after the job. Durations are keyed on
`(project, pipeline name, step id, file path)` — commit- and branch-agnostic,
so history recorded on any branch drives splitting everywhere. Rows are
retained per run: pruning runs also prunes their timing history.

**Cold start.** With no usable history, `fallback: single` (the default)
runs the full suite on shard 1 while the remaining shards no-op with
`FORGE_TEST_SHARD_EMPTY=1` — one warm-up run, not N concurrent full runs.
`fallback: round-robin` instead runs the full suite on every shard. Once at
least one report exists, files are distributed round-robin; once each file
has `min_history_runs` recorded runs, assignment switches to
duration-balanced bin packing.

**Shard environment.** Each shard receives:

| Variable                   | Meaning                                             |
|----------------------------|-----------------------------------------------------|
| `FORGE_TEST_FILES`         | Comma-separated file/package list for this shard.   |
| `FORGE_SHARD_INDEX`        | Zero-based shard index.                             |
| `FORGE_SHARD_TOTAL`        | Total shard count.                                  |
| `FORGE_SHARD_ESTIMATED_MS` | Predicted runtime for this shard's assignment.     |
| `FORGE_TEST_SHARD_EMPTY`   | `"1"` when this shard should no-op (cold start).    |

The run detail view has a **Shards** tab showing each shard's assigned
files, the predicted runtime, and — once finished — the actual runtime.

### Step Templates (`uses:`)

Reusable step definitions can be stored in separate files and imported using the `uses:` field.

**templates/docker-build.yml:**
```yaml
image: docker:27-cli
docker_socket: true
run: docker build -t ${{ env.IMAGE_NAME }} .
```

**pipeline.yml:**
```yaml
steps:
  - id: build-app
    uses: templates/docker-build.yml
    env:
      IMAGE_NAME: my-app:latest
```

Fields in the local step override fields in the template.

#### Registry steps

`uses:` also resolves **versioned steps from a registry**, rather than a file in
your own repo:

```yaml
steps:
  - id: scan-source
    uses: forge-community/trivy@v1.0.0
    with:
      severity: "HIGH,CRITICAL"
```

These are pinned by SHA-256, validated against their declared inputs at compile
time, and expanded into ordinary steps namespaced under the call site's id
(`scan-source.scan`). See [Step Registry](Step-Registry.md) for the reference
syntax, version selection, `requires:`/policy interaction, and how to run
against an internal mirror.

### Manual Approvals

A step with `type: approval` will pause the pipeline and wait for a user to click "Approve" in the Web UI before downstream dependencies are unlocked.

```yaml
- id: wait-for-approval
  type: approval
  depends_on: [test]

- id: deploy
  image: alpine:latest
  depends_on: [wait-for-approval]
  run: ./deploy.sh
```

### Conditional Execution

The `condition` field (aliased as `if` in some contexts, but `condition` in the internal IR) uses CEL expressions to determine if a step should run.

Supported functions:
- `success()`: All dependencies passed (default).
- `failure()`: At least one dependency failed.
- `always()`: Run regardless of dependency status.
- `tag()`: Only run if the pipeline was triggered by a Git tag.
- `branch(name)`: Only run if the pipeline branch matches the given name or glob pattern. Supports multiple branches: `branch(main, develop, feature/*)`.

```yaml
- id: notify-failure
  image: alpine:latest
  condition: failure()
  run: ./notify.sh "Build failed!"

- id: cleanup
  image: alpine:latest
  always_run: true  # shorthand for condition: always()
  run: rm -rf /tmp/build
```

---

### How step images are pulled

Before a step starts, the agent makes sure it has the step's image:

- **An image the agent already has is used as it is.** The registry is not contacted, so steps whose images are cached on the agent keep running while a registry or mirror is down. The other side of this is that a tag which is moved to a new image, such as `latest`, is not refreshed on an agent that already has it; pin a version or a digest when that matters.
- **A missing image is pulled, with retries.** A pull that fails for a reason that may pass (a timeout, a refused connection, a server error) is tried four times over about a minute. The attempts are shown in the step's log.
- **A pull that cannot succeed fails at once.** An image that does not exist, or a registry that refuses the agent's credentials, is not retried.

---

### Running a step only when files changed (`changes:`)

A step with `changes:` runs only when the commits the run is for touched a file matching one of its patterns.

```yaml
- id: build-api
  image: golang:1.26
  changes: ["services/api/**", "go.mod", "go.sum"]
  run: go build ./services/api/...

- id: build-docs
  image: python:3.13
  changes: ["docs/**", "mkdocs.yml"]
  run: mkdocs build

- id: release
  image: alpine:3.20
  depends_on: [build-api, build-docs]
  run: ./scripts/release.sh
```

**Patterns** are relative to the repository root and matched one path segment at a time:

| Pattern                 | Matches                                                        |
|-------------------------|----------------------------------------------------------------|
| `go.mod`                | Only `go.mod` in the repository root.                          |
| `*.md`                  | Markdown files in the root, not in subdirectories.             |
| `**/*.md`               | Markdown files anywhere.                                       |
| `services/api/**`       | Everything under `services/api`.                               |
| `services/api/`         | The same; a trailing slash means "everything below".           |
| `services/*/Dockerfile` | A `Dockerfile` directly inside each directory under `services`. |

In a matrix step, patterns support `${{ matrix.KEY }}`, for example `services/${{ matrix.service }}/**`. Malformed patterns are rejected by `forge validate`.

**What the changed files are compared against:**

| Trigger                     | Compared against                                                                 |
|-----------------------------|----------------------------------------------------------------------------------|
| Push to an existing branch  | The commit the branch pointed at before the push. All pushed commits count.       |
| Pull or merge request       | The point where the request's branch left its target branch. Changes that landed on the target branch in the meantime do not count. |
| Anything else               | Nothing; see below.                                                              |

**When Forge cannot tell what changed, the step runs.** That is the case for the first push of a new branch, a tag push, a force push that removed the previous commit, a manually triggered or API-submitted run, the generic webhook, and `forge run` on your machine.

**A skipped step counts as satisfied.** It is shown as skipped, with the reason as its log line, and steps that depend on it still run. In the example, a commit that only touches `docs/` skips `build-api`, and `release` still runs once `build-docs` has passed. Two consequences:

- If a dependent should be skipped as well, give it its own `changes:` list.
- A dependent that downloads an artifact from a skipped step fails, because the artifact was not produced in this run. Give such a step the same `changes:` list as the step it downloads from.

**`changes:` and `inputs:` answer different questions.** `changes:` asks whether this push touched the files, and skips the step without starting it. `inputs:` asks whether these exact file contents have already passed before, on any commit, and skips the step on a cache hit. They can be used together. Note that `changes:` skips a step even if its last run failed, as long as the new commits do not touch its files.

**Reruns repeat the decision.** A rerun is for the same commits, so the same steps are skipped. To run every step for a commit regardless of `changes:`, trigger the pipeline manually for that branch.

---

## Step Types

### `type: task` (default)

A standard step that runs a command in a Docker container.

```yaml
- id: test
  image: golang:1.26-alpine
  run: go test ./... -race
```

### `type: generator`

A generator step runs code that **emits new step definitions** as a JSON array to stdout. Forge adds those steps to the current run and executes them. This enables runtime job generation — no static matrix required.

```yaml
- id: matrix-generator
  type: generator
  image: python:3.12-slim
  script: scripts/ci/generate_matrix.py
```

The script's stdout must be a valid JSON array of step definition objects. Stderr is captured as log output. Any subsequent step with `depends_on: [matrix-generator]` will wait for the generator AND all of its emitted children.

**Generator script output format:**

```json
[
  {
    "id": "build-linux-amd64",
    "image": "golang:1.26-alpine",
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
| `project`           | string   | Another project to start instead of a file. See [Starting another project's pipeline](#starting-another-projects-pipeline). |
| `wait`              | bool     | If true (default), block until child run reaches a terminal state.                           |
| `variables`         | map      | Env vars injected into every step of the child pipeline, overriding the step's own env.      |
| `artifacts_send`    | string[] | Artifact names from the parent run copied to the child run's context before it starts.       |
| `artifacts_receive` | string[] | Artifact names from the child run copied back into the parent run after the child completes. |

#### Starting another project's pipeline

A pipeline step can also start the pipeline of another Forge project, in another repository, and wait for it. Name the project instead of a file:

```yaml
- id: build-sdk
  type: pipeline
  project: sdk                       # another project in the same organization
  ref: "${{ env.FORGE_BRANCH }}"     # optional: branch or tag in that project's repository
  fallback_ref: main                 # optional: used when ref does not exist there
  variables:
    RELEASE_LINE: "1.2"
  artifacts_receive:
    - sdk-wheel

- id: release
  type: release
  depends_on: [build-sdk]
  release:
    tag: "v${{ env.FORGE_BUILD_NUMBER }}"
    artifacts: [app-wheel, sdk-wheel]
```

| Field               | Type     | Description |
|---------------------|----------|-------------|
| `project`           | string   | Name or ID of a project in the same organization. Use either `project` or `pipeline`, not both. |
| `ref`               | string   | Branch or tag in that project's repository. Defaults to the branch or tag the parent run is for, so both repositories line up. Supports `${{ env.VAR }}`. |
| `fallback_ref`      | string   | Branch or tag to run when `ref` does not exist in that repository, typically its default branch. Without it the step fails. |
| `wait`              | bool     | As for a pipeline file: wait for the child run (default) or start it and move on. |
| `variables`         | map      | Set in the environment of every step of the child run, overriding the step's own values. Values support `${{ env.VAR }}`. Names starting with `FORGE_` are reserved and rejected. |
| `artifacts_send`    | string[] | Artifacts of the parent run made available to the child run. |
| `artifacts_receive` | string[] | Artifacts of the child run copied into the parent run when it finishes. |

What the child run is:

- It runs the pipeline file configured for that project, from that project's repository at the chosen branch or tag, on any available agent. The parent cannot supply a different pipeline file.
- It uses that project's own secrets and the organization's policies.
- **It has the parent's build number.** `FORGE_BUILD_NUMBER` is the same in both runs, which is what lets a release step collect artifacts from several repositories under one version.
- It is shown as a child of the parent run and is canceled when the parent is canceled. If it fails, the step fails.
- Every step runs: `changes:` has nothing to compare against in a child run.

The step's own `timeout` covers the whole wait, so set it longer than the child pipeline can take.

Limits: the project must belong to the same organization as the parent run; child runs can nest at most five levels deep, which stops two projects that start each other from looping.

### `type: release`

A release step pushes artifacts to an SCM provider (GitHub/GitLab). Unlike standard tasks, release steps run on the scheduler and do not require an agent. It automatically retrieves artifacts from the run's storage and uploads them as release assets.

```yaml
- id: github-release
  type: release
  depends_on: [build-binaries]
  condition: tag()
  release:
    name: "Release ${{ env.FORGE_COMMIT_TAG }}"  # interpolated at runtime
    tag: "${{ env.FORGE_COMMIT_TAG }}"
    body: "Forge Release ${{ env.FORGE_COMMIT_TAG }}"
    artifacts:
      - forge-*    # supports wildcards to upload multiple binaries
      - checksums.txt
```

**Release step fields:**

| Field       | Type     | Description                                                                 |
|-------------|----------|-----------------------------------------------------------------------------|
| `name`      | string   | The title of the release. Supports `${{ env.VAR }}`.                        |
| `tag`       | string   | The Git tag to associate the release with. Supports `${{ env.VAR }}`.       |
| `body`      | string   | The description/notes for the release. Supports `${{ env.VAR }}`.           |
| `artifacts` | string[] | A list of artifact names or glob patterns to attach to the release.         |

The `release` block supports interpolation for:
- `${{ env.FORGE_COMMIT_TAG }}`: The Git tag that triggered the run.
- `${{ env.FORGE_BRANCH }}`: The Git branch.
- `${{ env.FORGE_COMMIT_SHA }}`: The full commit SHA.

Ensure your SCM token has sufficient permissions to create releases and upload assets.

---

### `type: docker_build`

A `docker_build` step builds a container image from the job's workspace and pushes it to a registry. The agent runs the build itself, so the step needs no `image`, no `run`, and no `docker_socket`.

```yaml
- id: build-image
  type: docker_build
  secrets: [REGISTRY_USERNAME, REGISTRY_PASSWORD]
  docker_build:
    registry: registry.example.com
    repository: team/api
    context: services/api
    tags:
      - "test-${{ env.FORGE_BUILD_NUMBER }}"
    build_args:
      VERSION: "${{ env.FORGE_BUILD_NUMBER }}"
```

**docker_build step fields:**

| Field        | Type     | Required | Description |
|--------------|----------|----------|-------------|
| `registry`   | string   | yes      | Registry host, with a port if needed, e.g. `registry.example.com` or `localhost:5000`. |
| `repository` | string   | yes      | Repository within the registry, e.g. `team/api`. Lowercase. |
| `tags`       | string[] | yes      | One or more tags to push the image under. |
| `context`    | string   | no       | Build context directory, relative to the workspace. Defaults to the workspace root. |
| `dockerfile` | string   | no       | Path of the Dockerfile, relative to the workspace. Defaults to `Dockerfile` inside the context. |
| `target`     | string   | no       | Build stage to stop at. Defaults to the last stage. |
| `build_args` | map      | no       | Values passed to the build as `--build-arg NAME=value`. |

Every field supports `${{ env.VAR }}` and, in a matrix step, `${{ matrix.KEY }}`.

**A build always pushes.** Later steps can land on a different agent, which has to pull the image, so there is no build-only mode. The usual pattern is to push a per-run tag such as `test-${{ env.FORGE_BUILD_NUMBER }}` here and promote it to its release tag with a [`docker_publish`](#type-docker_publish) step once the tests pass.

**Validation.** Missing required fields, and a `context` or `dockerfile` that points outside the workspace, are rejected by `forge validate` and the pipeline editor. Names and tags are checked again when the step runs, after placeholders are resolved. A placeholder that names a variable the step does not have fails the step instead of producing an empty tag.

**Authentication.** List `REGISTRY_USERNAME` and `REGISTRY_PASSWORD` under `secrets:`, the same two names `docker_publish` uses. The agent writes them to a private file outside the workspace for the duration of the step and removes it afterwards; they are never passed on a command line. Omit both for a registry that accepts anonymous pushes. Giving only one of the two fails the step.

#### Running an image built earlier in the run

A later step runs the image by naming the same per-run tag in its `image:`:

```yaml
- id: build-image
  type: docker_build
  docker_build:
    registry: registry.example.com
    repository: team/api
    tags: ["test-${{ env.FORGE_BUILD_NUMBER }}"]

- id: integration-tests
  image: "registry.example.com/team/api:test-${{ env.FORGE_BUILD_NUMBER }}"
  depends_on: [build-image]
  run: pytest tests/integration
```

The placeholder is resolved on the agent just before the step starts, from the step's environment (`env:` values and the variables Forge sets, not `secrets:`). The resolved reference is validated before it is used: a variable the step does not have, or a value that does not form a valid image reference, fails the step with the reason in its log.

The agent that runs the later step pulls the image if it does not have it. For a registry that requires authentication to pull, the container engine on every agent host must be logged in to it; Forge does not pass the build step's credentials to other steps.

**Caching.** A `docker_build` step is never skipped on a Forge cache hit, even if it declares `inputs`, because the tags it pushes differ per run and later steps depend on them. Unchanged layers are still reused through the container engine's own layer cache on the agent.

**Base images.** The build runs with the agent's container engine. Under Podman, short names such as `FROM alpine` resolve only if the host defines a default registry, so write fully qualified names (`FROM docker.io/library/alpine:3.20`) or mirror names in the Dockerfile. In an environment with an internal CA, the engine on the agent host must already trust the registry and any mirrors; see [Internal certificate authorities](Configuration.md#internal-certificate-authorities).

**Plain-HTTP registries.** A local test registry without TLS works with Docker on `localhost` out of the box. Podman needs it marked as insecure in `registries.conf`:

```toml
[[registry]]
location = "localhost:5000"
insecure = true
```

**Current limits.** One CPU architecture per build (the agent's). No build-time secrets or SSH forwarding yet. The digest of each pushed tag is written to the step log.

A runnable example is in `examples/image-build/`.

---

### `type: docker_publish`

A `docker_publish` step adds one or more tags to an already-pushed image without rebuilding it — typically used to promote a test-specific tag to its real release tag(s) only after tests pass. Like `release` steps, `docker_publish` steps run on the scheduler and require neither an agent nor `docker_socket`: no image content is re-uploaded, since the source tag's manifest already references blobs that exist in the registry (issue #57).

```yaml
- id: promote-image
  type: docker_publish
  depends_on: [integration-tests]
  condition: success()
  docker_publish:
    registry: ghcr.io
    repository: myorg/myapp
    source: "test-${{ env.FORGE_BUILD_NUMBER }}"
    tags:
      - "${{ env.FORGE_BUILD_NUMBER }}"
      - latest
  secrets: [REGISTRY_USERNAME, REGISTRY_PASSWORD]
```

**docker_publish step fields:**

| Field           | Type     | Required | Description                                                                    |
|-----------------|----------|----------|----------------------------------------------------------------------------------|
| `registry`      | string   | yes      | Registry host, e.g. `ghcr.io`, `docker.io`. Supports `${{ env.VAR }}`.            |
| `repository`    | string   | yes      | Repository within the registry, e.g. `myorg/myapp`. Supports `${{ env.VAR }}`.    |
| `source`        | string   | yes      | The tag being promoted from. Supports `${{ env.VAR }}`.                          |
| `tags`          | string[] | yes      | One or more target tags being promoted to. Each supports `${{ env.VAR }}`.        |
| `delete_source` | bool     | no       | Not currently supported — see "Deletion is rejected, not ignored" below. Omit it or set it to `false`. |

`registry`, `repository`, `source`, and `tags` are required — a `docker_publish` block missing any of them is rejected by `forge validate` (and the UI's pipeline editor, since both run through the same validation path) before it's ever submitted, not first discovered when a run fails.

**Authentication.** `docker_publish` authenticates using the same secret resolution order as any task step (project → org → global) via `secrets:`. It looks specifically for two conventional secret names: `REGISTRY_USERNAME` and `REGISTRY_PASSWORD`. Both can be omitted only for a registry that permits every operation this step performs anonymously — which in practice means anonymous *pushes* (promoting a tag is a write), not just anonymous pulls; most registries, including GHCR and Docker Hub, require authentication to push even to a public repository.

**Deletion is rejected, not ignored.** Setting `delete_source: true` is a validation error — `forge validate` (and the UI's pipeline editor, since both run through the same validation path) rejects it before the pipeline is ever submitted, rather than silently accepting it and doing nothing. The reason is a real safety concern, not a missing feature checkbox: a promotion always leaves the source tag and every newly-promoted target tag pointing at the identical digest, and deleting a tag on many registries cascades to every other tag sharing that digest — so an automatic delete here could silently remove the tags this step just created. Until there's a registry-safe way to confirm a tag has no other references, clean up stale source tags manually or with a separate, deliberate process, and leave `delete_source` unset (or `false`) in your pipeline. The tags actually applied and the source digest are recorded per-job and retrievable via `forge status <run-id>` and the UI's step detail panel — not just from logs.

**Ordering "promote only after tests pass"** needs no new mechanism: `depends_on`/`condition` work exactly as they do for any other step type, so a `docker_publish` step simply depends on whatever step(s) must succeed first.

---

## Complete Example

```yaml
name: build-test-deploy

steps:
  # Parallel: test and lint run simultaneously
  - id: test
    image: golang:1.26-alpine
    timeout: 10m
    run: go test ./... -race -coverprofile=coverage.out

  - id: lint
    image: golangci/golangci-lint:latest
    timeout: 5m
    run: golangci-lint run ./...

  # Build waits for both test AND lint
  - id: build
    image: golang:1.26-alpine
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
      upload:
        - path: image-digest.txt
          name: image-digest
    run: |
      chmod +x dist/app
      docker build -t myapp:${GIT_SHA:-dev} .

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