<script lang="ts">
    import { onMount } from 'svelte';
    import { api, type AgentInfo } from '../api';
    import { Server, Activity, Cpu, Package, RefreshCw } from '@lucide/svelte';

    let agents: AgentInfo[] = [];
    let loading = true;
    let error = '';

    async function loadAgents() {
        loading = true;
        try {
            const list = await api.listAgents();
            agents = list.sort((a, b) => a.id.localeCompare(b.id));
            error = '';
        } catch (e) {
            console.error("Failed to load agents:", e);
            error = 'Failed to load agents dashboard.';
        } finally {
            loading = false;
        }
    }

    onMount(() => {
        loadAgents();
        const interval = setInterval(loadAgents, 10000);
        return () => clearInterval(interval);
    });

    function fmtTime(date: string | Date) {
        if (!date) return 'never';
        const d = new Date(date);
        return d.toLocaleTimeString();
    }

    function isStale(date: string | Date) {
        if (!date) return true;
        const d = new Date(date);
        return (new Date().getTime() - d.getTime()) > 30000; // 30s
    }
</script>

<div class="view-container">
    <div class="view-header">
        <div class="header-left">
            <h2>Runner Health</h2>
            <p class="subtitle">Self-hosted agents connected to this scheduler</p>
        </div>
        <button class="btn-secondary" on:click={loadAgents}>
            <RefreshCw size={16} class={loading ? 'spin' : ''} />
            Refresh
        </button>
    </div>

    {#if loading && agents.length === 0}
        <div class="loading">Loading agent status...</div>
    {:else if error}
        <div class="error-box">{error}</div>
    {:else if agents.length === 0}
        <div class="empty-state">
            <Server size={48} />
            <h3>No Agents Connected</h3>
            <p>Start an agent using the Forge CLI or Docker image to see it here.</p>
        </div>
    {:else}
        <div class="agents-grid">
            {#each agents as agent}
                <div class="agent-card card" class:stale={isStale(agent.last_heartbeat) || !agent.connected}>
                    <div class="agent-header">
                        <div class="agent-title">
                            <Server size={18} class="icon" />
                            <span class="id">{agent.id.slice(0, 12)}</span>
                            {#if agent.connected && !isStale(agent.last_heartbeat)}
                                <span class="status-badge online">online</span>
                            {:else}
                                <span class="status-badge offline">offline</span>
                            {/if}
                        </div>
                        <div class="last-seen">
                            Seen: {fmtTime(agent.last_heartbeat)}
                        </div>
                    </div>

                    <div class="metrics-row">
                        <div class="metric">
                            <Activity size={14} />
                            <span class="label">Jobs</span>
                            <span class="value">{agent.active_jobs_count} / {agent.concurrency}</span>
                        </div>
                        <div class="metric">
                            <Package size={14} />
                            <span class="label">Images</span>
                            <span class="value">{agent.docker_images}</span>
                        </div>
                        <div class="metric">
                            <Cpu size={14} />
                            <span class="label">Version</span>
                            <span class="value">{agent.version || 'v0.1.0'}</span>
                        </div>
                    </div>

                    {#if agent.labels && Object.keys(agent.labels).length > 0}
                        <div class="labels">
                            {#each Object.entries(agent.labels) as [k, v]}
                                <span class="tag">{k}={v}</span>
                            {/each}
                        </div>
                    {/if}
                </div>
            {/each}
        </div>
    {/if}

    <div class="deployment-guide card">
        <div class="guide-header">
            <Package size={20} />
            <h3>Connect a new Runner</h3>
        </div>
        <p>Save the following as <code>docker-compose.yml</code> on your server to connect a new self-hosted agent.</p>
        
        <div class="code-block">
            <div class="code-header">docker-compose.yml</div>
            <pre><code>services:
  agent:
    image: ghcr.io/jbraunsmajr/forge/forge:latest
    command: ["agent"]
    restart: unless-stopped
    environment:
      FORGE_SCHEDULER_URL: {window.location.origin}
      FORGE_API_TOKEN: <b>YOUR_AGENT_TOKEN</b>
    volumes:
      - /var/run/docker.sock:/var/run/docker.sock</code></pre>
        </div>

        <p>Then start the agent:</p>
        <div class="code-block">
            <pre><code>docker compose up -d</code></pre>
        </div>

        <div class="guide-notes">
            <p><strong>Note:</strong> Replace <code>YOUR_AGENT_TOKEN</code> with a token created in the <a href="/tokens">Tokens</a> page with the <code>agent</code> role.</p>
        </div>
    </div>
</div>

<style>
    .deployment-guide {
        margin-top: 40px;
        padding: 24px;
        background: var(--surface);
    }
    .guide-header {
        display: flex;
        align-items: center;
        gap: 10px;
        margin-bottom: 12px;
    }
    .guide-header h3 {
        margin: 0;
    }
    .code-block {
        background: #000;
        color: #fff;
        border-radius: 6px;
        font-family: var(--font-mono);
        font-size: 13px;
        margin: 16px 0;
        overflow: hidden;
    }
    .code-header {
        background: #222;
        padding: 8px 16px;
        font-size: var(--font-xs);
        color: #888;
        border-bottom: 1px solid #333;
    }
    .code-block pre { 
        margin: 0; 
        padding: 16px;
        overflow-x: auto;
    }
    .guide-notes {
        font-size: 13px;
        color: var(--muted);
    }
    .guide-notes a { color: var(--primary); text-decoration: none; }
    .guide-notes a:hover { text-decoration: underline; }

    .subtitle {
        color: var(--muted);
        margin: 4px 0 0 0;
        font-size: 14px;
    }
    .agents-grid {
        display: grid;
        grid-template-columns: repeat(auto-fill, minmax(300px, 1fr));
        gap: 20px;
        margin-top: 20px;
    }
    .agent-card {
        padding: 20px;
        background: var(--surface);
        border: 1px solid var(--border);
        border-radius: 8px;
        transition: transform 0.2s, border-color 0.2s;
    }
    .agent-card.stale {
        opacity: 0.7;
        border-style: dashed;
    }
    .agent-header {
        display: flex;
        justify-content: space-between;
        align-items: center;
        margin-bottom: 20px;
    }
    .agent-title {
        display: flex;
        align-items: center;
        gap: 10px;
    }
    .agent-title .id {
        font-family: var(--font-mono);
        font-weight: 600;
        font-size: 14px;
        color: var(--text);
    }
    .status-badge {
        font-size: var(--font-2xs);
        text-transform: uppercase;
        font-weight: 700;
        padding: 2px 6px;
        border-radius: 4px;
    }
    .status-badge.online { background: rgba(16, 185, 129, 0.1); color: #10b981; }
    .status-badge.offline { background: rgba(239, 68, 68, 0.1); color: #ef4444; }
    
    .last-seen {
        font-size: var(--font-xs);
        color: var(--muted);
    }
    .metrics-row {
        display: flex;
        gap: 20px;
        margin-bottom: 16px;
    }
    .metric {
        display: flex;
        flex-direction: column;
        gap: 4px;
        color: var(--muted);
    }
    .metric .label { font-size: var(--font-2xs); text-transform: uppercase; }
    .metric .value { font-size: 14px; color: var(--text); font-weight: 600; }
    
    .labels {
        display: flex;
        flex-wrap: wrap;
        gap: 6px;
        border-top: 1px solid var(--border);
        padding-top: 12px;
    }
    .tag {
        font-size: var(--font-2xs);
        background: var(--surface2);
        color: var(--muted);
        padding: 2px 6px;
        border-radius: 3px;
        font-family: var(--font-mono);
    }
    .empty-state {
        text-align: center;
        padding: 64px 0;
        color: var(--muted);
    }
    .loading {
        text-align: center;
        padding: 32px;
        color: var(--muted);
    }
    .error-box {
        background: rgba(239, 68, 68, 0.1);
        color: #ef4444;
        padding: 12px 16px;
        border-radius: 6px;
        margin-bottom: 20px;
    }
</style>
