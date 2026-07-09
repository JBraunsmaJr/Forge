#!/usr/bin/env python3
"""
container-security-transformer.py

Receives the pipeline as JSON on stdin.
Inspects steps for 'docker build' commands.
Injects a Trivy vulnerability scan after each build step and
rewires downstream dependencies through the scan.
"""
import json
import sys
import re

def is_docker_build(step):
    run = step.get("run", "") or ""
    command = " ".join(step.get("command", []) or [])
    return bool(re.search(r"docker\s+build", run + " " + command))

def extract_image_tag(step):
    run = step.get("run", "") or ""
    command = " ".join(step.get("command", []) or [])
    match = re.search(r"docker\s+build.*?-t\s+(\S+)", run + " " + command)
    return match.group(1) if match else "$(docker images -q | head -1)"

def main():
    data = json.load(sys.stdin)
    steps = data.get("steps", [])

    build_steps = {
        s["id"]: extract_image_tag(s)
        for s in steps
        if is_docker_build(s)
    }

    if not build_steps:
        print(json.dumps(steps))
        return

    result = []
    scan_map = {}

    for step in steps:
        result.append(step)

        if step["id"] in build_steps:
            image_tag = build_steps[step["id"]]
            scan_id = f"trivy-scan-{step['id']}"
            scan_map[step["id"]] = scan_id
            result.append({
                "id":      scan_id,
                "image":   "aquasec/trivy:latest",
                "command": ["image", "--severity", "HIGH,CRITICAL",
                            "--no-progress", image_tag],
                "env":     {"TRIVY_EXIT_CODE": "1"},
                "depends_on": [step["id"]],
                "workdir": "/workspace",
            })

    scan_ids = set(scan_map.values())
    for step in result:
        if step["id"] in scan_ids:
            continue
        if step.get("depends_on"):
            step["depends_on"] = [
                scan_map.get(dep, dep)
                for dep in step["depends_on"]
            ]

    print(json.dumps(result, indent=2))

if __name__ == "__main__":
    main()