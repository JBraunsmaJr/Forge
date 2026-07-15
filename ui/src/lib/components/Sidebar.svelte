<script lang="ts">
    import { onMount } from 'svelte';
    import { api, type Run } from '../api';
    import { runs, activeRun, connStatus, currentView, sidebarOpen } from '../stores';
    import { Play, Briefcase, Building2, ShieldCheck, Key, Server, Layout, Search, Shield } from '@lucide/svelte';

    let search = '';
    let statusFilter = '';
    let offset = 0;
    const PAGE_SIZE = 50;
    let hasMore = false;
    let filterTimer: number;

    async function refreshRunList(append = false) {
        const result = await api.listRuns(PAGE_SIZE, offset, search, statusFilter);
        if (append) {
            runs.update(current => [...current, ...result]);
        } else {
            runs.set(result);
        }
        hasMore = result.length === PAGE_SIZE;
    }

    function onFilterChange() {
        clearTimeout(filterTimer);
        filterTimer = setTimeout(() => {
            offset = 0;
            refreshRunList();
        }, 250);
    }

    async function loadMore() {
        offset += PAGE_SIZE;
        await refreshRunList(true);
    }

    function timeAgo(iso: string) {
        const s = Math.floor((Date.now() - new Date(iso).getTime()) / 1000);
        if (s < 60)   return `${s}s ago`;
        if (s < 3600) return `${Math.floor(s/60)}m ago`;
        return `${Math.floor(s/3600)}h ago`;
    }

    function statusBadge(status: string) {
        const labels: Record<string, string> = {
            passed: 'passed', failed: 'failed', running: 'running',
            queued: 'queued', pending: 'pending', canceled: 'canceled',
            timed_out: 'timed out', approval: 'waiting for approval',
            release: 'releasing'
        };
        return labels[status] || status;
    }

    onMount(() => {
        refreshRunList();
        const interval = setInterval(refreshRunList, 5000);
        return () => clearInterval(interval);
    });

    export let onSelectRun: (id: string) => void;
</script>

