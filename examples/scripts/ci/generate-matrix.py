#!/usr/bin/env python3
"""
generate-matrix.py — Reads a platform configuration and emits one build + test
job per platform as a Forge step list.

This replaces GitHub Actions' static matrix: syntax with real code. Benefits:
  - Add a platform by editing platforms.json, not the pipeline YAML
  - Skip platforms based on runtime conditions (git diff, env vars, flags)
  - Per-platform timeouts, resource limits, and env var overrides
  - Parallel builds with a single fan-in release step

platforms.json format:
  [
    {"os": "linux",   "arch": "amd64", "image": "golang:1.24-alpine"},
    {"os": "linux",   "arch": "arm64", "image": "golang:1.24-alpine"},
    {"os": "windows", "arch": "amd64", "image": "golang:1.24-windowsservercore-ltsc2022"}
  ]

Output (stdout): JSON array of StepDef objects
"""
import json
import os
import sys


PLATFORMS_FILE = "/workspace/.forge/platforms.json"


DEFAULT_PLATFORMS = [
    {"os": "linux",   "arch": "amd64",  "image": "golang:1.24-alpine"},
    {"os": "linux",   "arch": "arm64",  "image": "golang:1.24-alpine"},
    {"os": "windows", "arch": "amd64",  "image": "golang:1.24-alpine"},  # cross-compile
    {"os": "darwin",  "arch": "amd64",  "image": "golang:1.24-alpine"},  # cross-compile
    {"os": "darwin",  "arch": "arm64",  "image": "golang:1.24-alpine"},  # cross-compile
]


def load_platforms() -> list[dict]:
    if os.path.exists(PLATFORMS_FILE):
        with open(PLATFORMS_FILE) as f:
            platforms = json.load(f)
        print(f"[info] Loaded {len(platforms)} platforms from {PLATFORMS_FILE}", file=sys.stderr)
        return platforms
    print(f"[info] No platforms.json found — using defaults", file=sys.stderr)
    return DEFAULT_PLATFORMS


def binary_name(os_name: str, arch: str) -> str:
    base = f"dist/myapp-{os_name}-{arch}"
    return base + ".exe" if os_name == "windows" else base


def steps_for_platform(p: dict) -> list[dict]:
    """Return a build step and a test step for one platform."""
    os_name = p["os"]
    arch    = p["arch"]
    image   = p["image"]
    binary  = binary_name(os_name, arch)
    artifact_name = f"binary-{os_name}-{arch}"

    build_step = {
        "id":         f"build-{os_name}-{arch}",
        "image":      image,
        "depends_on": ["matrix-generator"],
        "timeout":    "15m",
        "env": {
            "GOOS":        os_name,
            "GOARCH":      arch,
            "CGO_ENABLED": "0",
        },
        "run": "\n".join([
            "mkdir -p dist",
            f'go build -ldflags="-s -w" -o {binary} ./cmd/myapp',
            f"echo 'Built: {binary}'",
            f"ls -lh {binary}",
        ]),
        "artifacts": {
            "upload": [{"path": binary, "name": artifact_name}]
        }
    }

    # Tests run in parallel with the build — they only need the source tree,
    # not the compiled binary. If your tests need a built binary, add
    # depends_on: [f"build-{os_name}-{arch}"]
    test_step = {
        "id":         f"test-{os_name}-{arch}",
        "image":      image,
        "depends_on": ["matrix-generator"],
        "timeout":    "10m",
        "env": {
            "GOOS":   os_name,
            "GOARCH": arch,
        },
        "run": "go test ./... -count=1 -race",
    }

    return [build_step, test_step]


def main():
    platforms = load_platforms()

    # Optional: skip platforms based on env flags.
    # e.g. SKIP_WINDOWS=1 to speed up draft PRs.
    if os.environ.get("SKIP_WINDOWS"):
        platforms = [p for p in platforms if p["os"] != "windows"]
        print("[info] SKIP_WINDOWS set — skipping Windows builds", file=sys.stderr)

    print(f"[info] Generating steps for {len(platforms)} platforms", file=sys.stderr)

    steps = []
    for p in platforms:
        steps.extend(steps_for_platform(p))

    # Write artifact manifest so the release step knows all artifact names.
    artifact_names = [f"binary-{p['os']}-{p['arch']}" for p in platforms]
    manifest_path = "/workspace/.forge/artifact-manifest.json"
    os.makedirs(os.path.dirname(manifest_path), exist_ok=True)
    with open(manifest_path, "w") as f:
        json.dump({"artifacts": artifact_names, "platforms": platforms}, f, indent=2)

    print(json.dumps(steps, indent=2))


if __name__ == "__main__":
    main()