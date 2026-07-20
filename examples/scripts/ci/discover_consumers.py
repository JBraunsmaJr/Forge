#!/usr/bin/env python3
"""
discover_consumers.py — Service consumer discovery for Forge CI.

Identifies which services depend on changed components and emits a Forge
step list to test those consumers.

Usage:
  Called via Forge's script: field.
"""
import json
import os
import subprocess
import sys


REGISTRY = os.environ.get("REGISTRY", "registry.example.com")
GIT_SHA  = os.environ.get("GIT_SHA", "dev")
BASE_REF = os.environ.get("BASE_REF", "HEAD~1")
SERVICES_DIR = "/workspace/services"


def git_changed_paths(base: str, cwd: str = "/workspace") -> list[str]:
    """Return the list of paths changed between base and HEAD."""
    result = subprocess.run(
        ["git", "diff", "--name-only", base, "HEAD"],
        capture_output=True, text=True, cwd=cwd
    )
    if result.returncode != 0:
        return []
    return result.stdout.strip().splitlines()


def discover_services(services_dir: str) -> list[str]:
    """Find all service directories in the workspace."""
    if not os.path.isdir(services_dir):
        return []
    return [
        d for d in os.listdir(services_dir)
        if os.path.isdir(os.path.join(services_dir, d))
    ]


def build_steps_for_service(svc: str, registry: str, git_sha: str) -> list[dict]:
    """Return CI steps for a consumer service."""
    return [
        {
            "id":    f"test-consumer-{svc}",
            "image": "golang:1.26-alpine",
            "workdir": f"/workspace/services/{svc}",
            "depends_on": ["consumer-discovery"],
            "timeout": "10m",
            "run": "go test ./... -race",
        }
    ]


def detect_affected_consumers(all_services: list[str], changed_paths: list[str]) -> list[str]:
    """
    Logic: find services affected by changes. 
    In a real app, this would use a dependency graph.
    """
    affected = [
        svc for svc in all_services
        if any(p.startswith(f"services/{svc}/") for p in changed_paths)
    ]
    return affected


def main():
    # 1. Collect inputs (stdin, env and git)
    try:
        if not sys.stdin.isatty():
            input_data = json.load(sys.stdin)
        else:
            input_data = {}
    except Exception:
        input_data = {}

    all_services = discover_services(SERVICES_DIR)
    base_ref = input_data.get("ref") or BASE_REF
    changed_paths = git_changed_paths(base_ref)

    # 2. Run core logic
    affected = detect_affected_consumers(all_services, changed_paths)

    # 3. Handle output
    if not affected:
        print("[]")
        return

    registry = input_data.get("env", {}).get("REGISTRY") or REGISTRY
    git_sha = input_data.get("commit_sha") or GIT_SHA

    steps = []
    for svc in affected:
        steps.extend(build_steps_for_service(svc, registry, git_sha))

    print(json.dumps(steps, indent=2))


if __name__ == "__main__":
    main()