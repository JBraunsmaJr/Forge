<script lang="ts">
    import { activeRun, selectedJob, artifacts } from '../stores';
    import { api } from '../api';
    import { onMount, onDestroy } from 'svelte';
    import LogViewer from './LogViewer.svelte';
    import ArtifactViewer from './ArtifactViewer.svelte';
    import GanttChart from './GanttChart.svelte';
    import Terminal from './Terminal.svelte';
    import { Maximize2, Minimize2, Terminal as TerminalIcon, FileText, Package, Clock, X, Check, TrendingUp } from '@lucide/svelte';

    export let debugSession: { sessionID: string | null, status: 'starting' | 'ready' | 'closed', expiresInS: number };
    export let onCloseDebug: () => void;

    let activeTab: 'logs' | 'artifacts' | 'terminal' | 'timing' | 'shards' = 'logs';
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

    let ttlInterval: any;
    let ttlText = '';

    function startTTLTimer(seconds: number) {
        let remaining = seconds;
        clearInterval(ttlInterval);
        const update = () => {
            if (remaining <= 0) {
                clearInterval(ttlInterval);
                ttlText = 'expired';
                return;
            }
            const m = Math.floor(remaining / 60);
            const s = remaining % 60;
            ttlText = `TTL ${m}:${String(s).padStart(2, '0')}`;
            remaining--;
        };
        update();
        ttlInterval = setInterval(update, 1000);
    }

    $: if (debugSession.expiresInS > 0 && activeTab === 'terminal') {
        startTTLTimer(debugSession.expiresInS);
    } else {
        clearInterval(ttlInterval);
        ttlText = '';
    }

    const statusLabels = {
        starting: 'connecting…',
        ready: 'ready',
        closed: 'closed'
    };

    async function handleApprove() {
        if (!$selectedJob) return;
        const ok = await api.approveJob($selectedJob.job_id);
        if (ok) {
            // refresh run detail
            const detail = await api.runDetail($activeRun!.run_id);
            activeRun.set(detail);
            // Re-select the job to update its status in the panel
            const updatedJob = detail?.jobs.find(j => j.job_id === $selectedJob!.job_id);
            if (updatedJob) selectedJob.set(updatedJob);
        } else {
            alert('Failed to approve step');
        }
    }

    function fmtDuration(ms: number) {
        if (!ms) return '0s';
        if (ms < 1000) return `${ms}ms`;
        if (ms < 90000) return `${(ms/1000).toFixed(1)}s`;
        return `${Math.floor(ms/60000)}m ${Math.round((ms%60000)/1000)}s`;
    }

    // The job backing a shard card, e.g. "integration-tests-shard-2".
    // Used to show the actual runtime and status next to the estimate.
    function shardJob(stepId: string, shardIndex: number) {
        return ($activeRun?.jobs || []).find(
            (j) => j.step_id === `${stepId}-shard-${shardIndex + 1}`
        );
    }

    // Assignments are stored under the ORIGINAL step id; shard jobs are
    // named "<step>-shard-N". Normalize so the Shards tab appears whether
    // the user selects the fan-in node or an individual shard.
    function shardBaseStep(stepId: string): string {
        return stepId.split('-shard-')[0];
    }
    $: shardStepKey = $selectedJob ? shardBaseStep($selectedJob.step_id) : '';
    $: shardList = shardStepKey ? $activeRun?.shard_assignments?.[shardStepKey] : undefined;

    onDestroy(() => {
        clearInterval(ttlInterval);
    });
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
            <button class:active={activeTab === 'timing'} on:click={() => activeTab = 'timing'}>
                <Clock size={12} />
                Timing
            </button>
            {#if $selectedJob && shardList}
                <button class:active={activeTab === 'shards'} on:click={() => activeTab = 'shards'}>
                    <TrendingUp size={12} />
                    Shards
                </button>
            {/if}
        </div>
        <div class="actions">
            {#if $selectedJob?.status === 'approval'}
                <button class="approve-btn" on:click={handleApprove}>
                    <Check size={14} />
                    Approve Step
                </button>
            {/if}
            {#if activeTab === 'terminal' && debugSession.sessionID}
                <div class="terminal-controls">
                    <span class="status {debugSession.status}">
                        <span class="dot"></span>
                        {statusLabels[debugSession.status]}
                    </span>
                    <span class="ttl">{ttlText}</span>
                    <button class="close-session-btn" on:click={onCloseDebug} title="Close Debug Session">
                        <X size={14} />
                        Close Session
                    </button>
                </div>
            {/if}
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
            />
        {:else if activeTab === 'timing'}
            <div class="timing-tab-content">
                <GanttChart jobs={$activeRun?.jobs || []} />
            </div>
        {:else if activeTab === 'shards'}
            <div class="shards-tab-content">
                {#if $selectedJob && shardList}
                    {#each shardList as shard}
                        {@const job = shardJob(shardStepKey, shard.shard_index)}
                        <div class="shard-card">
                            <div class="shard-card-header">
                                <span class="shard-name">Shard {shard.shard_index + 1} of {shard.total_shards}</span>
                                {#if job && job.duration_ms > 0}
                                    <span class="shard-est shard-actual-{job.status}">
                                        {fmtDuration(job.duration_ms)}
                                        {#if shard.estimated_ms > 0}(est. {fmtDuration(shard.estimated_ms)}){/if}
                                    </span>
                                {:else if job && (job.status === 'running' || job.status === 'waiting')}
                                    <span class="shard-est">Running{#if shard.estimated_ms > 0} — est. {fmtDuration(shard.estimated_ms)}{/if}</span>
                                {:else if shard.estimated_ms > 0}
                                    <span class="shard-est">Estimated {fmtDuration(shard.estimated_ms)}</span>
                                {:else}
                                    <span class="shard-est">No estimate yet</span>
                                {/if}
                            </div>
                            <div class="shard-files">
                                {#if shard.file_paths && shard.file_paths.length > 0}
                                    {#each shard.file_paths as file}
                                        <div class="file-item">{file}</div>
                                    {/each}
                                {:else}
                                    <div class="file-item">No timing history yet — this shard ran the full suite. Assignments appear once test reports have been recorded.</div>
                                {/if}
                            </div>
                        </div>
                    {/each}
                {/if}
            </div>
        {/if}
    </div>
</div>

<style>
    .timing-tab-content {
        flex: 1;
        overflow-y: auto;
    }
    .shards-tab-content {
        flex: 1;
        overflow-y: auto;
        padding: 20px;
        display: grid;
        grid-template-columns: repeat(auto-fill, minmax(300px, 1fr));
        gap: 20px;
        background: var(--bg);
    }
    .shard-card {
        background: var(--surface2);
        border: 1px solid var(--border);
        border-radius: 8px;
        display: flex;
        flex-direction: column;
        overflow: hidden;
    }
    .shard-card-header {
        padding: 10px 14px;
        background: rgba(255,255,255,0.03);
        border-bottom: 1px solid var(--border);
        display: flex;
        justify-content: space-between;
        align-items: center;
    }
    .shard-name { font-size: 11px; font-weight: 700; color: var(--text); text-transform: uppercase; }
    .shard-actual-passed { color: var(--ok, #4caf50); }
    .shard-actual-failed { color: var(--err, #f44336); }

    .shard-est { font-size: 10px; color: var(--muted); }
    .shard-files { padding: 10px 14px; font-family: var(--font-mono); font-size: 10px; color: var(--muted); }
    .file-item { margin-bottom: 4px; white-space: nowrap; overflow: hidden; text-overflow: ellipsis; }

    #details-panel {
        display: flex;
        flex-direction: column;
        background: var(--surface);
        border-top: 1px solid var(--border);
        height: 320px;
        transition: all 0.15s ease-out;
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
    .actions {
        display: flex;
        align-items: center;
    }
    .approve-btn {
        background: var(--green);
        color: white;
        border: none;
        padding: 4px 12px;
        font-size: 11px;
        font-weight: 700;
        border-radius: 4px;
        cursor: pointer;
        display: flex;
        align-items: center;
        gap: 6px;
        margin-right: 12px;
        transition: opacity 0.2s;
    }
    .approve-btn:hover {
        opacity: 0.9;
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
    .terminal-controls {
        display: flex;
        align-items: center;
        gap: 12px;
        margin-right: 12px;
        padding-right: 12px;
        border-right: 1px solid var(--border);
    }
    .ttl {
        font-family: var(--font-mono);
        font-size: 11px;
        color: var(--muted);
    }
    .status {
        font-size: 11px;
        display: flex;
        align-items: center;
        gap: 6px;
        color: var(--muted);
    }
    .status .dot {
        width: 6px;
        height: 6px;
        border-radius: 50%;
        background: currentColor;
    }
    .status.starting { color: var(--amber); }
    .status.ready    { color: var(--green); }
    .status.closed   { color: var(--muted); }
    
    .close-session-btn {
        background: none;
        border: 1px solid var(--border);
        color: var(--muted);
        padding: 4px 10px;
        font-size: 11px;
        font-weight: 600;
        border-radius: 4px;
        cursor: pointer;
        display: flex;
        align-items: center;
        gap: 6px;
        transition: all 0.2s;
    }
    .close-session-btn:hover {
        border-color: var(--red);
        color: var(--red);
        background: rgba(239, 68, 68, 0.05);
    }
    #details-content {
        flex: 1;
        overflow: hidden;
        display: flex;
        flex-direction: column;
    }
</style>
