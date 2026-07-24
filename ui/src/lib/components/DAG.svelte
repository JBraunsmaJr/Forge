<script lang="ts">
    import { activeRun, selectedJob, artifacts, navigateToRunID } from '../stores';
    import { api, wsUrl, type Job, type RunComparison, type RunDetail } from '../api';
    import { RotateCcw, XCircle, TrendingUp, TrendingDown } from '@lucide/svelte';
    import { onDestroy } from 'svelte';

    import DetailsPanel from './DetailsPanel.svelte';
    import DAGNodes from './DAGNodes.svelte';
    import { computeLayout, STATUS_COLORS } from '../dagLayout';


    let expandedSteps = new Set<string>();
    let expandedPipelineJobs = new Set<string>();
    let childRunCache: Record<string, RunDetail> = {};
    let loadingChildRuns = new Set<string>();
    let childSockets: Record<string, WebSocket> = {};


    const MAX_NEST_DEPTH = 3;

    function toggleExpand(stepID: string) {
        if (expandedSteps.has(stepID)) {
            expandedSteps.delete(stepID);
        } else {
            expandedSteps.add(stepID);
        }
        expandedSteps = expandedSteps;
    }

    function closeChildSocket(runID: string) {
        childSockets[runID]?.close();
        delete childSockets[runID];
    }

    function openChildSocket(runID: string) {
        if (childSockets[runID]) return; // already live
        const ws = new WebSocket(wsUrl(`/api/v1/runs/${runID}/events`));
        ws.onmessage = (e) => {
            const updated: RunDetail = JSON.parse(e.data);
            childRunCache = { ...childRunCache, [runID]: updated };
            if (updated.status === 'passed' || updated.status === 'failed') {
                closeChildSocket(runID);
            }
        };
        ws.onerror = () => closeChildSocket(runID);
        childSockets[runID] = ws;
    }

    async function onTogglePipeline(j: Job) {
        if (expandedPipelineJobs.has(j.job_id)) {
            expandedPipelineJobs.delete(j.job_id);
            expandedPipelineJobs = expandedPipelineJobs;
            if (j.child_run_id) closeChildSocket(j.child_run_id);
            return;
        }
        expandedPipelineJobs.add(j.job_id);
        expandedPipelineJobs = expandedPipelineJobs;

        if (!j.child_run_id) return;
        if (!childRunCache[j.child_run_id] && !loadingChildRuns.has(j.child_run_id)) {
            loadingChildRuns.add(j.child_run_id);
            loadingChildRuns = loadingChildRuns;
            const detail = await api.runDetail(j.child_run_id);
            loadingChildRuns.delete(j.child_run_id);
            loadingChildRuns = loadingChildRuns;
            if (detail) {
                childRunCache = { ...childRunCache, [j.child_run_id]: detail };
            }
        }
        const cached = childRunCache[j.child_run_id];
        if (cached && cached.status !== 'passed' && cached.status !== 'failed') {
            openChildSocket(j.child_run_id);
        }
    }

    onDestroy(() => {
        for (const runID of Object.keys(childSockets)) closeChildSocket(runID);
    });

    let lastRunID: string | undefined;
    $: if ($activeRun?.run_id !== lastRunID) {
        lastRunID = $activeRun?.run_id;
        for (const runID of Object.keys(childSockets)) closeChildSocket(runID);
        expandedPipelineJobs = new Set();
        childRunCache = {};
    }

    function fmtDuration(ms: number) {
        if (!ms) return '';
        if (ms < 1000) return `${ms}ms`;
        return `${(ms/1000).toFixed(1)}s`;
    }


    $: layout = ($activeRun && $activeRun.jobs)
        ? computeLayout($activeRun.jobs, $activeRun.shard_assignments, expandedPipelineJobs, expandedSteps, childRunCache)
        : null;
    $: jobHasArtifacts = (jobID: string) => $artifacts.some(a => a.job_id === jobID);

    async function cancel() {
        if ($activeRun && confirm('Cancel this run?')) {
            await api.cancelRun($activeRun.run_id);
        }
    }

    async function rerun() {
        if ($activeRun && confirm('Rerun this pipeline?')) {
            const res = await api.rerunRun($activeRun.run_id);
            if (res) {
                
                
            }
        }
    }

    async function rerunFailed() {
        if ($activeRun && confirm('Rerun failed jobs?')) {
            await api.rerunFailed($activeRun.run_id);
        }
    }

    export let onOpenDebug: (job: Job) => void;

    let comparison: RunComparison | null = null;

    async function fetchComparison(runID: string | undefined) {
        if (!runID) {
            comparison = null;
            return;
        }
        comparison = await api.runComparison(runID);
    }

    $: fetchComparison($activeRun?.run_id);
