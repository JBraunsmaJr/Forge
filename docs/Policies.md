# Policy Engine

Policies let platform and security teams inject mandatory steps into every pipeline submitted under an org — without pipeline authors knowing or having to do anything. Add a new security scan policy today, and it applies to every future pipeline submission automatically.

---

## How Policies Work

When a pipeline is submitted with an `--org` flag, the scheduler runs all transformer policies registered for that org. Each transformer receives the full pipeline, modifies it (adding, removing, or reordering steps), and returns the modified pipeline. The result is what actually gets submitted.

```
User submits pipeline:              Scheduler stores:
  - lint                              - lint
  - build                →            - build
  - deploy               transform    - trivy-scan-build    ← injected
                                      - deploy
```

Pipeline authors write what they need. Security teams ensure compliance through policies. Neither group needs to coordinate per-pipeline.

---

## Policy Types

### Transformer Policies (recommended)

A transformer is a Docker image that receives the pipeline on stdin as JSON and returns the modified pipeline on stdout. This is the most powerful type — transformers can apply sophisticated logic: inspect the workspace, detect languages, conditionally inject steps.

```bash
forge policy transformer security-scan \
  --org <org-id> \
  --image forge-security-policies:latest \
  --command python3 /policies/container-security.py \
  --description "Injects Trivy after every docker build step"
```

### Static Policies

Inject a fixed set of steps into every pipeline, with configurable placement.

```bash
forge policy create notify-on-failure \
  --org <org-id> \
  --description "Sends Slack notification on pipeline failure"
```

---

## Built-in Transformer Examples

The `examples/policies/` directory contains two ready-to-use transformers, built into the compose stack as `forge-security-policies:latest`.

### `container-security.py`

Detects `docker build` steps and injects a Trivy vulnerability scan step after each one. Trivy scans the built image for CVEs and exits non-zero if critical vulnerabilities are found.

```
Pipeline step: docker build -t myapp .
Policy injects: trivy image --exit-code 1 --severity CRITICAL myapp
```

### `language-security.py`

Detects the primary language of the workspace by looking for language-specific files (`go.mod`, `package.json`, `requirements.txt`, etc.) and injects the appropriate scanner:

| Language | Scanner injected |
|---|---|
| Go | `govulncheck ./...` |
| Node.js | `npm audit --audit-level=high` |
| Python | `pip-audit` |
| Java | `dependency-check` |

---

## Writing a Transformer

A transformer is any executable that reads JSON from stdin and writes JSON to stdout.

**Input (stdin):**
```json
{
  "pipeline_name": "my-pipeline",
  "steps": [
    {
      "id": "build",
      "image": "docker:24-cli",
      "run": "docker build -t myapp .",
      ...
    }
  ],
  "workspace_dir": "/workspace",
  "org_id": "abc123"
}
```

**Output (stdout):** the complete modified step list as a JSON array.

**Python transformer skeleton:**

```python
#!/usr/bin/env python3
import json, sys

data = json.load(sys.stdin)
steps = data["steps"]

new_steps = []
for step in steps:
    new_steps.append(step)
    
    # Example: inject a step after every step that matches a condition
    if "docker build" in step.get("run", ""):
        new_steps.append({
            "id": f"scan-{step['id']}",
            "image": "aquasec/trivy:latest",
            "depends_on": [step["id"]],
            "run": f"trivy image --exit-code 1 myapp",
        })

print(json.dumps(new_steps))
```

### Transformer rules

- **Must output the complete step list** — not just the new steps, the full list
- **Non-zero exit** means the submission is rejected (policy enforcement failure)
- **stderr** is logged for debugging, not shown to pipeline authors
- **Timeout** defaults to 30 seconds — transformers should be fast
- The workspace is mounted at `/workspace` read-only — transformers can inspect files

---

## Managing Policies

```bash
# List policies for an org
forge policy list --org <org-id>

# Delete a policy
forge policy delete <policy-id> --org <org-id>
```

Deleting a policy has no effect on already-submitted runs. New submissions will not have the policy applied.

---

## `forbid_override`

A policy with `forbid_override: true` causes the scheduler to reject the submission with HTTP 403 if the transformer detects a prohibited pattern. Use this for hard compliance requirements.

```bash
forge policy transformer no-latest-tags \
  --org <org-id> \
  --image forge-security-policies:latest \
  --command python3 /policies/no-latest-tags.py \
  --forbid-override \
  --description "Reject pipelines that use :latest image tags"
```

---

## Policy Badges in the Web UI

The DAG view shows a 🛡 badge on each step injected by a policy. Hovering shows which policy injected it. This gives pipeline authors visibility into what was added without cluttering their pipeline file.