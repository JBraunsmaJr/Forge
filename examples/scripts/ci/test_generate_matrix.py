import io
import json
import os
import pytest
from generate_matrix import generate_matrix, steps_for_platform, main

def test_steps_for_platform():
    p = {"os": "linux", "arch": "amd64", "image": "golang:1.26-alpine"}
    steps = steps_for_platform(p)
    
    assert len(steps) == 2
    assert steps[0]["id"] == "build-linux-amd64"
    assert steps[1]["id"] == "test-linux-amd64"
    assert steps[0]["env"]["GOOS"] == "linux"
    assert steps[1]["env"]["GOARCH"] == "amd64"

def test_generate_matrix_basic():
    platforms = [
        {"os": "linux", "arch": "amd64", "image": "golang:1.26-alpine"},
        {"os": "darwin", "arch": "arm64", "image": "golang:1.26-alpine"},
    ]
    steps = generate_matrix(platforms)
    
    # 2 platforms -> 4 steps
    assert len(steps) == 4
    ids = [s["id"] for s in steps]
    assert "build-linux-amd64" in ids
    assert "test-linux-amd64" in ids
    assert "build-darwin-arm64" in ids
    assert "test-darwin-arm64" in ids

def test_generate_matrix_skip_windows():
    platforms = [
        {"os": "linux", "arch": "amd64", "image": "golang:1.26-alpine"},
        {"os": "windows", "arch": "amd64", "image": "golang:1.26-alpine"},
    ]
    
    # Case 1: no skip
    steps = generate_matrix(platforms, skip_windows=False)
    assert len(steps) == 4
    
    # Case 2: skip windows
    steps = generate_matrix(platforms, skip_windows=True)
    assert len(steps) == 2
    for s in steps:
        assert "windows" not in s["id"]

def test_main_with_stdin_context(monkeypatch, capsys):
    # Mock stdin to provide SKIP_WINDOWS context
    input_ctx = {
        "pipeline_name": "Test Pipeline",
        "env": {"SKIP_WINDOWS": "1"}
    }
    monkeypatch.setattr('sys.stdin', io.StringIO(json.dumps(input_ctx)))
    
    # We also need to mock load_platforms since it looks for platforms.json
    monkeypatch.setattr('generate_matrix.load_platforms', lambda _: [
        {"os": "linux", "arch": "amd64", "image": "golang:1.26-alpine"},
        {"os": "windows", "arch": "amd64", "image": "golang:1.26-alpine"}
    ])
    
    # Mock write_manifest to avoid file I/O
    monkeypatch.setattr('generate_matrix.write_manifest', lambda _a, _b: None)

    main()
    
    out, err = capsys.readouterr()
    steps = json.loads(out)
    
    # 2 platforms, but windows skipped -> 2 steps (build-linux, test-linux)
    assert len(steps) == 2
    for s in steps:
        assert "windows" not in s["id"]
    assert "Read generator context from stdin (pipeline: Test Pipeline)" in err