</script>

<div id="dag-panel">
    <div id="dag-header">
        <h2>
            Pipeline DAG — 
            {#if $activeRun}
                {$activeRun.name}

                {#if comparison && comparison.diff_ms !== 0}
                    <span class="comparison-badge" class:regression={comparison.regression_detected}>
                        {#if comparison.regression_detected}
                            <TrendingUp size={12} />
                        {:else}
                            <TrendingDown size={12} />
                        {/if}
                        {fmtDuration(Math.abs(comparison.diff_ms))} {comparison.diff_ms > 0 ? 'slower' : 'faster'} than avg
                    </span>
                {/if}
            {:else}
                no run selected
            {/if}
        </h2>
        {#if $activeRun}
            <div class="actions">
                {#if $activeRun.status === 'running' || $activeRun.status === 'queued'}
                    <button class="btn-cancel" on:click={cancel} title="Cancel Run">
                        <XCircle size={14} />
                        Cancel
                    </button>
                {:else}
                    {#if $activeRun.status === 'failed'}
                        <button class="btn-rerun" on:click={rerunFailed} title="Rerun Failed Jobs">
                            <RotateCcw size={14} />
                            Rerun Failed
                        </button>
                    {/if}
                    <button class="btn-rerun" on:click={rerun} title="Rerun Pipeline">
                        <RotateCcw size={14} />
                        Rerun
                    </button>
                {/if}
            </div>
        {/if}
    </div>
    <div id="dag-scroll" on:click={() => selectedJob.set(null)}>
        {#if !$activeRun}
            <div id="dag-empty">Select a run from the sidebar to view its pipeline graph.</div>
        {:else if layout}
            <svg 
                width={layout.svgW} 
                height={layout.svgH} 
                viewBox="0 0 {layout.svgW} {layout.svgH}"
                on:click|stopPropagation={() => selectedJob.set(null)}
            >
                <defs>
                    <pattern id="grid" width="40" height="40" patternUnits="userSpaceOnUse">
                        <path d="M 40 0 L 0 0 0 40" fill="none" stroke="rgba(255,255,255,0.03)" stroke-width="1"/>
                    </pattern>
                    <filter id="shadow" x="-20%" y="-20%" width="140%" height="140%">
                        <feGaussianBlur in="SourceAlpha" stdDeviation="2" />
                        <feOffset dx="0" dy="1" result="offsetblur" />
                        <feComponentTransfer>
                            <feFuncA type="linear" slope="0.4" />
                        </feComponentTransfer>
                        <feMerge>
                            <feMergeNode />
                            <feMergeNode in="SourceGraphic" />
                        </feMerge>
                    </filter>
                    <filter id="selected-shadow" x="-20%" y="-20%" width="140%" height="140%">
                        <feGaussianBlur in="SourceAlpha" stdDeviation="4" />
                        <feOffset dx="0" dy="2" result="offsetblur" />
                        <feComponentTransfer>
                            <feFuncA type="linear" slope="0.6" />
                        </feComponentTransfer>
                        <feMerge>
                            <feMergeNode />
                            <feMergeNode in="SourceGraphic" />
                        </feMerge>
                    </filter>
                    {#each Object.entries(STATUS_COLORS) as [status, c]}
                        <marker 
                            id="arrow-{status}" 
                            viewBox="0 0 10 10" 
                            refX="8" 
                            refY="5"
                            markerWidth="6" 
                            markerHeight="6" 
                            orient="auto-start-reverse"
                        >
                            <path 
                                d="M2 1L8 5L2 9" 
                                fill="none" 
                                stroke={c.stroke}
                                stroke-width="1.5" 
                                stroke-linecap="round"
                            />
                        </marker>
                    {/each}
                </defs>
                <rect width="100%" height="100%" fill="url(#grid)" />

                <!-- All node/edge rendering — including recursive nesting for
                     expanded pipeline groups — lives in DAGNodes.svelte. This
                     component only owns the <svg> viewport, <defs>, and the
                     header/toolbar chrome above it. -->
                <DAGNodes
                    resolvedJobs={layout.resolvedJobs}
                    {layout}
                    shardAssignments={$activeRun.shard_assignments}
                    depth={0}
                    maxDepth={MAX_NEST_DEPTH}
                    {onOpenDebug}
                    onToggleShard={toggleExpand}
                    {onTogglePipeline}
                    {jobHasArtifacts}
                />
            </svg>
        {/if}
    </div>
</div>

<style>
    #dag-panel { flex: 1; position: relative; overflow: hidden; display: flex; flex-direction: column; }
    #dag-header {
        display: flex;
        justify-content: space-between;
        align-items: center;
        border-bottom: 1px solid var(--border);
        padding-right: 20px;
    }
    #dag-panel h2 { font-size: 11px; font-weight: 600; letter-spacing: 1px; color: var(--muted);
        text-transform: uppercase; padding: 14px 20px 10px; margin: 0; border: none; }
    .actions { display: flex; gap: 8px; }
    .btn-cancel, .btn-rerun {
        background: var(--surface2);
        border: 1px solid var(--border);
        color: var(--text);
        border-radius: 4px;
        padding: 4px 10px;
        font-size: 10px;
        font-weight: 700;
        display: flex;
        align-items: center;
        gap: 6px;
        cursor: pointer;
        text-transform: uppercase;
    }
    .btn-cancel:hover { background: #2e1414; color: var(--red); border-color: var(--red); }
    .btn-rerun:hover { background: var(--bg); border-color: var(--accent); color: var(--accent); }

    #dag-scroll { overflow: auto; flex: 1; padding: 24px; }
    #dag-empty { position: absolute; top: 50%; left: 50%; transform: translate(-50%, -50%);
        color: var(--muted); text-align: center; font-size: 13px; }

    .policy-badge {
        background: #2e1f5e;
        color: #a78bfa;
        padding: 1px 6px;
        border-radius: 8px;
        font-size: 10px;
        font-weight: 700;
        margin-left: 8px;
    }

    .comparison-badge {
        display: inline-flex;
        align-items: center;
        gap: 4px;
        background: rgba(16, 185, 129, 0.1);
        color: #10b981;
        padding: 1px 8px;
        border-radius: 8px;
        font-size: 10px;
        font-weight: 700;
        margin-left: 12px;
        vertical-align: middle;
        text-transform: none;
        letter-spacing: 0;
    }
    .comparison-badge.regression {
        background: rgba(239, 68, 68, 0.1);
        color: #ef4444;
    }

    .dag-node { cursor: pointer; }
    .dag-node rect { transition: all .2s; }
    .dag-node:hover rect { filter: brightness(1.2); transform: translateY(-1px); }
    
    .artifact-icon { display: flex; align-items: center; justify-content: center; opacity: 0.8; }
    .node-actions-top {
        display: flex;
        justify-content: flex-end;
        align-items: center;
        gap: 6px;
        height: 100%;
        padding-right: 4px;
    }
    .node-rerun-btn {
        background: none;
        border: none;
        padding: 3px;
        cursor: pointer;
        color: var(--muted);
        display: flex;
        align-items: center;
        justify-content: center;
        border-radius: 4px;
        transition: all 0.2s;
    }
    .node-rerun-btn:hover {
        background: rgba(255,255,255,0.1);
        color: var(--accent);
    }
    .node-rerun-btn.approve:hover {
        color: #fbbf24;
    }
    .node-debug-link {
        font-size: 10px;
        font-weight: 700;
        cursor: pointer;
        text-transform: uppercase;
        letter-spacing: 0.5px;
        transition: all 0.2s;
    }
    .node-debug-link:hover { fill: #a78bfa; }
    .node-expand-btn {
        background: none;
        border: none;
        padding: 2px;
        cursor: pointer;
        color: var(--muted);
        display: flex;
        align-items: center;
        justify-content: center;
        border-radius: 4px;
        transition: all 0.2s;
    }
    .node-expand-btn:hover {
        background: rgba(255,255,255,0.1);
        color: var(--accent);
    }
    .dag-label { font-family: 'Inter', system-ui, sans-serif; font-size: 13px; font-weight: 700;
        pointer-events: none; }
    .dag-sub { font-family: 'Inter', system-ui, sans-serif; font-size: 10px; font-weight: 500; pointer-events: none; }
    .dag-sub.policy { font-size: 9px; }
</style>
