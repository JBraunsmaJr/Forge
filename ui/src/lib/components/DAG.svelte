<script lang="ts">
    import { activeRun, selectedJob, artifacts } from '../stores';
    import { api, type Job } from '../api';
    import { RotateCcw, XCircle, Package } from '@lucide/svelte';

    const MIN_NODE_W = 160, NODE_H = 52, COL_GAP = 90, ROW_GAP = 24, PAD = 32;
    const STATUS_COLORS: Record<string, any> = {
        passed:   { fill: '#062016', stroke: '#10b981', text: '#3ecf8e', sub: '#065f46' },
        failed:   { fill: '#2d0a0a', stroke: '#ef4444', text: '#f87171', sub: '#7f1d1d' },
        running:  { fill: '#0a192f', stroke: '#3b82f6', text: '#60a5fa', sub: '#1e3a8a' },
        queued:   { fill: '#161b22', stroke: '#6b7280', text: '#9ca3af', sub: '#374151' },
        pending:  { fill: '#0d1117', stroke: '#30363d', text: '#484f58', sub: '#21262d' },
        canceled: { fill: '#0d1117', stroke: '#30363d', text: '#484f58', sub: '#21262d' },
    };

    function estimateNodeWidth(j: Job) {
        const labelLen = j.step_id.length;
        const subLen = (statusBadge(j.status) + (j.duration_ms ? ` · ${fmtDuration(j.duration_ms)}` : '')).length;
        const policyLen = j.policy_source ? j.policy_source.length + 2 : 0;
        
        // Estimated pixel widths (Inter font)
        // Label: 13px bold ~ 8.5px/char
        // Sub: 10px ~ 6px/char
        const labelW = labelLen * 8.5;
        const subW = Math.max(subLen * 6, policyLen * 6);
        
        const contentW = Math.max(labelW, subW);
        const actionsW = 65; // Artifact + Rerun + Debug link + margins
        
        return Math.max(MIN_NODE_W, 14 + contentW + actionsW);
    }

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

            const positions: Record<string, { x: number, y: number, w: number }> = {};
            const colKeys = Object.keys(cols).map(Number).sort((a, b) => a - b);
            let x = PAD;
            colKeys.forEach(col => {
                const colJobs = cols[col];
                
                // Determine max width for this column
                let maxColW = MIN_NODE_W;
                colJobs.forEach(j => {
                    const w = estimateNodeWidth(j);
                    if (w > maxColW) maxColW = w;
                });

                let y = PAD;
                colJobs.forEach(j => {
                    positions[j.job_id] = { x, y, w: maxColW };
                    y += NODE_H + ROW_GAP;
                });
                x += maxColW + COL_GAP;
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

                <g class="edges">
                    {#each $activeRun.jobs as j}
                        {#if layout.positions[j.job_id]}
                            {#each j.depends_on || [] as depStep}
                                {#if byStep[depStep] && layout.positions[byStep[depStep].job_id]}
                                    {@const from = layout.positions[byStep[depStep].job_id]}
                                    {@const to = layout.positions[j.job_id]}
                                    {@const x1 = from.x + from.w}
                                    {@const y1 = from.y + NODE_H / 2}
                                    {@const x2 = to.x}
                                    {@const y2 = to.y + NODE_H / 2}
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
                    {#each $activeRun.jobs as j}
                        {#if layout.positions[j.job_id]}
                            {@const pos = layout.positions[j.job_id]}
                            {@const c = STATUS_COLORS[j.status] || STATUS_COLORS.pending}
                            {@const isSelected = j.job_id === $selectedJob?.job_id}
                            
                            
                            <g 
                                class="dag-node" 
                                class:selected={isSelected}
                                on:click|stopPropagation={() => selectedJob.set(j)}
                                filter={isSelected ? "url(#selected-shadow)" : "url(#shadow)"}
                            >
                                <rect 
                                    x={pos.x} y={pos.y} 
                                    width={pos.w} height={NODE_H} 
                                    rx="10"
                                    fill={isSelected ? '#1e293b' : c.fill} 
                                    stroke={isSelected ? '#818cf8' : c.stroke}
                                    stroke-width={isSelected ? '3' : '1.5'}
                                />
                                <text 
                                    class="dag-label" 
                                    x={pos.x + 14} 
                                    y={j.policy_source ? pos.y + 16 : pos.y + 22}
                                    fill={isSelected ? '#e2e8f0' : c.text}
                                >
                                    {j.step_id}
                                </text>
                                {#if j.policy_source}
                                    <text 
                                        class="dag-sub policy" 
                                        x={pos.x + 14} y={pos.y + 28}
                                        fill="#a78bfa"
                                    >
                                        🛡 {j.policy_source}
                                    </text>
                                {/if}
                                <text 
                                    class="dag-sub" 
                                    x={pos.x + 14}
                                    y={j.policy_source ? pos.y + 40 : pos.y + 38}
                                    fill={isSelected ? '#94a3b8' : c.sub}
                                >
                                    {statusBadge(j.status)}{#if j.duration_ms} · {fmtDuration(j.duration_ms)}{/if}
                                </text>

                                <foreignObject x={pos.x + pos.w - 58} y={pos.y + 4} width="52" height="24">
                                    <div class="node-actions-top">
                                        {#if jobHasArtifacts(j.job_id)}
                                            <div class="artifact-icon" title="Produces Artifacts">
                                                <Package size={14} color={isSelected ? '#818cf8' : c.stroke} />
                                            </div>
                                        {/if}
                                        {#if j.status === 'passed' || j.status === 'failed' || j.status === 'canceled'}
                                            <button 
                                                class="node-rerun-btn" 
                                                title="Rerun Job"
                                                on:click|stopPropagation={() => rerunJob(j)}
                                            >
                                                <RotateCcw size={14} />
                                            </button>
                                        {/if}
                                    </div>
                                </foreignObject>

                                {#if j.status === 'failed'}
                                    <text 
                                        x={pos.x + pos.w - 10} y={pos.y + NODE_H - 10}
                                        text-anchor="end" fill="#818cf8"
                                        class="node-debug-link"
                                        on:click|stopPropagation={() => onOpenDebug(j)}
                                    >
                                        Debug →
                                    </text>
                                {/if}
                            </g>
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
    .node-debug-link {
        font-size: 10px;
        font-weight: 700;
        cursor: pointer;
        text-transform: uppercase;
        letter-spacing: 0.5px;
        transition: all 0.2s;
    }
    .node-debug-link:hover { fill: #a78bfa; }
    .dag-label { font-family: 'Inter', system-ui, sans-serif; font-size: 13px; font-weight: 700;
        pointer-events: none; }
    .dag-sub { font-family: 'Inter', system-ui, sans-serif; font-size: 10px; font-weight: 500; pointer-events: none; }
    .dag-sub.policy { font-size: 9px; }
</style>
