<script lang="ts">
    import { onMount } from 'svelte';
    import { api, type Run } from '../api';
    import { runs, activeRun, connStatus, currentView } from '../stores';
    import { Play, Briefcase, Building2, ShieldCheck, Key } from '@lucide/svelte';

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

    onMount(() => {
        refreshRunList();
        const interval = setInterval(refreshRunList, 5000);
        return () => clearInterval(interval);
    });

    export let onSelectRun: (id: string) => void;
</script>

<aside>
    <div id="nav">
        <button class:active={$currentView === 'runs'} on:click={() => currentView.set('runs')} title="Runs">
            <Play size={18} />
            <span>Runs</span>
        </button>
        <button class:active={$currentView === 'projects'} on:click={() => currentView.set('projects')} title="Projects">
            <Briefcase size={18} />
            <span>Projects</span>
        </button>
        <button class:active={$currentView === 'orgs'} on:click={() => currentView.set('orgs')} title="Organizations">
            <Building2 size={18} />
            <span>Orgs</span>
        </button>
        <button class:active={$currentView === 'policies'} on:click={() => currentView.set('policies')} title="Policies">
            <ShieldCheck size={18} />
            <span>Policies</span>
        </button>
        <button class:active={$currentView === 'tokens'} on:click={() => currentView.set('tokens')} title="API Tokens">
            <Key size={18} />
            <span>Tokens</span>
        </button>
    </div>

    {#if $currentView === 'runs'}
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
        </select>
    </div>
    <div id="run-list">
        {#if $runs.length === 0}
            <div id="empty-state">No runs yet.<br>Submit a pipeline to start.</div>
        {:else}
            {#each $runs as run}
                
                
                <div 
                    class="run-item" 
                    class:active={$activeRun?.run_id === run.run_id}
                    on:click={() => onSelectRun(run.run_id)}
                >
                    <div class="run-item-name">{run.name}</div>
                    <div class="run-item-meta">
                        <span class="badge badge-{run.status}">{run.status}</span>
                        <span>{run.job_count} job{run.job_count !== 1 ? 's' : ''}</span>
                        <span>{timeAgo(run.created_at)}</span>
                    </div>
                </div>
            {/each}
        {/if}
        {#if hasMore}
            <button id="load-more-btn" on:click={loadMore}>Load more</button>
        {/if}
    </div>
    {/if}
</aside>

<style>
    #nav {
        display: flex;
        background: var(--bg);
        border-bottom: 1px solid var(--border);
        padding: 4px;
        gap: 4px;
    }
    #nav button {
        flex: 1;
        display: flex;
        flex-direction: column;
        align-items: center;
        justify-content: center;
        gap: 4px;
        padding: 8px 0;
        background: transparent;
        border: none;
        color: var(--muted);
        cursor: pointer;
        border-radius: 4px;
        transition: all .15s;
    }
    #nav button span {
        font-size: 9px;
        text-transform: uppercase;
        font-weight: 600;
        letter-spacing: 0.5px;
    }
    #nav button:hover {
        background: var(--surface);
        color: var(--text);
    }
    #nav button.active {
        background: var(--surface2);
        color: var(--accent);
    }
    aside {
        width: 280px;
        background: var(--surface);
        border-right: 1px solid var(--border);
        display: flex;
        flex-direction: column;
        flex-shrink: 0;
    }
    aside h2 {
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
        border-bottom: 1px solid var(--border);
        cursor: pointer;
        transition: background .15s;
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
    .badge-running { background: #1a2f4a; color: var(--blue); }
    .badge-passed  { background: #0d2e20; color: var(--green); }
    .badge-failed  { background: #2e1414; color: var(--red); }
    .badge-pending, .badge-queued { background: var(--surface2); color: var(--muted); }
    #empty-state { padding: 32px 16px; text-align: center; color: var(--muted); font-size: 13px; }
</style>
