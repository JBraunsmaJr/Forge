# Forge

The programmable CI/CD engine for modern teams. Forge is a container-native CI/CD system that uses Go-based logic (via generators) instead of complex YAML templating.

---

## Why Forge?

Traditional CI/CD systems rely on static YAML. When you need complex logic (dynamic matrices, monorepo change detection, cross-repo triggers), you end up with thousands of lines of unmaintainable YAML or brittle bash scripts.

**Forge changes this by making the pipeline dynamic.** You can emit new pipeline steps at runtime using any language (Python, Go, Node), allowing your CI/CD to adapt to your code, not the other way around.

---

## Core Features

- 🚀 **Dynamic Pipelines**: Emit steps at runtime based on your code's state.
- 🐳 **Container Native**: Every step runs in a clean Docker container.
- 🛠️ **Live Debugging**: SSH/Terminal directly into failing job containers from the Web UI.
- 🛡️ **Org-wide Policies**: Automatically inject security scans into every pipeline.
- 📦 **Smart Artifacts**: Built-in artifact store with automatic fan-in/fan-out.
- 📊 **Real-time UI**: Watch logs and status updates as they happen via SSE.

---

## Architecture

```mermaid
graph TD
    subgraph IG ["Ingress"]
        CLI[forge CLI]
        UI[Web UI]
        SCM[GitHub/GitLab]
    end

    Scheduler["Scheduler (:8080)"]
    
    subgraph INF ["Infrastructure"]
        DB[(PostgreSQL)]
        Vault[Vault]
        S3[MinIO/S3]
    end

    subgraph WK ["Workers"]
        Agent1["Agent 1"]
        Agent2["Agent 2"]
    end

    IG --> Scheduler
    Scheduler --> INF
    
    Agent1 -- "Polls" --> Scheduler
    Agent2 -- "Polls" --> Scheduler
    
    Agent1 -- "Logs/Status" --> Scheduler
    Agent2 -- "Logs/Status" --> Scheduler

    Agent1 -- "Secrets" --> Vault
    Agent2 -- "Secrets" --> Vault
    
    Agent1 -- "Artifacts" --> S3
    Agent2 -- "Artifacts" --> S3
```

The **scheduler** accepts pipeline submissions, applies org policies, stores jobs in PostgreSQL, and serves the Web UI. **Agents** poll the scheduler, lease jobs, execute them in Docker containers, stream logs back in real time, and upload/download artifacts to S3-compatible storage. The browser connects **directly** to agents for debug terminal sessions — the scheduler is not in the hot path for interactive use.
