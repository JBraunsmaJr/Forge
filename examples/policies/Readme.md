# Forge Security Policy Transformers

This directory contains example policy transformer scripts and the Dockerfile
that packages them into a single image.

## What's here

| File | Purpose |
|---|---|
| `Dockerfile` | Builds the `forge-security-policies` image |
| `container-security.py` | Injects Trivy scans after `docker build` steps |
| `language-security.py` | Injects language-appropriate scans based on workspace files |

---

## Quick start

### 1. Build the image

```bash
docker build -t forge-security-policies:latest examples/policies/
```

### 2. Create an org and set it in your session

```powershell
./forge.exe org create my-company
$env:FORGE_ORG = "<org-id>"  # copy from the output above
```

### 3. Register transformer policies

```powershell
# Inject Trivy after any docker build step
./forge.exe policy transformer container-security `
  --image forge-security-policies:latest `
  --command python3 /policies/container-security.py

# Inject language-appropriate scans based on workspace files
./forge.exe policy transformer language-security `
  --image forge-security-policies:latest `
  --command python3 /policies/language-security.py

# Confirm policies are registered
./forge.exe policy list
```

### 4. Submit a pipeline — transformers run automatically

```powershell
./forge.exe submit examples/.forge/pipeline.json
```

---

## How the transformer protocol works

Each transformer is a process that:

- Reads the full pipeline from **stdin** as JSON
- Writes the modified pipeline to **stdout** as JSON
- Writes diagnostic messages to **stderr** (logged, does not fail the build)
- Exits non-zero to abort the submission with an error

**stdin shape:**
```json
{
  "pipeline_name": "forge-ci",
  "workspace_dir": "/path/to/your/project",
  "org_id": "abc123",
  "steps": [
    { "id": "build", "image": "docker:27", "run": "docker build -t myapp ." },
    { "id": "deploy", "depends_on": ["build"] }
  ]
}
```

**stdout shape** (complete step list after transformation):
```json
[
  { "id": "build",       "run": "docker build -t myapp .",   "depends_on": [] },
  { "id": "trivy-scan",  "run": "trivy image myapp:latest",  "depends_on": ["build"] },
  { "id": "deploy",      "run": "...",                        "depends_on": ["trivy-scan"] }
]
```

Note that `deploy` now depends on `trivy-scan` instead of `build` — the
transformer restructured the dependency graph so the scan is a mandatory gate.

---

## Testing a transformer locally

You can test any transformer script without running Forge at all:

```bash
echo '{
  "pipeline_name": "test",
  "workspace_dir": "/your/project",
  "org_id": "x",
  "steps": [
    {"id": "build", "image": "docker:27", "run": "docker build -t myapp ."},
    {"id": "deploy", "depends_on": ["build"]}
  ]
}' | python3 examples/policies/container-security.py | python3 -m json.tool
```

Or via the Docker image:

```bash
echo '<pipeline-json>' | docker run --rm -i \
  forge-security-policies:latest \
  python3 /policies/container-security.py
```

---

## Writing your own transformer

1. Write a script (Python, shell, Go, anything) that reads JSON from stdin
   and writes JSON to stdout.

2. Add it to the Dockerfile:
   ```dockerfile
   COPY my-transformer.py /policies/my-transformer.py
   ```

3. Rebuild the image:
   ```bash
   docker build -t forge-security-policies:latest examples/policies/
   ```

4. Register it:
   ```powershell
   ./forge.exe policy transformer my-policy `
     --image forge-security-policies:latest `
     --command python3 /policies/my-transformer.py
   ```

The workspace is mounted read-only at `/workspace` inside the container,
so your transformer can inspect any file in the project:

```python
import os, json, sys

data = json.load(sys.stdin)
steps = data["steps"]

# Inspect the workspace
if os.path.exists("/workspace/Dockerfile"):
    steps.insert(0, {"id": "my-scan", "image": "scanner:latest", "run": "scan ."})

print(json.dumps(steps))
```
## Image tags containing variables

`container-security.py` reads the image reference straight out of your
`docker build -t …` line, so whatever you wrote there is what Trivy is
asked to scan — including any shell variables.

That matters because `FORGE_BUILD_NUMBER` is assigned when the run is
inserted, which happens *after* policies run. Its value cannot be known at
policy time, so the injected scan steps run through `/bin/sh -c` and let
the shell expand the variable at execution time, from the env the
scheduler stamps onto every step:

```yaml
- id: build-image
  run: docker build -t ghcr.io/acme/app:test-${FORGE_BUILD_NUMBER} .
```

```
trivy image "ghcr.io/acme/app:test-${FORGE_BUILD_NUMBER}"   # -> test-417
```

The scan steps override the image entrypoint to do this. `aquasec/trivy`
sets `ENTRYPOINT ["trivy"]`, so without `entrypoint: ["/bin/sh"]` the
shell invocation would be passed to Trivy as arguments.

Two consequences worth knowing:

- **Use shell syntax (`${VAR}`), not `${{ env.VAR }}`, in a build tag.**
  The `${{ … }}` form is resolved by the compiler, which runs before the
  build number exists; it will not produce a value here.
- **Tags are validated before interpolation.** A tag is allowed to contain
  image-reference characters and `$VAR` / `${VAR}` and nothing else. A tag
  carrying quotes, backticks, `$(…)`, `;`, `&` or `|` fails the policy
  rather than being spliced into a shell command.

A `docker build` step whose tag cannot be found is **not scanned**. The
policy warns on stderr rather than failing, so check the run log if you
expect a scan and don't see one.
