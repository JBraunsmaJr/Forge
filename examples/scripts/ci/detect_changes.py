#!/usr/bin/env python3
"""
detect_changes.py — Monorepo change detector for Forge CI.

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


def git_changed_paths(base: str, cwd: str = "/workspace") -> list[str]:
    """Return the list of paths changed between base and HEAD."""
    result = subprocess.run(
        ["git", "diff", "--name-only", base, "HEAD"],
        capture_output=True, text=True, cwd=cwd
    )
    if result.returncode != 0:
        # If git diff fails (e.g. shallow clone), rebuild everything.
        print(f"[warn] git diff failed: {result.stderr.strip()}", file=sys.stderr)
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
    """Return the four CI steps for a single service."""
    image_ref = f"{registry}/{svc}:{git_sha}"
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
            "image": "golang:1.26-alpine",
            "workdir": f"/workspace/services/{svc}",
            "depends_on": ["change-detector"],
            "timeout": "10m",
            "run": "go test ./... -race -coverprofile=coverage.out && go tool cover -func=coverage.out",
        },
        {
            "id":    f"build-{svc}",
            "image": "docker:27-cli",
            "docker_socket": True,
            "depends_on": [f"lint-{svc}", f"test-{svc}"],
            "timeout": "15m",
            "run": f'docker build -t "{image_ref}" services/{svc}',
        },
        {
            "id":    f"push-{svc}",
            "image": "docker:27-cli",
            "docker_socket": True,
            "depends_on": [f"build-{svc}"],
            "secrets": ["REGISTRY_TOKEN"],
            "timeout": "5m",
            "run": f'docker push "{image_ref}"',
        },
    ]


def detect_affected_services(all_services: list[str], changed_paths: list[str]) -> list[str]:
    """Pure logic: determine which services are affected by changes."""
    # If shared/ changed, treat every service as affected.
    shared_changed = any(p.startswith("shared/") for p in changed_paths)
    if shared_changed:
        return all_services
    
    return [
        svc for svc in all_services
        if any(p.startswith(f"services/{svc}/") for p in changed_paths)
    ]


def main():
    # 1. Collect inputs (stdin, env and git)
    try:
        if not sys.stdin.isatty():
            input_data = json.load(sys.stdin)
            print(f"[info] Read generator context from stdin (ref: {input_data.get('ref')})", file=sys.stderr)
        else:
            input_data = {}
    except Exception:
        input_data = {}

    all_services = discover_services(SERVICES_DIR)
    if not all_services:
        print("[]")
        return

    # Use ref from stdin if available, else fallback to BASE_REF env.
    base_ref = input_data.get("ref") or BASE_REF
    changed_paths = git_changed_paths(base_ref)

    # 2. Run core logic
    affected = detect_affected_services(all_services, changed_paths)

    # 3. Handle output
    if not affected:
        print("[info] No services changed — nothing to build", file=sys.stderr)
        print("[]")
        return

    print(f"[info] Affected services: {', '.join(affected)}", file=sys.stderr)

    # Use registry/sha from stdin if available.
    registry = input_data.get("env", {}).get("REGISTRY") or REGISTRY
    git_sha = input_data.get("commit_sha") or GIT_SHA

    steps = []
    for svc in affected:
        steps.extend(build_steps_for_service(svc, registry, git_sha))

    print(json.dumps(steps, indent=2))


if __name__ == "__main__":
    main()