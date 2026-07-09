#!/usr/bin/env python3
"""
language-security-transformer.py

Reads the workspace to detect languages/frameworks and injects
appropriate security scans at the start of the pipeline.

The workspace is mounted read-only at /workspace when running via Docker.
"""
import json
import os
import sys
import glob

WORKSPACE = "/workspace"

def workspace_find(f):
    matches = [match for match in glob.glob(f"**/{f}", root_dir=WORKSPACE, recursive=True)
               if os.path.isfile(os.path.join(WORKSPACE, match))]
    return matches[0] if matches else None

def workspace_has(*files):
    for f in files:
        if workspace_find(f):
            return True
    return False

def main():
    data = json.load(sys.stdin)
    steps = data.get("steps", [])
    existing_ids = {s["id"] for s in steps}
    injected = []

    # Container / filesystem scan ----------------------------------
    if workspace_has("Dockerfile", "compose.yml", "compose.yaml", "docker-compose.yml", "docker-compose.yaml"):
        if "trivy-fs-scan" not in existing_ids:
            injected.append({
                "id":      "trivy-fs-scan",
                "image":   "aquasec/trivy:latest",
                "command": ["filesystem", "--severity", "HIGH,CRITICAL", "/workspace"],
                "env":     {"TRIVY_EXIT_CODE": "1"},
                "depends_on": [],
                "workdir": "/workspace",
            })

    # ---------------------------------- Python ----------------------------------
    if workspace_has("requirements.txt", "Pipfile", "pyproject.toml"):
        if "pip-audit" not in existing_ids:
            req_match = workspace_find("requirements.txt")
            if req_match:
                req_file = os.path.join(WORKSPACE, req_match)
                injected.append({
                    "id":      "pip-audit",
                    "image":   "python:3.11-slim",
                    "command": ["sh", "-c",
                                f"pip install --quiet pip-audit && pip-audit -r {req_file}"],
                    "depends_on": [],
                    "workdir": "/workspace",
                })

    # ---------------------------------- Go ----------------------------------
    if workspace_has("go.mod"):
        if "govulncheck" not in existing_ids:
            go_match = workspace_find("go.mod")
            if go_match:
                go_dir = os.path.dirname(os.path.join(WORKSPACE, go_match))
                injected.append({
                    "id":      "govulncheck",
                    "image":   "golang:1.26-alpine",
                    # govulncheck must be installed first; wrap in sh -c for &&
                    "command": ["sh", "-c",
                                "go install golang.org/x/vuln/cmd/govulncheck@latest"
                                " && govulncheck ./..."],
                    "depends_on": [],
                    "workdir": go_dir,
                })

    # ---------------------------------- Node.js ----------------------------------
    if workspace_has("package.json"):
        if "npm-audit" not in existing_ids:
            pkg_match = workspace_find("package.json")
            if pkg_match:
                pkg_dir = os.path.dirname(os.path.join(WORKSPACE, pkg_match))
                injected.append({
                    "id":      "npm-audit",
                    "image":   "node:20-alpine",
                    "command": ["npm", "audit", "--audit-level=high"],
                    "depends_on": [],
                    "workdir": pkg_dir,
                })

    if not injected:
        print(json.dumps(steps))
        return

    # Scan steps first, then user steps.
    print(json.dumps(injected + steps, indent=2))

if __name__ == "__main__":
    main()