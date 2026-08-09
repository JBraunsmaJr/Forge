<script lang="ts">
    import { selectedJob, navigateToRunID } from '../stores';
    import { api, type Job, type ShardAssignmentDetail } from '../api';
    import {
        GROUP_HEADER_H, GROUP_PAD, STATUS_COLORS,
        type LayoutResult, summarizeStatuses,
    } from '../dagLayout';
    import { RotateCcw, CheckCircle, Package, ChevronRight, ChevronDown, ExternalLink } from '@lucide/svelte';

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
            canceled: 'canceled', timed_out: 'timed out',
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
                    {@const x1 = from.x + from.w}
                    {@const y1 = from.y + from.h / 2}
                    {@const x2 = to.x}
                    {@const y2 = to.y + to.h / 2}
                    {@const cx = (x1 + x2) / 2}
                    {@const c = STATUS_COLORS[byStep[depStep].status] || STATUS_COLORS.pending}
                    <path
                        d="M{x1},{y1} C{cx},{y1} {cx},{y2} {x2},{y2}"
                        fill="none"
                        stroke={c.stroke}
                        stroke-width="2"
                        stroke-opacity="0.3"
                        marker-end="url(#arrow-{byStep[depStep].status})"
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

            <g
                class="dag-node"
                class:selected={isSelected}
                class:group-node={isGroup}
                on:click|stopPropagation={() => selectedJob.set(j)}
                filter={isSelected ? 'url(#selected-shadow)' : 'url(#shadow)'}
            >
                <rect
                    x={pos.x} y={pos.y}
                    width={pos.w} height={isGroup ? GROUP_HEADER_H : pos.h}
                    rx="10"
                    fill={isSelected ? '#1e293b' : c.fill}
                    stroke={isSelected ? '#818cf8' : c.stroke}
                    stroke-width={isSelected ? '3' : '1.5'}
                />
                {#if isGroup}
                    <!-- Frame for the nested sub-DAG below the header bar -->
                    <rect
                        x={pos.x} y={pos.y + GROUP_HEADER_H}
                        width={pos.w} height={pos.h - GROUP_HEADER_H}
                        rx="10"
                        fill="rgba(255,255,255,0.015)"
                        stroke={c.stroke}
                        stroke-width="1"
                        stroke-dasharray="4 3"
                        stroke-opacity="0.5"
                    />
                {/if}
                <text
                    class="dag-label"
                    x={pos.x + 14}
                    y={j.policy_source && !isGroup ? pos.y + 16 : pos.y + 22}
                    fill={isSelected ? '#e2e8f0' : c.text}
                >
                    {j.step_id}{shardAssignments?.[j.step_id] ? ` ×${shardAssignments[j.step_id].length}` : ''}
                </text>
                {#if !isGroup && j.policy_source}
                    <text class="dag-sub policy" x={pos.x + 14} y={pos.y + 28} fill="#a78bfa">
                        🛡 {j.policy_source}
                    </text>
                {/if}

                {#if isGroup}
                    {@const summary = summarizeStatuses(pos.nested.runDetail.jobs)}
                    <text class="dag-sub group-summary" x={pos.x + pos.w - 14} y={pos.y + 20}
                          text-anchor="end" fill={isSelected ? '#94a3b8' : c.sub}>
                        {summary.label}
                    </text>
                {:else}
                    <text
                        class="dag-sub"
                        x={pos.x + 14}
                        y={j.policy_source ? pos.y + 40 : pos.y + 38}
                        fill={isSelected ? '#94a3b8' : c.sub}
                    >
                        {statusBadge(j.status)}{#if j.duration_ms} · {fmtDuration(j.duration_ms)}{/if}{#if j.status === 'timed_out' && j.timeout_ns} · after {fmtTimeout(j.timeout_ns)}{/if}
                    </text>
                {/if}

                {#if !isGroup}
                    <foreignObject x={pos.x + pos.w - 58} y={pos.y + 4} width="52" height="24">
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
                        x={pos.x + pos.w - (j.step_id.includes('-shard-') ? 10 : 65)} y={pos.y + (isGroup ? GROUP_HEADER_H : pos.h) - 10}
                        text-anchor="end" fill="#818cf8"
                        class="node-debug-link"
                        on:click|stopPropagation={() => onOpenDebug(j)}
                    >
                        Debug →
                    </text>
                {/if}

                {#if shardAssignments?.[j.step_id]}
                    <foreignObject x={pos.x + pos.w - 24} y={pos.y + pos.h - 24} width="20" height="20">
                        <button class="node-expand-btn" on:click|stopPropagation={() => onToggleShard(j.step_id)}>
                            {#if isGroup}<ChevronDown size={14} />{:else}<ChevronRight size={14} />{/if}
                        </button>
                    </foreignObject>
                {/if}

                {#if j.child_run_id}
                    {#if depth < maxDepth}
                        <foreignObject x={pos.x + pos.w - 24} y={pos.y + 4} width="20" height="20">
                            <button class="node-expand-btn" title={isGroup ? 'Collapse child pipeline' : 'Expand child pipeline'}
                                    on:click|stopPropagation={() => onTogglePipeline(j)}>
                                {#if isGroup}<ChevronDown size={14} />{:else}<ChevronRight size={14} />{/if}
                            </button>
                        </foreignObject>
                    {:else}
                        <foreignObject x={pos.x + pos.w - 24} y={pos.y + 4} width="20" height="20">
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
    .dag-node rect { transition: all .2s; }
    .dag-node:hover rect { filter: brightness(1.2); }
    .group-node:hover rect { filter: none; } /* group frame shouldn't brighten on hover like a leaf node */

    .artifact-icon { display: flex; align-items: center; justify-content: center; opacity: 0.8; }
    .node-actions-top {
        display: flex; justify-content: flex-end; align-items: center;
        gap: 6px; height: 100%; padding-right: 4px;
    }
    .node-rerun-btn {
        background: none; border: none; padding: 3px; cursor: pointer;
        color: var(--muted); display: flex; align-items: center;
        justify-content: center; border-radius: 4px; transition: all 0.2s;
    }
    .node-rerun-btn:hover { background: rgba(255,255,255,0.1); color: var(--accent); }
    .node-rerun-btn.approve:hover { color: #fbbf24; }
    .node-debug-link {
        font-size: 11px; font-weight: 700; cursor: pointer;
        text-transform: uppercase; letter-spacing: 0.5px; transition: all 0.2s;
    }
    .node-debug-link:hover { fill: #a78bfa; }
    .node-expand-btn {
        background: none; border: none; padding: 2px; cursor: pointer;
        color: var(--muted); display: flex; align-items: center;
        justify-content: center; border-radius: 4px; transition: all 0.2s;
    }
    .node-expand-btn:hover { background: rgba(255,255,255,0.1); color: var(--accent); }
    .dag-label {
        font-family: 'Inter', system-ui, sans-serif; font-size: 14px;
        font-weight: 700; pointer-events: none;
    }
    .dag-sub {
        font-family: 'Inter', system-ui, sans-serif; font-size: 11px;
        font-weight: 500; pointer-events: none;
    }
    .dag-sub.policy { font-size: 10px; }
    .dag-sub.group-summary { font-size: 11px; font-weight: 700; }
</style>