<aside>
    <div id="nav">
        <div class="nav-logo">⚒</div>
        <button class:active={$currentView === 'runs'} on:click={() => currentView.set('runs')} title="Runs">
            <Play size={20} />
            <span>Runs</span>
        </button>
        <button class:active={$currentView === 'editor'} on:click={() => currentView.set('editor')} title="Pipeline Editor">
            <Layout size={20} />
            <span>Editor</span>
        </button>
        <button class:active={$currentView === 'search'} on:click={() => currentView.set('search')} title="Log Search">
            <Search size={20} />
            <span>Search</span>
        </button>
        <button class:active={$currentView === 'projects'} on:click={() => currentView.set('projects')} title="Projects">
            <Briefcase size={20} />
            <span>Projects</span>
        </button>
        <button class:active={$currentView === 'orgs'} on:click={() => currentView.set('orgs')} title="Organizations">
            <Building2 size={20} />
            <span>Orgs</span>
        </button>
        <button class:active={$currentView === 'policies'} on:click={() => currentView.set('policies')} title="Policies">
            <ShieldCheck size={20} />
            <span>Policies</span>
        </button>
        <button class:active={$currentView === 'tokens'} on:click={() => currentView.set('tokens')} title="API Tokens">
            <Key size={20} />
            <span>Tokens</span>
        </button>
        <button class:active={$currentView === 'agents'} on:click={() => currentView.set('agents')} title="Runners Health">
            <Server size={20} />
            <span>Runners</span>
        </button>
        <button class:active={$currentView === 'audit'} on:click={() => currentView.set('audit')} title="Audit Logs">
            <Shield size={20} />
            <span>Audit</span>
        </button>
    </div>

    {#if $currentView === 'runs'}
    <div id="pane" class:open={$sidebarOpen}>
        <h2>Runs</h2>
        <div id="run-filters">
            <input 
                type="text" 
                placeholder="Search pipelines…" 
                bind:value={search} 
                on:input={onFilterChange}
            >
            <select bind:value={statusFilter} on:change={onFilterChange}>
                <option value="">All statuses</option>
                <option value="running">Running</option>
                <option value="passed">Passed</option>
                <option value="failed">Failed</option>
                <option value="canceled">Canceled</option>
                <option value="approval">Waiting for Approval</option>
            </select>
        </div>
        <div id="run-list">
            {#if $runs.length === 0}
                <div id="empty-state">No runs yet.<br>Submit a pipeline to start.</div>
            {:else}
                {#each $runs as run}
                    <button 
                        class="run-item" 
                        class:active={$activeRun?.run_id === run.run_id}
                        on:click={() => { onSelectRun(run.run_id); sidebarOpen.set(false); }}
                        type="button"
                    >
                        <div class="run-item-name">{run.name}</div>
                        <div class="run-item-meta">
                            <span class="badge badge-{run.status}">{statusBadge(run.status)}</span>
                            <span>{run.job_count} job{run.job_count !== 1 ? 's' : ''}</span>
                            <span>{timeAgo(run.created_at)}</span>
                        </div>
                    </button>
                {/each}
            {/if}
            {#if hasMore}
                <button id="load-more-btn" on:click={loadMore}>Load more</button>
            {/if}
        </div>
    </div>
    {/if}
</aside>

<style>
    aside {
        display: flex;
        background: var(--surface);
        border-right: 1px solid var(--border);
        flex-shrink: 0;
        z-index: 10;
    }
    #nav {
        width: 72px;
        background: var(--bg);
        border-right: 1px solid var(--border);
        display: flex;
        flex-direction: column;
        align-items: center;
        padding: 12px 0;
        gap: 8px;
        flex-shrink: 0;
    }
    .nav-logo {
        font-size: 24px;
        margin-bottom: 16px;
        color: var(--accent);
        height: 48px;
        display: flex;
        align-items: center;
        justify-content: center;
    }
    #nav button {
        width: 48px;
        height: 48px;
        background: transparent;
        border: none;
        color: var(--muted);
        cursor: pointer;
        border-radius: 12px;
        transition: all .2s;
        display: flex;
        flex-direction: column;
        align-items: center;
        justify-content: center;
        gap: 2px;
    }
    #nav button span {
        font-size: 8px;
        font-weight: 700;
        text-transform: uppercase;
        letter-spacing: 0.2px;
    }
    #nav button:hover {
        background: var(--surface);
        color: var(--text);
    }
    #nav button.active {
        background: var(--surface2);
        color: var(--accent);
    }
    #pane {
        width: 280px;
        display: flex;
        flex-direction: column;
    }
    #pane h2 {
        font-size: 11px;
        font-weight: 600;
        letter-spacing: 1px;
        color: var(--muted);
        text-transform: uppercase;
        padding: 16px 16px 8px;
    }
    #run-list {
        flex: 1;
        overflow-y: auto;
    }
    #run-filters {
        padding: 8px 12px;
        display: flex;
        flex-direction: column;
        gap: 6px;
        border-bottom: 1px solid var(--border);
        flex-shrink: 0;
    }
    input, select {
        width: 100%;
        background: var(--bg);
        color: var(--text);
        border: 1px solid var(--border);
        border-radius: 4px;
        padding: 5px 8px;
        font-size: 12px;
        box-sizing: border-box;
    }
    input:focus {
        outline: none;
        border-color: var(--accent);
    }
    #load-more-btn {
        width: 100%;
        margin: 0;
        padding: 8px;
        background: var(--surface);
        color: var(--muted);
        border: none;
        border-top: 1px solid var(--border);
        cursor: pointer;
        font-size: 12px;
        flex-shrink: 0;
    }
    #load-more-btn:hover {
        color: var(--text);
    }
    .run-item {
        padding: 12px 16px;
        border: none;
        border-bottom: 1px solid var(--border);
        cursor: pointer;
        transition: background .15s;
        background: transparent;
        color: inherit;
        text-align: left;
        width: 100%;
        display: block;
        font-family: inherit;
    }
    .run-item:hover {
        background: var(--surface2);
    }
    .run-item.active {
        background: var(--surface2);
        border-left: 3px solid var(--accent);
    }
    .run-item-name {
        font-weight: 600;
        font-size: 13px;
        margin-bottom: 4px;
        white-space: nowrap;
        overflow: hidden;
        text-overflow: ellipsis;
    }
    .run-item-meta {
        display: flex;
        align-items: center;
        gap: 8px;
        font-size: 11px;
        color: var(--muted);
    }
    .badge {
        padding: 2px 7px;
        border-radius: 10px;
        font-size: 10px;
        font-weight: 700;
        text-transform: uppercase;
        letter-spacing: .5px;
    }
    .badge-running, .badge-release { background: #1a2f4a; color: var(--blue); }
    .badge-passed  { background: #0d2e20; color: var(--green); }
    .badge-failed  { background: #2e1414; color: var(--red); }
    .badge-approval { background: #3b2b10; color: var(--amber); }
    .badge-pending, .badge-queued { background: var(--surface2); color: var(--muted); }
    #empty-state { padding: 32px 16px; text-align: center; color: var(--muted); font-size: 13px; }

    @media (max-width: 768px) {
        aside {
            flex-direction: column-reverse;
            width: 100%;
            height: auto;
            position: fixed;
            bottom: 0;
            left: 0;
            right: 0;
            border-right: none;
            border-top: 1px solid var(--border);
        }
        #nav {
            width: 100%;
            height: 64px;
            flex-direction: row;
            justify-content: space-around;
            padding: 0;
            border-right: none;
        }
        .nav-logo { display: none; }
        #nav button {
            width: auto;
            flex: 1;
            height: 100%;
            border-radius: 0;
        }
        #pane {
            display: none; /* Hide run list by default on mobile */
        }
        #pane.open {
            display: flex;
            position: fixed;
            top: 52px;
            bottom: 64px;
            left: 0;
            right: 0;
            width: 100%;
            background: var(--bg);
            z-index: 20;
        }
    }
</style>
