<script lang="ts">
    import { activeRun, selectedJob } from '../stores';
    import { api, type Job } from '../api';
    import { RotateCcw, XCircle } from '@lucide/svelte';

    const NODE_W = 160, NODE_H = 52, COL_GAP = 90, ROW_GAP = 24, PAD = 32;
    const STATUS_COLORS: Record<string, any> = {
        passed:   { fill: '#0d2e20', stroke: '#3ecf8e', text: '#3ecf8e', sub: '#2a8c60' },
        failed:   { fill: '#2e1414', stroke: '#f87171', text: '#f87171', sub: '#a84a4a' },
        running:  { fill: '#1a2040', stroke: '#60a5fa', text: '#60a5fa', sub: '#3a6a9a' },
        queued:   { fill: '#1e2035', stroke: '#6b7094', text: '#9da3cc', sub: '#4b5280' },
        pending:  { fill: '#1a1d27', stroke: '#3a3f5c', text: '#6b7094', sub: '#3a3f5c' },
        canceled: { fill: '#1a1d27', stroke: '#3a3f5c', text: '#4b5280', sub: '#3a3f5c' },
    };

    function computeLayout(jobs: Job[]) {
        if (!jobs || jobs.length === 0) {
            return { positions: {}, svgW: 0, svgH: 0 };
        }

        try {
            const depth: Record<string, number> = {};
            const getDepth = (stepID: string, visited: string[] = []): number => {
                if (stepID in depth) return depth[stepID];
                if (visited.includes(stepID)) return 0; // Cycle detected
                
                const job = jobs.find(j => j.step_id === stepID);
                if (!job || !job.depends_on || job.depends_on.length === 0) return (depth[stepID] = 0);
                
                const nextVisited = [...visited, stepID];
                const deps = job.depends_on.filter(d => jobs.some(j => j.step_id === d));
                if (deps.length === 0) return (depth[stepID] = 0);
                
                return (depth[stepID] = 1 + Math.max(...deps.map(d => getDepth(d, nextVisited))));
            };
            jobs.forEach(j => getDepth(j.step_id));

            const cols: Record<number, Job[]> = {};
            jobs.forEach(j => {
                const d = depth[j.step_id] ?? 0;
                (cols[d] = cols[d] || []).push(j);
            });

            const positions: Record<string, { x: number, y: number }> = {};
            const colKeys = Object.keys(cols).map(Number).sort((a, b) => a - b);
            let x = PAD;
            colKeys.forEach(col => {
                const colJobs = cols[col];
                let y = PAD;
                colJobs.forEach(j => {
                    positions[j.job_id] = { x, y };
                    y += NODE_H + ROW_GAP;
                });
                x += NODE_W + COL_GAP;
            });

            const svgW = Math.max(0, x - COL_GAP + PAD);
            const posValues = Object.values(positions);
            const maxH = posValues.length > 0 ? Math.max(...posValues.map(p => p.y + NODE_H)) + PAD : PAD;
            return { positions, svgW, svgH: maxH };
        } catch (e) {
            console.error("Layout computation failed:", e);
            return { positions: {}, svgW: 0, svgH: 0 };
        }
    }

    function fmtDuration(ms: number) {
        if (!ms) return '';
        if (ms < 1000) return `${ms}ms`;
        return `${(ms/1000).toFixed(1)}s`;
    }

    function statusBadge(status: string) {
        const labels: Record<string, string> = { 
            passed:'passed', failed:'failed', running:'running…',
            queued:'queued', pending:'pending', canceled:'canceled' 
        };
        return labels[status] || status;
    }

    $: layout = ($activeRun && $activeRun.jobs) ? computeLayout($activeRun.jobs) : null;
    $: byStep = ($activeRun && $activeRun.jobs) ? Object.fromEntries($activeRun.jobs.map(j => [j.step_id, j])) : {};

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

    async function rerunJob(job: Job) {
        if (confirm(`Rerun job ${job.step_id} and its downstream?`)) {
            await api.rerunJob(job.job_id);
        }
    }

    export let onOpenDebug: (job: Job) => void;
</script>

