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
    # Check root first (fastest and most robust for many policies)
    if os.path.isfile(os.path.join(WORKSPACE, f)):
        return f
    
    # Fallback to recursive search for nested projects (e.g. monorepos)
    try:
        matches = [match for match in glob.glob(f"**/{f}", root_dir=WORKSPACE, recursive=True)
                   if os.path.isfile(os.path.join(WORKSPACE, match))]
        return matches[0] if matches else None
    except Exception as e:
        print(f"DEBUG: glob error for {f}: {e}", file=sys.stderr)
        return None

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
                "id":      "trivy-fs-report",
                "image":   "aquasec/trivy:latest",
                "command": ["filesystem", "--severity", "HIGH,CRITICAL", "--format", "template", "--template", "@/contrib/html.tpl", "-o", "trivy-fs-report.html", "/workspace"],
                "depends_on": [],
                "workdir": "/workspace",
                "artifact_uploads": [
                    {"path": "trivy-fs-report.html", "name": "Filesystem Security Report"}
                ],
                "inputs":[
                    "Dockerfile"
                ]
            })
            injected.append({
                "id":      "trivy-fs-scan",
                "image":   "aquasec/trivy:latest",
                "command": ["filesystem", "--severity", "HIGH,CRITICAL", "/workspace"],
                "env":     {"TRIVY_EXIT_CODE": "1"},
                "depends_on": ["trivy-fs-report"],
                "workdir": "/workspace",
                "inputs":[
                    "Dockerfile"
                ]
            })

    # ---------------------------------- Python ----------------------------------
    if workspace_has("requirements.txt"):
        if "pip-audit" not in existing_ids:
            req_match = workspace_find("requirements.txt")
            if req_match:
                req_file = os.path.join(WORKSPACE, req_match)
                injected.append({
                    "id":      "pip-audit",
                    "image":   "python:3.11-slim",
                    "inputs": [req_match],
                    "command": ["sh", "-c",
                                f"pip install --quiet pip-audit && "
                                f"pip-audit -r {req_file} > pip-audit-output.txt ; "
                                f"RET=$? ; "
                                f"echo '<html><body style=\"{{font-family: monospace; white-space: pre; background: #1e1e1e; color: #d4d4d4; padding: 20px;}}\">' > pip-audit-report.html ; "
                                f"cat pip-audit-output.txt >> pip-audit-report.html ; "
                                f"echo '</body></html>' >> pip-audit-report.html ; "
                                f"cat pip-audit-output.txt ; "
                                f"exit $RET"],
                    "depends_on": [],
                    "workdir": "/workspace",
                    "artifact_uploads": [
                        {"path": "pip-audit-report.html", "name": "Python Security Report"}
                    ]
                })


    # ---------------------------------- Go ----------------------------------
    if workspace_has("go.mod"):
        if "govulncheck" not in existing_ids:
            go_match = workspace_find("go.mod")
            if go_match:
                go_rel_dir = os.path.dirname(go_match)
                go_dir = os.path.join(WORKSPACE, go_rel_dir)
                injected.append({
                    "id":      "govulncheck",
                    "image":   "golang:1.26.5-alpine",
                    "inputs": ["go.mod"],
                    # govulncheck must be installed first; wrap in sh -c for &&
                    "command": ["sh", "-c",
                                "go install golang.org/x/vuln/cmd/govulncheck@latest && "
                                "govulncheck ./... > govulncheck-output.txt ; "
                                "RET=$? ; "
                                "echo '<html><body style=\"font-family: monospace; white-space: pre; background: #1e1e1e; color: #d4d4d4; padding: 20px;\">' > govulncheck-report.html ; "
                                "cat govulncheck-output.txt >> govulncheck-report.html ; "
                                "echo '</body></html>' >> govulncheck-report.html ; "
                                "cat govulncheck-output.txt ; "
                                "exit $RET"],
                    "depends_on": [],
                    "workdir": go_dir,
                    "artifact_uploads": [
                        {"path": os.path.join(go_rel_dir, "govulncheck-report.html"), "name": "Go Security Report"}
                    ]
                })

    # ---------------------------------- Node.js ----------------------------------
    if workspace_has("package.json"):
        if "npm-audit" not in existing_ids:
            pkg_match = workspace_find("package.json")
            if pkg_match:
                pkg_rel_dir = os.path.dirname(pkg_match)
                pkg_dir = os.path.join(WORKSPACE, pkg_rel_dir)
                injected.append({
                    "id":      "npm-audit",
                    "image":   "node:20-alpine",
                    "inputs": [pkg_match],
                    "command": ["sh", "-c",
                                "npm audit --audit-level=high > npm-audit-output.txt ; "
                                "RET=$? ; "
                                "echo '<html><body style=\"font-family: monospace; white-space: pre; background: #1e1e1e; color: #d4d4d4; padding: 20px;\">' > npm-audit-report.html ; "
                                "cat npm-audit-output.txt >> npm-audit-report.html ; "
                                "echo '</body></html>' >> npm-audit-report.html ; "
                                "cat npm-audit-output.txt ; "
                                "exit $RET"],
                    "depends_on": [],
                    "workdir": pkg_dir,
                    "artifact_uploads": [
                        {"path": os.path.join(pkg_rel_dir, "npm-audit-report.html"), "name": "Node Security Report"}
                    ]
                })

    if not injected:
        print(json.dumps(steps))
        return

    # Scan steps first, then user steps.
    print(json.dumps(injected + steps, indent=2))

if __name__ == "__main__":
    main()