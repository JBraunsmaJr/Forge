<script lang="ts">
    import { activeRun, selectedJob, artifacts, navigateToRunID } from '../stores';
    import { api, wsUrl, type Job, type RunComparison, type RunDetail } from '../api';
    import { RotateCcw, XCircle, TrendingUp, TrendingDown, Hash } from '@lucide/svelte';
    import { onDestroy } from 'svelte';

    import DetailsPanel from './DetailsPanel.svelte';
    import DAGNodes from './DAGNodes.svelte';
    import { computeLayout, STATUS_COLORS } from '../dagLayout';


    let expandedSteps = new Set<string>();
    let expandedPipelineJobs = new Set<string>();
    // Every job ID that's ever been auto-expanded or manually toggled —
    // deliberately a plain Set, mutated in place and never reassigned,
    // so Svelte's reactivity system does NOT treat it as a dependency
    // of any $: block. See the two auto-expand reactive statements
    // below for why that matters: they must react to new run data
    // appearing, not to expandedPipelineJobs itself changing (which
    // happens on every manual collapse too, and previously caused a
    // collapse to be immediately undone by the very reactive block
    // meant to catch a newly-started child pipeline).
    let autoExpandConsidered = new Set<string>();
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

    // Fetches a child run's detail into the cache (if not already
    // there), marks it expanded, and opens a live socket if it's still
    // running. Shared by the manual-click path (onTogglePipeline) and
    // the auto-expand-on-load path below, so both stay in sync with
    // exactly the same fetch/cache/socket behavior.
    let expansionGeneration = 0;

    async function ensurePipelineExpanded(
        j: Job,
        depth = 0,
        generation = expansionGeneration,
    ): Promise<void> {
        autoExpandConsidered.add(j.job_id);
        expandedPipelineJobs.add(j.job_id);
        expandedPipelineJobs = expandedPipelineJobs;

        if (!j.child_run_id) return;
        let detail = childRunCache[j.child_run_id];
        if (!detail && !loadingChildRuns.has(j.child_run_id)) {
            loadingChildRuns.add(j.child_run_id);
            loadingChildRuns = loadingChildRuns;
            let fetched: RunDetail | null = null;
            try {
                fetched = await api.runDetail(j.child_run_id);
            } catch {
                fetched = null;
            } finally {
                // Only clear this generation's own loading flag. A stale
                // request finishing after a later generation started must
                // not clear the flag the new generation just set for the
                // same run — otherwise a fast navigate-away-and-back
                // arrives with a loading entry from the wrong generation
                // and the current fetch is dropped as "already loading".
                if (generation === expansionGeneration) {
                    loadingChildRuns.delete(j.child_run_id);
                    loadingChildRuns = loadingChildRuns;
                }
            }
            if (generation !== expansionGeneration) return;
            if (!fetched) {
                expandedPipelineJobs.delete(j.job_id);
                expandedPipelineJobs = expandedPipelineJobs;
                autoExpandConsidered.delete(j.job_id);
                return;
            }
            childRunCache = { ...childRunCache, [j.child_run_id]: fetched };
            detail = fetched;
        }
        if (!detail) return;

        if (generation !== expansionGeneration) return;
        if (detail.status !== 'passed' && detail.status !== 'failed') {
            openChildSocket(j.child_run_id);
        }

        // Child pipelines should always be visible by default, not just
        // the first level — recurse into this child's own pipeline-type
        // jobs too, up to the same depth cap the renderer already
        // enforces (MAX_NEST_DEPTH), so a long chain doesn't cascade
        // into an unbounded burst of fetches on initial load.
        if (depth + 1 < MAX_NEST_DEPTH) {
            for (const childJob of detail.jobs) {
                if (childJob.child_run_id) {
                    ensurePipelineExpanded(childJob, depth + 1, generation);
                }
            }
        }
    }

    async function onTogglePipeline(j: Job) {
        // A manual click is a deliberate choice either way — record it
        // so the auto-expand reactive block below never second-guesses
        // it, whether the user just opened or closed this node.
        autoExpandConsidered.add(j.job_id);
        if (expandedPipelineJobs.has(j.job_id)) {
            expandedPipelineJobs.delete(j.job_id);
            expandedPipelineJobs = expandedPipelineJobs;
            if (j.child_run_id) closeChildSocket(j.child_run_id);
            return;
        }
        await ensurePipelineExpanded(j);
    }

    onDestroy(() => {
        for (const runID of Object.keys(childSockets)) closeChildSocket(runID);
    });

    let lastRunID: string | undefined;
    $: if ($activeRun?.run_id !== lastRunID) {
        expansionGeneration += 1;
        lastRunID = $activeRun?.run_id;
        for (const runID of Object.keys(childSockets)) closeChildSocket(runID);
        expandedPipelineJobs = new Set();
        autoExpandConsidered = new Set();
        childRunCache = {};
        // Also reset per-generation loading state, otherwise a
        // still-in-flight request from before this navigation can be
        // mistaken for "already loading" under the new generation and
        // block the new fetch from ever starting.
        loadingChildRuns = new Set();
        // Always show child pipeline structure, live or historical —
        // not just when a user happens to click to expand it while
        // watching a run execute.
        for (const j of $activeRun?.jobs ?? []) {
            if (j.child_run_id) ensurePipelineExpanded(j);
        }
    }

    // Runs on every $activeRun update, including live websocket ticks —
    // catches a pipeline job whose child_run_id only appears once that
    // child run actually starts, not just ones already present when
    // this run was first loaded above.
    //
    // Deliberately gated on autoExpandConsidered, not expandedPipelineJobs:
    // the latter is reassigned (and so reactively re-triggers this block)
    // on every manual collapse too, which previously meant collapsing a
    // pipeline node immediately re-expanded it — this block would see
    // "has child_run_id, not in expandedPipelineJobs" and treat a
    // deliberate collapse as a new appearance to auto-expand.
    // autoExpandConsidered is only ever added to, never reassigned, so
    // it isn't itself a reactive dependency here — this block only
    // re-runs when $activeRun changes, which is exactly the "did new
    // data arrive" signal it's meant to react to.
    $: for (const j of $activeRun?.jobs ?? []) {
        if (j.child_run_id && !autoExpandConsidered.has(j.job_id)) {
            ensurePipelineExpanded(j);
        }
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
            <span class="dag-eyebrow">Pipeline DAG</span>
            {#if $activeRun}
                <span class="dag-run-name">{$activeRun.name}</span>

                {#if $activeRun.build_number}
                    <span class="build-badge" title="FORGE_BUILD_NUMBER for this run">
                        <Hash size={13} />
                        {$activeRun.build_number}
                    </span>
                {/if}

                {#if comparison && comparison.diff_ms !== 0}
                    <span class="comparison-badge" class:regression={comparison.regression_detected}>
                        {#if comparison.regression_detected}
                            <TrendingUp size={13} />
                        {:else}
                            <TrendingDown size={13} />
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
                    <!-- Dots rather than ruled squares: the graph is read
                         along its edges, and a line grid competes with them
                         at exactly the angles the edges use. Dots give the
                         same sense of a positioned canvas without drawing
                         anything that could be mistaken for a connection. -->
                    <pattern id="grid" width="24" height="24" patternUnits="userSpaceOnUse">
                        <circle cx="1" cy="1" r="1" fill="rgba(255,255,255,0.055)"/>
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
                            refX="9"
                            refY="5"
                            markerWidth="5"
                            markerHeight="5"
                            orient="auto-start-reverse"
                        >
                            <!-- Solid head, matching the reference: a filled
                                 triangle stays legible at the small marker
                                 size an orthogonal route needs, where an
                                 open chevron blurs into the line. -->
                            <path
                                d="M2 1.5L9 5L2 8.5Z"
                                fill={c.stroke}
                                fill-opacity="0.75"
                                stroke="none"
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
    #dag-panel h2 { font-weight: 600; color: var(--muted);
        padding: 14px 20px 10px; margin: 0; border: none; display: flex; align-items: baseline; flex-wrap: wrap; gap: 8px; }
    .dag-eyebrow {
        font-size: 11px; letter-spacing: 1px; text-transform: uppercase; color: var(--muted);
    }
    .dag-eyebrow::after { content: '—'; margin-left: 8px; }
    .dag-run-name {
        font-size: var(--font-lg);
        font-weight: 600;
        color: var(--text);
    }
    .actions { display: flex; gap: 8px; }
    .btn-cancel, .btn-rerun {
        background: var(--surface2);
        border: 1px solid var(--border);
        color: var(--text);
        border-radius: 4px;
        padding: 4px 10px;
        font-size: var(--font-xs);
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
        padding: 2px 8px;
        border-radius: 8px;
        font-size: var(--font-xs);
        font-weight: 700;
        margin-left: 8px;
    }

    .build-badge {
        display: inline-flex;
        align-items: center;
        gap: 3px;
        background: var(--surface2);
        color: var(--muted);
        padding: 3px 10px;
        border-radius: 8px;
        font-size: var(--font-sm);
        font-weight: 700;
        font-family: var(--font-mono);
        margin-left: 12px;
        vertical-align: middle;
        text-transform: none;
        letter-spacing: 0;
    }

    .comparison-badge {
        display: inline-flex;
        align-items: center;
        gap: 4px;
        background: rgba(16, 185, 129, 0.1);
        color: #10b981;
        padding: 3px 10px;
        border-radius: 8px;
        font-size: var(--font-sm);
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
</style>