<div id="dag-panel">
    <div id="dag-header">
        <h2>
            Pipeline DAG — 
            {#if $activeRun}
                {$activeRun.name}
                {#each $activeRun.applied_policies || [] as policy}
                    <span class="policy-badge">🛡 {policy}</span>
                {/each}
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
    <div id="dag-scroll">
        {#if !$activeRun}
            <div id="dag-empty">Select a run from the sidebar to view its pipeline graph.</div>
        {:else if layout}
            <svg 
                width={layout.svgW} 
                height={layout.svgH} 
                viewBox="0 0 {layout.svgW} {layout.svgH}"
            >
                <defs>
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

                <g class="edges">
                    {#each $activeRun.jobs as j}
                        {#if layout.positions[j.job_id]}
                            {#each j.depends_on || [] as depStep}
                                {#if byStep[depStep] && layout.positions[byStep[depStep].job_id]}
                                    {@const from = layout.positions[byStep[depStep].job_id]}
                                    {@const to = layout.positions[j.job_id]}
                                    {@const x1 = from.x + NODE_W}
                                    {@const y1 = from.y + NODE_H / 2}
                                    {@const x2 = to.x}
                                    {@const y2 = to.y + NODE_H / 2}
                                    {@const cx = (x1 + x2) / 2}
                                    {@const c = STATUS_COLORS[byStep[depStep].status] || STATUS_COLORS.pending}
                                    <path 
                                        d="M{x1},{y1} C{cx},{y1} {cx},{y2} {x2},{y2}"
                                        fill="none" 
                                        stroke={c.stroke} 
                                        stroke-width="1.5"
                                        stroke-opacity="0.5"
                                        marker-end="url(#arrow-{byStep[depStep].status})"
                                    />
                                {/if}
                            {/each}
                        {/if}
                    {/each}
                </g>

                <g class="nodes">
                    {#each $activeRun.jobs as j}
                        {#if layout.positions[j.job_id]}
                            {@const pos = layout.positions[j.job_id]}
                            {@const c = STATUS_COLORS[j.status] || STATUS_COLORS.pending}
                            {@const isSelected = j.job_id === $selectedJob?.job_id}
                            
                            
                            <g 
                                class="dag-node" 
                                class:selected={isSelected}
                                on:click={() => selectedJob.set(j)}
                            >
                                <rect 
                                    x={pos.x} y={pos.y} 
                                    width={NODE_W} height={NODE_H} 
                                    rx="8"
                                    fill={c.fill} stroke={c.stroke}
                                    stroke-width={isSelected ? '2.5' : '1'}
                                />
                                <text 
                                    class="dag-label" 
                                    x={pos.x + 12} 
                                    y={j.policy_source ? pos.y + 14 : pos.y + 20}
                                    fill={c.text}
                                >
                                    {j.step_id}
                                </text>
                                {#if j.policy_source}
                                    <text 
                                        class="dag-sub policy" 
                                        x={pos.x + 12} y={pos.y + 26}
                                        fill="#a78bfa"
                                    >
                                        🛡 {j.policy_source}
                                    </text>
                                {/if}
                                <text 
                                    class="dag-sub" 
                                    x={pos.x + 12}
                                    y={j.policy_source ? pos.y + 38 : pos.y + 36}
                                    fill={c.sub}
                                >
                                    {statusBadge(j.status)}{#if j.duration_ms} · {fmtDuration(j.duration_ms)}{/if}
                                </text>
                            </g>
                        {/if}
                    {/each}
                </g>

                <g class="debug-btns">
                    {#each $activeRun.jobs as j}
                        {#if layout.positions[j.job_id]}
                            {@const pos = layout.positions[j.job_id]}
                            {#if j.status === 'failed'}
                                <text 
                                    x={pos.x + NODE_W - 8} y={pos.y + NODE_H - 10}
                                    text-anchor="end" fill="#818cf8"
                                    font-size="11px"
                                    style="cursor:pointer"
                                    on:click|stopPropagation={() => onOpenDebug(j)}
                                >
                                    Debug →
                                </text>
                            {/if}
                            {#if j.status === 'passed' || j.status === 'failed' || j.status === 'canceled'}
                                <text 
                                    x={pos.x + NODE_W - 8} y={pos.y + 14}
                                    text-anchor="end" fill="#6b7094"
                                    font-size="12px"
                                    style="cursor:pointer"
                                    on:click|stopPropagation={() => rerunJob(j)}
                                >
                                    ⟳
                                </text>
                            {/if}
                        {/if}
                    {/each}
                </g>
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

    .dag-node { cursor: pointer; }
    .dag-node rect { transition: filter .15s; }
    .dag-node:hover rect { filter: brightness(1.25); }
    .dag-label { font-family: system-ui, sans-serif; font-size: 13px; font-weight: 600;
        pointer-events: none; }
    .dag-sub { font-family: system-ui, sans-serif; font-size: 11px; pointer-events: none; }
    .dag-sub.policy { font-size: 9px; }
</style>
