#!/usr/bin/env python3
"""
detect-changes.py — Monorepo change detector for Forge CI.

Reads the git diff between the current commit and a base ref, identifies which
services under services/ have changed, and emits a Forge step list as JSON.

Each changed service gets four steps (in the right dependency order):
  lint → test (parallel), then build → push (sequential after both pass)

If shared/ changes, all services are rebuilt regardless of per-service diff.
If nothing changed, an empty list is emitted and the run finishes immediately.

Usage:
  Called via Forge's script: field — runs inside a container with the
  workspace mounted at /workspace and git history available.

Environment variables:
  REGISTRY    Container registry hostname (default: registry.example.com)
  GIT_SHA     Current commit SHA (default: "dev")
  BASE_REF    Git ref to diff against (default: HEAD~1)
"""
import json
import os
import subprocess
import sys


REGISTRY = os.environ.get("REGISTRY", "registry.example.com")
GIT_SHA  = os.environ.get("GIT_SHA", "dev")
BASE_REF = os.environ.get("BASE_REF", "HEAD~1")
SERVICES_DIR = "/workspace/services"


def git_changed_paths(base: str) -> list[str]:
    """Return the list of paths changed between base and HEAD."""
    result = subprocess.run(
        ["git", "diff", "--name-only", base, "HEAD"],
        capture_output=True, text=True, cwd="/workspace"
    )
    if result.returncode != 0:
        # If git diff fails (e.g. shallow clone), rebuild everything.
        print(f"[warn] git diff failed: {result.stderr.strip()}", file=sys.stderr)
        return []
    return result.stdout.strip().splitlines()


def discover_services() -> list[str]:
    """Find all service directories in the workspace."""
    if not os.path.isdir(SERVICES_DIR):
        return []
    return [
        d for d in os.listdir(SERVICES_DIR)
        if os.path.isdir(os.path.join(SERVICES_DIR, d))
    ]


def build_steps_for_service(svc: str) -> list[dict]:
    """Return the four CI steps for a single service."""
    image_ref = f"{REGISTRY}/{svc}:{GIT_SHA}"
    return [
        {
            "id":    f"lint-{svc}",
            "image": "golangci/golangci-lint:latest",
            "workdir": f"/workspace/services/{svc}",
            "depends_on": ["change-detector"],
            "timeout": "5m",
            "run": "golangci-lint run ./...",
        },
        {
            "id":    f"test-{svc}",
            "image": "golang:1.24-alpine",
            "workdir": f"/workspace/services/{svc}",
            "depends_on": ["change-detector"],
            "timeout": "10m",
            "run": "go test ./... -race -coverprofile=coverage.out && go tool cover -func=coverage.out",
        },
        {
            "id":    f"build-{svc}",
            "image": "docker:24-cli",
            "docker_socket": True,
            "depends_on": [f"lint-{svc}", f"test-{svc}"],
            "timeout": "15m",
            "run": f'docker build -t "{image_ref}" services/{svc}',
        },
        {
            "id":    f"push-{svc}",
            "image": "docker:24-cli",
            "docker_socket": True,
            "depends_on": [f"build-{svc}"],
            "secrets": ["REGISTRY_TOKEN"],
            "timeout": "5m",
            "run": f'docker push "{image_ref}"',
        },
    ]


def main():
    all_services = discover_services()
    if not all_services:
        print("[]")
        return

    changed_paths = git_changed_paths(BASE_REF)

    # If shared/ changed, treat every service as affected.
    shared_changed = any(p.startswith("shared/") for p in changed_paths)
    if shared_changed:
        print(f"[info] shared/ changed — rebuilding all {len(all_services)} services", file=sys.stderr)
        affected = all_services
    else:
        affected = [
            svc for svc in all_services
            if any(p.startswith(f"services/{svc}/") for p in changed_paths)
        ]

    if not affected:
        print("[info] No services changed — nothing to build", file=sys.stderr)
        print("[]")
        return

    print(f"[info] Affected services: {', '.join(affected)}", file=sys.stderr)

    steps = []
    for svc in affected:
        steps.extend(build_steps_for_service(svc))

    print(json.dumps(steps, indent=2))


if __name__ == "__main__":
    main()