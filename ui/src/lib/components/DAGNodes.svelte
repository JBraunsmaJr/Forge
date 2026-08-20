<script lang="ts">
    import { selectedJob, navigateToRunID } from '../stores';
    import { api, type Job, type ShardAssignmentDetail } from '../api';
    import {
        GROUP_HEADER_H, GROUP_PAD, STATUS_COLORS,
        CARD_FILL, CARD_FILL_SELECTED, CARD_STROKE, CARD_STROKE_SELECTED,
        HANDLE_FILL, CARD_R, RAIL_W, ICON_TILE, ICON_X, TEXT_X,
        elbowPath, tint,
        type LayoutResult, summarizeStatuses,
    } from '../dagLayout';
    import {
        RotateCcw, CheckCircle, Package, ChevronRight, ChevronDown, ExternalLink,
        Workflow, Layers, Rocket, UserCheck, Terminal,
    } from '@lucide/svelte';

    // Icon tile contents. The tile encodes what KIND of step this is —
    // something the step id alone often doesn't reveal at a glance in a
    // wide graph — while colour is reserved entirely for status. Keeping
    // the two channels separate means neither has to be read through the
    // other.
    const STEP_ICONS = {
        pipeline: Workflow,
        shards: Layers,
        release: Rocket,
        approval: UserCheck,
        step: Terminal,
    };

    function stepIcon(j: Job, hasShards: boolean) {
        if (j.child_run_id) return STEP_ICONS.pipeline;
        if (hasShards) return STEP_ICONS.shards;
        if (j.status === 'approval') return STEP_ICONS.approval;
        if (j.status === 'release' || j.status === 'docker_publish') return STEP_ICONS.release;
        return STEP_ICONS.step;
    }

    // An edge is drawn solid once its source has actually produced a
    // result, and dashed while it hasn't. The dash is not decoration: it
    // marks the part of the graph that has not been traversed yet, so a
    // half-finished run reads as a solid front advancing into dashed
    // territory rather than as a uniform web of identical lines.
    const SETTLED = new Set(['passed', 'failed', 'timed_out', 'canceled']);

    export let resolvedJobs: Job[];
    export let layout: LayoutResult;
    export let shardAssignments: Record<string, ShardAssignmentDetail[]> | undefined = undefined;
    export let depth = 0;
    export let maxDepth = 3;
    export let onOpenDebug: (job: Job) => void;
    export let onToggleShard: (stepId: string) => void;
    export let onTogglePipeline: (job: Job) => void;
    export let jobHasArtifacts: (jobID: string) => boolean;

    function fmtDuration(ms: number) {
        if (!ms) return '';
        if (ms < 1000) return `${ms}ms`;
        if (ms < 90000) return `${(ms / 1000).toFixed(1)}s`;
        return `${Math.floor(ms / 60000)}m ${Math.round((ms % 60000) / 1000)}s`;
    }

    function fmtTimeout(ns: number) {
        if (!ns) return '';
        const seconds = Math.floor(ns / 1000000000);
        if (seconds < 60) return `${seconds}s`;
        return `${Math.floor(seconds / 60)}m`;
    }

    function statusBadge(status: string) {
        const labels: Record<string, string> = {
            passed: 'passed', failed: 'failed', running: 'running…',
            waiting: 'waiting…', queued: 'queued', pending: 'pending',
            canceled: 'canceled', timed_out: 'timed out', skipped: 'skipped',
            approval: 'waiting for approval', release: 'releasing', docker_publish: 'publishing',
        };
        return labels[status] || status;
    }

    $: byStep = Object.fromEntries(resolvedJobs.map((j) => [j.step_id, j]));

    async function rerunJob(job: Job) {
        if (confirm(`Rerun job ${job.step_id} and its downstream?`)) {
            await api.rerunJob(job.job_id);
        }
    }

    async function approveJob(job: Job) {
        if (confirm(`Approve step ${job.step_id}?`)) {
            await api.approveJob(job.job_id);
        }
    }
