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
    # Support both -t and --tag flags, and handle optional quotes (with spaces)
    match = re.search(r"docker\s+build.*?(?:-t|--tag)\s+(?:([\"'])(.*?)\1|(\S+))", run + " " + command)
    if not match:
        return None
    return match.group(2) or match.group(3)

def main():
    data = json.load(sys.stdin)
    steps = data.get("steps", [])

    build_steps = {
        s["id"]: extract_image_tag(s)
        for s in steps
        if is_docker_build(s)
    }

    # Filter out steps where we couldn't find an image tag.
    # Trivy cannot scan an image if we don't know its name.
    build_steps = {k: v for k, v in build_steps.items() if v}

    if not build_steps:
        print(json.dumps(steps, indent=2))
        return

    result = []
    scan_map = {}

    for step in steps:
        result.append(step)

        if step["id"] in build_steps:
            image_tag = build_steps[step["id"]]
            report_id = f"trivy-report-{step['id']}"
            scan_id = f"trivy-scan-{step['id']}"
            scan_map[step["id"]] = scan_id
            result.append({
                "id":      report_id,
                "image":   "aquasec/trivy:latest",
                "docker_socket": True,
                "command": ["image", "--severity", "HIGH,CRITICAL", "--no-progress",
                            "--format", "template", "--template", "@/contrib/html.tpl",
                            "-o", f"{scan_id}.html", image_tag],
                "depends_on": [step["id"]],
                "workdir": "/workspace",
                "artifact_uploads": [
                    {"path": f"{scan_id}.html", "name": f"Container Security Report ({step['id']})"}
                ]
            })
            result.append({
                "id":      scan_id,
                "image":   "aquasec/trivy:latest",
                "docker_socket": True,
                "command": ["image", "--severity", "HIGH,CRITICAL", "--no-progress", image_tag],
                "env":     {"TRIVY_EXIT_CODE": "1"},
                "depends_on": [report_id],
                "workdir": "/workspace",
            })

    # All injected steps should be excluded from dependency rewiring to avoid cycles.
    injected_ids = set(scan_map.values()) | {f"trivy-report-{k}" for k in build_steps.keys()}
    for step in result:
        if step["id"] in injected_ids:
            continue
        if step.get("depends_on"):
            step["depends_on"] = [
                scan_map.get(dep, dep)
                for dep in step["depends_on"]
            ]

    print(json.dumps(result, indent=2))

if __name__ == "__main__":
    main()