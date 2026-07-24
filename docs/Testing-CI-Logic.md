# Testing CI Logic

In Forge, your CI pipeline logic isn't trapped in a wall of escaped YAML. It lives in real source files—Python, Go, Node.js—which means it should be treated like any other production code.

**Your CI logic is real code. Here's how to test it.**

## The Core Convention

The reason CI scripts often feel untestable is that they are written as a single block of logic that immediately performs I/O (like printing JSON or writing files). 

The fix is simple: **Separate your logic from your output.**

1.  **Logic**: A pure function that takes inputs (data, environment flags) and returns a list of Forge step definitions.
2.  **Entrypoint**: A thin wrapper that collects inputs (from `os.environ` or files), calls the logic function, and prints the result to `stdout`.

---

## The Generator Protocol

Every generator script in Forge follows a simple standard:

1.  **Input (stdin)**: A JSON object containing the current run context (`pipeline_name`, `ref`, `commit_sha`, `env`, etc.).
2.  **Output (stdout)**: A JSON array of Forge step definitions.
3.  **Logs (stderr)**: Diagnostic messages (captured and displayed in the Forge UI).

### Input Schema (stdin)

```json
{
  "pipeline_name": "Forge Main",
  "workspace_dir": "/workspace",
  "org_id": "org_abc123",
  "project_id": "proj_xyz456",
  "ref": "refs/heads/main",
  "commit_sha": "a1b2c3d4...",
  "env": { "CUSTOM_VAR": "val" },
  "with": { "param": "val" }
}
```

---

## Language Examples

### Python (pytest)

Use `json.load(sys.stdin)` to grab your context. In tests, you can mock `sys.stdin` using `io.StringIO`.

**`scripts/ci/generate_matrix.py`**
```python
import json, sys, os

def generate_matrix(platforms, skip_windows=False):
    # Pure logic
    ...

def main():
    # Load context from stdin (provided by Forge)
    try:
        input_data = json.load(sys.stdin)
    except:
        input_data = {}
        
    skip = os.getenv("SKIP_WINDOWS") == "1" or input_data.get("env", {}).get("SKIP_WINDOWS") == "1"
    steps = generate_matrix(load_platforms(), skip)
    print(json.dumps(steps))
```

**`scripts/ci/test_generate_matrix.py`**
```python
import io, json
from generate_matrix import generate_matrix

def test_logic():
    # Test the pure function
    ...

def test_main_with_stdin(monkeypatch):
    # Test the entrypoint by mocking stdin
    mock_input = json.dumps({"env": {"SKIP_WINDOWS": "1"}})
    monkeypatch.setattr('sys.stdin', io.StringIO(mock_input))
    
    # Now calling main() would see SKIP_WINDOWS=1
```

### Go (go test)

**`scripts/ci/generate-matrix.go`**
```go
func main() {
    var input api.GeneratorInput
    json.NewDecoder(os.Stdin).Decode(&input)
    
    steps := GenerateMatrix(platforms, input.Env["SKIP_WINDOWS"] == "1")
    json.NewEncoder(os.Stdout).Encode(steps)
}
```

### Node.js (jest)

**`scripts/ci/generate-matrix.js`**
```javascript
if (require.main === module) {
  const input = JSON.parse(require('fs').readFileSync(0, 'utf8'));
  const steps = generateMatrix(platforms, input.env.SKIP_WINDOWS === "1");
  console.log(JSON.stringify(steps));
}
```

---

## Previewing with `generate-preview`

Forge provides a built-in tool to see exactly what your generator will produce without running the full pipeline. This is a great "middle ground" between unit tests and full CI runs.

```bash
# Preview all generators in a pipeline
forge generate-preview .forge/pipeline.yml

# Target a specific generator step
forge generate-preview .forge/pipeline.yml --step my-generator
```

This command runs the generator locally and prints the resulting JSON steps in a pretty-printed format. It even supports secret injection:

```bash
forge generate-preview --secret GITHUB_TOKEN=shhh
```

## Why this matters

By following these conventions:
- **IDE Support**: You get syntax highlighting, linting, and autocomplete.
- **Fast Feedback**: Run `pytest` or `go test` in milliseconds instead of waiting minutes for a CI runner.
- **Complexity Management**: If your monorepo logic gets complex, you can use real data structures and abstractions instead of 500 lines of YAML.
