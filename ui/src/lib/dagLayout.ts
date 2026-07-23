// Layout math for the pipeline DAG view, factored out of DAG.svelte so it
// can be called recursively: an expanded `type: pipeline` step embeds its
// child run's own sub-DAG inline, and that sub-DAG needs the exact same
// column/row layout algorithm applied to a different (nested) job list.
//
// This module is deliberately pure (no Svelte, no stores) — it's called
// once per render from DAG.svelte with plain data, including data for
// child runs that have been fetched and cached by the caller. Keeping it
// pure means the recursive "measure nested content, then lay out the
// column that contains it" pass below is an ordinary function call, not a
// circular component-mount dependency.

import type { Job, RunDetail, ShardAssignmentDetail } from './api';

export const STATUS_COLORS: Record<string, { fill: string; stroke: string; text: string; sub: string }> = {
    passed:   { fill: '#062016', stroke: '#10b981', text: '#3ecf8e', sub: '#065f46' },
    failed:   { fill: '#2d0a0a', stroke: '#ef4444', text: '#f87171', sub: '#7f1d1d' },
    timed_out: { fill: '#2d0a0a', stroke: '#ef4444', text: '#f87171', sub: '#7f1d1d' },
    approval: { fill: '#2d1a0a', stroke: '#f59e0b', text: '#fbbf24', sub: '#78350f' },
    running:  { fill: '#0a192f', stroke: '#3b82f6', text: '#60a5fa', sub: '#1e3a8a' },
    waiting:  { fill: '#0a192f', stroke: '#3b82f6', text: '#60a5fa', sub: '#71717a' },
    queued:   { fill: '#161b22', stroke: '#6b7280', text: '#9ca3af', sub: '#374151' },
    pending:  { fill: '#0d1117', stroke: '#30363d', text: '#484f58', sub: '#21262d' },
    canceled: { fill: '#0d1117', stroke: '#30363d', text: '#484f58', sub: '#21262d' },
    release:  { fill: '#0a192f', stroke: '#3b82f6', text: '#60a5fa', sub: '#1e3a8a' },
};

export const MIN_NODE_W = 160;
export const NODE_H = 52;
export const COL_GAP = 90;
export const ROW_GAP = 24;
export const PAD = 32;
// A group is drawn as a labeled frame around its nested sub-DAG:
// GROUP_HEADER_H is the label bar height, GROUP_PAD the inner margin
// between the frame and the nested content on every side.
export const GROUP_HEADER_H = 30;
export const GROUP_PAD = 14;

export interface Position {
    x: number;
    y: number;
    w: number;
    h: number;
    // Present when this position is an expanded pipeline group: the child
    // run's jobs/shards and its own fully-computed layout, ready to render
    // recursively without recomputing anything.
    nested?: {
        runDetail: RunDetail;
        layout: LayoutResult;
    };
}

export interface LayoutResult {
    positions: Record<string, Position>;
    // The exact (filtered, fan-in-redirected, sorted) job list computeLayout
    // used internally. The renderer iterates this — not the raw input —
    // so edges always match what was actually laid out.
    resolvedJobs: Job[];
    svgW: number;
    svgH: number;
}

/** Compact status tally for a group header badge, e.g. "4/5 passed". */
export function summarizeStatuses(jobs: Job[]): { label: string; allTerminal: boolean } {
    if (jobs.length === 0) return { label: 'empty', allTerminal: true };
    const total = jobs.length;
    let passed = 0, failed = 0, active = 0;
    for (const j of jobs) {
        if (j.status === 'passed') passed++;
        else if (j.status === 'failed' || j.status === 'timed_out') failed++;
        else if (j.status === 'running' || j.status === 'waiting' || j.status === 'queued' || j.status === 'pending' || j.status === 'approval') active++;
    }
    const allTerminal = active === 0;
    if (failed > 0) return { label: `${failed}/${total} failed`, allTerminal };
    if (allTerminal) return { label: `${passed}/${total} passed`, allTerminal };
    return { label: `${passed}/${total} passed · ${active} running`, allTerminal };
}

function estimateNodeWidth(j: Job, shardAssignments: Record<string, ShardAssignmentDetail[]> | undefined) {
    let label = j.step_id;
    if (shardAssignments?.[j.step_id]) {
        label += ` ×${shardAssignments[j.step_id].length}`;
    }
    const labelLen = label.length;
    const subLen = (j.status.length + (j.duration_ms ? 10 : 0));
    const policyLen = j.policy_source ? j.policy_source.length + 2 : 0;

    const labelW = labelLen * 8.5;
    const subW = Math.max(subLen * 6, policyLen * 6);

    const contentW = Math.max(labelW, subW);
    const actionsW = 65; // Artifact + Rerun + Debug link + margins

    return Math.max(MIN_NODE_W, 14 + contentW + actionsW);
}

/**
 * Computes node positions and edges for one DAG level.
 *
 * @param jobs           Jobs at this level (a run's own jobs, or a child
 *                        run's jobs when called recursively).
 * @param shardAssignments Shard plan for this level's run, if any.
 * @param expandedPipelineJobs job_ids whose child pipeline is expanded
 *                        (keyed by job_id, globally unique, so the same
 *                        step name in two different runs never collides).
 * @param expandedShardSteps step_ids whose shard fan-out is expanded
 *                        (keyed by step_id — scoped correctly since this
 *                        function only ever looks within its own `jobs`).
 * @param childRunCache  child_run_id -> already-fetched RunDetail. A
 *                        pipeline step whose child_run_id isn't in the
 *                        cache yet renders collapsed (nothing to nest).
 */
