<script lang="ts">
    import { activeRun, selectedJob, artifacts } from '../stores';
    import { api } from '../api';
    import LogViewer from './LogViewer.svelte';
    import ArtifactViewer from './ArtifactViewer.svelte';
    import Terminal from './Terminal.svelte';
    import { Maximize2, Minimize2, Terminal as TerminalIcon, FileText, Package } from '@lucide/svelte';

    export let debugSession: { sessionID: string | null, status: 'starting' | 'ready' | 'closed', expiresInS: number };
    export let onCloseDebug: () => void;

    let activeTab: 'logs' | 'artifacts' | 'terminal' = 'logs';
    let expanded = false;

    $: if (debugSession.sessionID && activeTab !== 'terminal') {
        activeTab = 'terminal';
    }

    async function fetchArtifacts(runID: string | undefined) {
        if (!runID) {
            artifacts.set([]);
            return;
        }
        const list = await api.runArtifacts(runID);
        artifacts.set(list);
    }

    $: fetchArtifacts($activeRun?.run_id);

    $: hasArtifacts = $selectedJob 
        ? $artifacts.some(a => a.job_id === $selectedJob.job_id) 
        : $artifacts.length > 0;

    $: if (!hasArtifacts && activeTab === 'artifacts') {
        activeTab = 'logs';
    }

    $: if (!debugSession.sessionID && activeTab === 'terminal') {
        activeTab = 'logs';
    }
</script>

<div id="details-panel" class:expanded>
    <div id="details-header">
        <div class="tabs">
            <button class:active={activeTab === 'logs'} on:click={() => activeTab = 'logs'}>
                <FileText size={12} />
                Logs
            </button>
            {#if hasArtifacts}
                <button class:active={activeTab === 'artifacts'} on:click={() => activeTab = 'artifacts'}>
                    <Package size={12} />
                    Artifacts
                </button>
            {/if}
            {#if debugSession.sessionID}
                <button class:active={activeTab === 'terminal'} on:click={() => activeTab = 'terminal'}>
                    <TerminalIcon size={12} />
                    Terminal
                </button>
            {/if}
        </div>
        <div class="actions">
            <button class="expand-btn" on:click={() => expanded = !expanded} title={expanded ? "Collapse" : "Expand"}>
                {#if expanded}
                    <Minimize2 size={14} />
                {:else}
                    <Maximize2 size={14} />
                {/if}
            </button>
        </div>
    </div>
    <div id="details-content">
        {#if activeTab === 'logs'}
            <LogViewer hideHeader={true} />
        {:else if activeTab === 'artifacts'}
            <ArtifactViewer hideHeader={true} />
        {:else if activeTab === 'terminal'}
            <Terminal 
                sessionID={debugSession.sessionID} 
                status={debugSession.status} 
                expiresInS={debugSession.expiresInS}
                on:close={onCloseDebug}
                hideHeader={true}
            />
        {/if}
    </div>
</div>

<style>
    #details-panel {
        display: flex;
        flex-direction: column;
        background: var(--surface);
        border-top: 1px solid var(--border);
        height: 280px;
        transition: height 0.15s ease-out;
        flex-shrink: 0;
    }
    #details-panel.expanded {
        height: calc(100vh - 100px);
    }
    #details-header {
        display: flex;
        justify-content: space-between;
        align-items: center;
        background: var(--surface2);
        padding: 0 10px;
        border-bottom: 1px solid var(--border);
        flex-shrink: 0;
    }
    .tabs {
        display: flex;
    }
    .tabs button {
        background: none;
        border: none;
        color: var(--muted);
        padding: 10px 16px;
        font-size: 11px;
        font-weight: 700;
        text-transform: uppercase;
        cursor: pointer;
        display: flex;
        align-items: center;
        gap: 8px;
        border-bottom: 2px solid transparent;
        transition: all 0.2s;
    }
    .tabs button:hover {
        color: var(--text);
        background: rgba(255,255,255,0.03);
    }
    .tabs button.active {
        color: var(--accent);
        border-bottom-color: var(--accent);
        background: rgba(110, 109, 240, 0.05);
    }
    .expand-btn {
        background: none;
        border: none;
        color: var(--muted);
        cursor: pointer;
        padding: 6px;
        display: flex;
        align-items: center;
        border-radius: 4px;
    }
    .expand-btn:hover {
        color: var(--text);
        background: var(--surface);
    }
    #details-content {
        flex: 1;
        overflow: hidden;
        display: flex;
        flex-direction: column;
    }
</style>