</script>

<g class="edges">
    {#each resolvedJobs as j (j.job_id)}
        {#if layout.positions[j.job_id]}
            {#each j.depends_on || [] as depStep}
                {#if byStep[depStep] && layout.positions[byStep[depStep].job_id]}
                    {@const from = layout.positions[byStep[depStep].job_id]}
                    {@const to = layout.positions[j.job_id]}
                    {@const src = byStep[depStep]}
                    {@const fromH = from.nested ? GROUP_HEADER_H : from.h}
                    {@const toH = to.nested ? GROUP_HEADER_H : to.h}
                    {@const c = STATUS_COLORS[src.status] || STATUS_COLORS.pending}
                    {@const settled = SETTLED.has(src.status)}
                    <path
                        d={elbowPath(from.x + from.w, from.y + fromH / 2, to.x, to.y + toH / 2)}
                        fill="none"
                        stroke={c.stroke}
                        stroke-width="1.75"
                        stroke-linecap="round"
                        stroke-opacity={settled ? 0.55 : 0.32}
                        stroke-dasharray={settled ? undefined : '5 5'}
                        marker-end="url(#arrow-{src.status})"
                    />
                {/if}
            {/each}
        {/if}
    {/each}
</g>

<g class="nodes">
    {#each resolvedJobs as j (j.job_id)}
        {#if layout.positions[j.job_id]}
            {@const pos = layout.positions[j.job_id]}
            {@const c = STATUS_COLORS[j.status] || STATUS_COLORS.pending}
            {@const isSelected = j.job_id === $selectedJob?.job_id}
            {@const isGroup = !!pos.nested}
            {@const cardH = isGroup ? GROUP_HEADER_H : pos.h}
            {@const shards = shardAssignments?.[j.step_id]}
            {@const tileSize = isGroup ? 24 : ICON_TILE}
            {@const tileY = pos.y + (cardH - tileSize) / 2}
            {@const textX = pos.x + (isGroup ? ICON_X + tileSize + 10 : TEXT_X)}
            {@const Icon = stepIcon(j, !!shards)}

            <g
                class="dag-node"
                class:selected={isSelected}
                class:group-node={isGroup}
                on:click|stopPropagation={() => selectedJob.set(j)}
                filter={isSelected ? 'url(#selected-shadow)' : 'url(#shadow)'}
            >
                <!-- The rail is a full-height bar clipped to the card, so it
                     picks up the card's rounded corners exactly rather than
                     sitting inside them as a floating pill. -->
                <clipPath id="card-{j.job_id}">
                    <rect x={pos.x} y={pos.y} width={pos.w} height={cardH} rx={CARD_R} />
                </clipPath>

                <rect
                    x={pos.x} y={pos.y}
                    width={pos.w} height={cardH}
                    rx={CARD_R}
                    fill={isSelected ? CARD_FILL_SELECTED : CARD_FILL}
                    stroke={isSelected ? CARD_STROKE_SELECTED : CARD_STROKE}
                    stroke-width={isSelected ? '2' : '1'}
                />
                <g clip-path="url(#card-{j.job_id})">
                    <rect x={pos.x} y={pos.y} width={RAIL_W} height={cardH} fill={c.stroke} />
                </g>

                <rect
                    x={pos.x + ICON_X} y={tileY}
                    width={tileSize} height={tileSize}
                    rx={isGroup ? 7 : 10}
                    fill={tint(c.stroke, 0.14)}
                    stroke={tint(c.stroke, 0.38)}
                    stroke-width="1"
                />
                <foreignObject x={pos.x + ICON_X} y={tileY} width={tileSize} height={tileSize}>
                    <div class="node-icon">
                        <Icon size={isGroup ? 13 : 18} color={c.text} />
                    </div>
                </foreignObject>

                {#if isGroup}
                    <!-- Frame for the nested sub-DAG below the header bar -->
                    <rect
                        x={pos.x} y={pos.y + GROUP_HEADER_H}
                        width={pos.w} height={pos.h - GROUP_HEADER_H}
                        rx={CARD_R}
                        fill="rgba(255,255,255,0.015)"
                        stroke={c.stroke}
                        stroke-width="1"
                        stroke-dasharray="4 3"
                        stroke-opacity="0.4"
                    />
                {/if}

                <text
                    class="dag-label"
                    x={textX}
                    y={isGroup ? pos.y + cardH / 2 + 5 : (j.policy_source ? pos.y + 27 : pos.y + 30)}
                    fill={isSelected ? '#f1f3ff' : '#e2e4f0'}
                >
                    {j.step_id}{shards ? ` ×${shards.length}` : ''}
                </text>

                {#if !isGroup && j.policy_source}
                    <text class="dag-sub policy" x={textX} y={pos.y + 41} fill="#a78bfa">
                        🛡 {j.policy_source}
                    </text>
                {/if}

                {#if isGroup}
                    {@const summary = summarizeStatuses(pos.nested.runDetail.jobs)}
                    <text class="dag-sub group-summary" x={pos.x + pos.w - 34} y={pos.y + cardH / 2 + 4}
                          text-anchor="end" fill={c.text}>
                        {summary.label}
                    </text>
                {:else}
                    <text
                        class="dag-sub"
                        x={textX}
                        y={j.policy_source ? pos.y + 54 : pos.y + 48}
                        fill={c.text}
                    >
                        {statusBadge(j.status)}{#if j.duration_ms} · {fmtDuration(j.duration_ms)}{/if}{#if j.status === 'timed_out' && j.timeout_ns} · after {fmtTimeout(j.timeout_ns)}{/if}
                    </text>
                {/if}

                {#if !isGroup}
                    <foreignObject x={pos.x + pos.w - 60} y={pos.y + 8} width="52" height="24">
                        <div class="node-actions-top">
                            {#if jobHasArtifacts(j.job_id)}
                                <div class="artifact-icon" title="Produces Artifacts">
                                    <Package size={14} color={isSelected ? '#818cf8' : c.stroke} />
                                </div>
                            {/if}
                            {#if j.status === 'approval'}
                                <button class="node-rerun-btn approve" title="Approve Step"
                                        on:click|stopPropagation={() => approveJob(j)}>
                                    <CheckCircle size={14} />
                                </button>
                            {/if}
                            {#if j.status === 'passed' || j.status === 'failed' || j.status === 'canceled'}
                                <button class="node-rerun-btn" title="Rerun Job"
                                        on:click|stopPropagation={() => rerunJob(j)}>
                                    <RotateCcw size={14} />
                                </button>
                            {/if}
                        </div>
                    </foreignObject>
                {/if}

                {#if j.status === 'failed'}
                    <text
                        x={pos.x + pos.w - (j.step_id.includes('-shard-') ? 12 : 66)} y={pos.y + (isGroup ? GROUP_HEADER_H : pos.h) - 12}
                        text-anchor="end" fill="#818cf8"
                        class="node-debug-link"
                        on:click|stopPropagation={() => onOpenDebug(j)}
                    >
                        Debug →
                    </text>
                {/if}

                {#if shardAssignments?.[j.step_id]}
                    <foreignObject x={pos.x + pos.w - 28} y={pos.y + pos.h - 28} width="22" height="22">
                        <button class="node-expand-btn" on:click|stopPropagation={() => onToggleShard(j.step_id)}>
                            {#if isGroup}<ChevronDown size={14} />{:else}<ChevronRight size={14} />{/if}
                        </button>
                    </foreignObject>
                {/if}

                {#if j.child_run_id}
                    {#if depth < maxDepth}
                        <foreignObject x={pos.x + pos.w - 28} y={pos.y + 9} width="22" height="22">
                            <button class="node-expand-btn" title={isGroup ? 'Collapse child pipeline' : 'Expand child pipeline'}
                                    on:click|stopPropagation={() => onTogglePipeline(j)}>
                                {#if isGroup}<ChevronDown size={14} />{:else}<ChevronRight size={14} />{/if}
                            </button>
                        </foreignObject>
                    {:else}
                        <foreignObject x={pos.x + pos.w - 28} y={pos.y + 9} width="22" height="22">
                            <button class="node-expand-btn" title="Open child pipeline in main view"
                                    on:click|stopPropagation={() => navigateToRunID.set(j.child_run_id)}>
                                <ExternalLink size={12} />
                            </button>
                        </foreignObject>
                    {/if}
                {/if}
            </g>

            {#if pos.nested}
                <g transform="translate({pos.x + GROUP_PAD}, {pos.y + GROUP_HEADER_H + GROUP_PAD})">
                    <svelte:self
                        resolvedJobs={pos.nested.layout.resolvedJobs}
                        layout={pos.nested.layout}
                        shardAssignments={pos.nested.runDetail.shard_assignments}
                        depth={depth + 1}
                        {maxDepth}
                        {onOpenDebug}
                        {onToggleShard}
                        {onTogglePipeline}
                        {jobHasArtifacts}
                    />
                </g>
            {/if}
        {/if}
    {/each}
</g>

<style>
    .dag-node { cursor: pointer; }
    .dag-node rect { transition: stroke .18s, fill .18s; }
    /* Lift on hover instead of brightening the whole card: the rail and
       icon tile carry status colour, and brightness(1.2) washed both of
       them out on exactly the nodes a user was pointing at. */
    .dag-node:hover > rect:first-of-type { stroke: #4a5170; }
    .group-node:hover > rect:first-of-type { stroke: #3a4059; }

    .node-icon {
        display: flex; align-items: center; justify-content: center;
        width: 100%; height: 100%; pointer-events: none;
    }
    .artifact-icon { display: flex; align-items: center; justify-content: center; opacity: 0.8; }
    .node-actions-top {
        display: flex; justify-content: flex-end; align-items: center;
        gap: 6px; height: 100%; padding-right: 4px;
    }
    .node-rerun-btn {
        background: none; border: none; padding: 3px; cursor: pointer;
        color: var(--muted); display: flex; align-items: center;
        justify-content: center; border-radius: 6px; transition: all 0.2s;
    }
    .node-rerun-btn:hover { background: rgba(255,255,255,0.08); color: var(--accent); }
    .node-rerun-btn.approve:hover { color: #fbbf24; }
    .node-debug-link {
        font-size: 10px; font-weight: 700; cursor: pointer;
        text-transform: uppercase; letter-spacing: 0.6px; transition: all 0.2s;
    }
    .node-debug-link:hover { fill: #a78bfa; }
    /* Circular, like the expand affordance on the reference cards, and
       given a visible border so it reads as a control rather than a glyph
       that happens to sit in the corner. */
    .node-expand-btn {
        background: rgba(255,255,255,0.04);
        border: 1px solid var(--border);
        padding: 0; cursor: pointer; width: 22px; height: 22px;
        color: var(--muted); display: flex; align-items: center;
        justify-content: center; border-radius: 50%; transition: all 0.2s;
    }
    .node-expand-btn:hover {
        background: rgba(110,109,240,0.16);
        border-color: var(--accent); color: var(--accent);
    }
    .dag-label {
        font-family: 'Inter', system-ui, sans-serif; font-size: 14px;
        font-weight: 650; letter-spacing: -0.01em; pointer-events: none;
    }
    /* The subtitle is the card's data line -- status, duration, policy --
       so it is set in the app's mono face. Tabular figures stop durations
       from jittering as they tick up during a running step. */
    .dag-sub {
        font-family: var(--font-mono); font-size: 10.5px;
        font-weight: 500; letter-spacing: 0.02em;
        font-variant-numeric: tabular-nums; pointer-events: none;
        opacity: 0.85;
    }
    .dag-sub.policy { font-size: 10px; }
    .dag-sub.group-summary { font-size: 10.5px; font-weight: 600; }
</style>