export function computeLayout(
    jobs: Job[],
    shardAssignments: Record<string, ShardAssignmentDetail[]> | undefined,
    expandedPipelineJobs: Set<string>,
    expandedShardSteps: Set<string>,
    childRunCache: Record<string, RunDetail>,
): LayoutResult {
    if (!jobs || jobs.length === 0) {
        return { positions: {}, resolvedJobs: [], svgW: 0, svgH: 0 };
    }

    // Filter out shard jobs if their parent step is not expanded.
    const filteredJobs = jobs.filter((j) => {
        if (j.step_id.includes('-shard-')) {
            const base = j.step_id.split('-shard-')[0];
            return expandedShardSteps.has(base);
        }
        return true;
    });

    // Redirect dependencies for fan-in steps if shards are collapsed.
    const workingJobs = filteredJobs
        .map((j) => {
            if (shardAssignments?.[j.step_id] && !expandedShardSteps.has(j.step_id)) {
                const shard1 = jobs.find((sj) => sj.step_id === j.step_id + '-shard-1');
                if (shard1) {
                    return { ...j, depends_on: shard1.depends_on };
                }
            }
            return j;
        })
        .sort((a, b) => a.step_id.localeCompare(b.step_id));

    try {
        // Pass 0: for every expanded pipeline group with cached child data,
        // recursively lay out its child run FIRST, so we know how much
        // space its row needs before doing our own column layout.
        const nestedByJobID: Record<string, { runDetail: RunDetail; layout: LayoutResult }> = {};
        for (const j of workingJobs) {
            if (j.child_run_id && expandedPipelineJobs.has(j.job_id)) {
                const childDetail = childRunCache[j.child_run_id];
                if (childDetail) {
                    const childLayout = computeLayout(
                        childDetail.jobs,
                        childDetail.shard_assignments,
                        expandedPipelineJobs,
                        expandedShardSteps,
                        childRunCache,
                    );
                    nestedByJobID[j.job_id] = { runDetail: childDetail, layout: childLayout };
                }
            }
        }

        const heightFor = (j: Job) => {
            const n = nestedByJobID[j.job_id];
            if (!n) return NODE_H;
            return Math.max(NODE_H, GROUP_HEADER_H + GROUP_PAD * 2 + n.layout.svgH);
        };
        const widthFor = (j: Job) => {
            const n = nestedByJobID[j.job_id];
            const base = estimateNodeWidth(j, shardAssignments);
            if (!n) return base;
            // The group header packs the step label on the left and a
            // status-tally badge ("2/5 passed · 1 running") on the right.
            // estimateNodeWidth() only sizes for the label — it knows
            // nothing about the badge — so a header sized from it alone let
            // the badge overflow past the frame's right edge whenever the
            // label was short. Size the header for both, side by side.
            const summary = summarizeStatuses(n.runDetail.jobs);
            const headerW = 14 /* left pad */ + j.step_id.length * 8.5 /* label, 13px bold */
                + 28 /* gap between label and badge */
                + summary.label.length * 6.5 /* badge, 10px bold */
                + 14 /* right pad */;
            return Math.max(base, headerW, GROUP_PAD * 2 + n.layout.svgW);
        };

        const depth: Record<string, number> = {};
        const getDepth = (stepID: string, visited: string[] = []): number => {
            if (stepID in depth) return depth[stepID];
            if (visited.includes(stepID)) return 0; // Cycle detected

            const job = workingJobs.find((j) => j.step_id === stepID);
            if (!job || !job.depends_on || job.depends_on.length === 0) return (depth[stepID] = 0);

            const nextVisited = [...visited, stepID];
            const deps = job.depends_on.filter((d) => workingJobs.some((j) => j.step_id === d));
            if (deps.length === 0) return (depth[stepID] = 0);

            return (depth[stepID] = 1 + Math.max(...deps.map((d) => getDepth(d, nextVisited))));
        };
        workingJobs.forEach((j) => getDepth(j.step_id));

        const cols: Record<number, Job[]> = {};
        workingJobs.forEach((j) => {
            const d = depth[j.step_id] ?? 0;
            (cols[d] = cols[d] || []).push(j);
        });

        const positions: Record<string, Position> = {};
        const colKeys = Object.keys(cols).map(Number).sort((a, b) => a - b);
        let x = PAD;
        colKeys.forEach((col) => {
            const colJobs = cols[col];

            let maxColW = MIN_NODE_W;
            colJobs.forEach((j) => {
                const w = widthFor(j);
                if (w > maxColW) maxColW = w;
            });

            let y = PAD;
            colJobs.forEach((j) => {
                const h = heightFor(j);
                const pos: Position = { x, y, w: maxColW, h };
                if (nestedByJobID[j.job_id]) {
                    pos.nested = nestedByJobID[j.job_id];
                }
                positions[j.job_id] = pos;
                y += h + ROW_GAP;
            });
            x += maxColW + COL_GAP;
        });

        const svgW = Math.max(0, x - COL_GAP + PAD);
        const posValues = Object.values(positions);
        const maxH = posValues.length > 0 ? Math.max(...posValues.map((p) => p.y + p.h)) + PAD : PAD;
        return { positions, resolvedJobs: workingJobs, svgW, svgH: maxH };
    } catch (e) {
        console.error('Layout computation failed:', e);
        return { positions: {}, resolvedJobs: [], svgW: 0, svgH: 0 };
    }
}
