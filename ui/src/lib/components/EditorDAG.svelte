<script lang="ts">
    import { Layers, Cpu, Play, Package, Globe } from '@lucide/svelte';

    interface Step {
        id: string;
        type: 'command' | 'generator' | 'pipeline' | 'release' | 'approval';
        depends_on: string[];
        image?: string;
        pipeline_ref?: { name: string };
        // Optional: present when the full editor's richer Step type is
        // passed in. Used only to show a small "N shards" indicator.
        split?: { enabled: boolean; shards: number };
    }

    export let steps: Step[] = [];
    export let onSelectStep: (id: string) => void = () => {};

    const NODE_W = 180, NODE_H = 60, COL_GAP = 40, ROW_GAP = 80, PAD = 40;

    function truncate(text: string, maxChars: number): string {
        if (text.length <= maxChars) return text;
        return text.slice(0, Math.max(1, maxChars - 1)) + '…';
    }

    const LABEL_MAX_CHARS = Math.max(4, Math.floor((NODE_W - 45 - 10) / 8.5));
    const SUB_MAX_CHARS = Math.max(4, Math.floor((NODE_W - 45 - 10) / 6));

    const TYPE_COLORS: Record<string, any> = {
        command:   { fill: '#0d1117', stroke: '#3fb950', text: '#3fb950', icon: Cpu },
        generator: { fill: '#0d1117', stroke: '#d29922', text: '#d29922', icon: Layers },
        pipeline:  { fill: '#0d1117', stroke: '#58a6ff', text: '#58a6ff', icon: Play },
        release:   { fill: '#0d1117', stroke: '#a78bfa', text: '#a78bfa', icon: Package },
        approval:  { fill: '#0d1117', stroke: '#f59e0b', text: '#f59e0b', icon: Globe },
    };

    function computeLayout(stepsInput: Step[]) {
        if (!stepsInput || stepsInput.length === 0) {
            return { positions: {}, svgW: 0, svgH: 0 };
        }

        const stepsMap = new Map(stepsInput.map(s => [s.id, s]));
        const depth: Record<string, number> = {};

        const getDepth = (id: string, visited: string[] = []): number => {
            if (id in depth) return depth[id];
            if (visited.includes(id)) return 0; // Cycle detected
            
            const step = stepsMap.get(id);
            if (!step || !step.depends_on || step.depends_on.length === 0) return (depth[id] = 0);
            
            const nextVisited = [...visited, id];
            const deps = step.depends_on.filter(d => stepsMap.has(d));
            if (deps.length === 0) return (depth[id] = 0);
            
            return (depth[id] = 1 + Math.max(...deps.map(d => getDepth(d, nextVisited))));
        };

        stepsInput.forEach(s => getDepth(s.id));

        const rows: Record<number, Step[]> = {};
        stepsInput.forEach(s => {
            const d = depth[s.id] ?? 0;
            (rows[d] = rows[d] || []).push(s);
        });

        const positions: Record<string, { x: number, y: number }> = {};
        const rowKeys = Object.keys(rows).map(Number).sort((a, b) => a - b);
        
        let maxRowW = 0;
        rowKeys.forEach(row => {
            const w = rows[row].length * NODE_W + (rows[row].length - 1) * COL_GAP;
            if (w > maxRowW) maxRowW = w;
        });

        let currentY = PAD;
        rowKeys.forEach(row => {
            const rowSteps = rows[row];
            const rowW = rowSteps.length * NODE_W + (rowSteps.length - 1) * COL_GAP;
            let currentX = PAD + (maxRowW - rowW) / 2;
            
            rowSteps.forEach(s => {
                positions[s.id] = { x: currentX, y: currentY };
                currentX += NODE_W + COL_GAP;
            });
            currentY += NODE_H + ROW_GAP;
        });

        const svgW = maxRowW + PAD * 2;
        const svgH = currentY - ROW_GAP + PAD;

        return { positions, svgW, svgH };
    }

    $: layout = computeLayout(steps);
</script>

<div class="editor-dag">
    <svg 
        width={layout.svgW} 
        height={layout.svgH} 
        viewBox="0 0 {layout.svgW} {layout.svgH}"
    >
        <defs>
            <pattern id="grid-editor" width="40" height="40" patternUnits="userSpaceOnUse">
                <path d="M 40 0 L 0 0 0 40" fill="none" stroke="rgba(255,255,255,0.03)" stroke-width="1"/>
            </pattern>
            <marker 
                id="arrowhead" 
                viewBox="0 0 10 10" 
                refX="8" 
                refY="5"
                markerWidth="6" 
                markerHeight="6" 
                orient="auto-start-reverse"
            >
                <path d="M 0 0 L 10 5 L 0 10 z" fill="#30363d" />
            </marker>
            {#each steps as s}
                {#if layout.positions[s.id]}
                    {@const pos = layout.positions[s.id]}
                    <clipPath id="node-clip-{s.id}">
                        <rect x={pos.x} y={pos.y} width={NODE_W} height={NODE_H} rx="8" />
                    </clipPath>
                {/if}
            {/each}
        </defs>

        <rect width="100%" height="100%" fill="url(#grid-editor)" />

        <!-- Edges -->
        <g class="edges">
            {#each steps as s}
                {#if layout.positions[s.id]}
                    {#each s.depends_on || [] as depId}
                        {#if layout.positions[depId]}
                            {@const from = layout.positions[depId]}
                            {@const to = layout.positions[s.id]}
                            {@const x1 = from.x + NODE_W / 2}
                            {@const y1 = from.y + NODE_H}
                            {@const x2 = to.x + NODE_W / 2}
                            {@const y2 = to.y}
                            {@const cy = (y1 + y2) / 2}
                            <path 
                                d="M{x1},{y1} C{x1},{cy} {x2},{cy} {x2},{y2}"
                                fill="none" 
                                stroke="#30363d" 
                                stroke-width="2"
                                marker-end="url(#arrowhead)"
                            />
                        {/if}
                    {/each}
                {/if}
            {/each}
        </g>

        <!-- Nodes -->
        <g class="nodes">
            {#each steps as s}
                {#if layout.positions[s.id]}
                    {@const pos = layout.positions[s.id]}
                    {@const color = TYPE_COLORS[s.type] || TYPE_COLORS.command}
                    {@const subtitle = s.type === 'pipeline'
                        ? `Trigger: ${s.pipeline_ref?.name || '...'}`
                        : s.type === 'release'
                            ? 'SCM Release'
                            : (s.image || 'no image') + (s.split?.enabled ? ` · ${s.split.shards} shards` : '')}
                    <g class="node" on:click={() => onSelectStep(s.id)} clip-path="url(#node-clip-{s.id})">
                        <rect 
                            x={pos.x} y={pos.y} 
                            width={NODE_W} height={NODE_H} 
                            rx="8"
                            fill={color.fill} 
                            stroke={color.stroke}
                            stroke-width="1.5"
                        />
                        
                        <text 
                            x={pos.x + 45} y={pos.y + 25}
                            fill="#e6edf3"
                            class="node-id"
                        >
                            {truncate(s.id, LABEL_MAX_CHARS)}
                            <title>{s.id}</title>
                        </text>

                        <text 
                            x={pos.x + 45} y={pos.y + 42}
                            fill="#8b949e"
                            class="node-type"
                        >
                            {truncate(subtitle, SUB_MAX_CHARS)}
                            <title>{subtitle}</title>
                        </text>

                        <g transform="translate({pos.x + 12}, {pos.y + 18})">
                            <svelte:component this={color.icon} size={20} color={color.stroke} />
                        </g>
                    </g>
                {/if}
            {/each}
        </g>
    </svg>
</div>

<style>
    .editor-dag {
        flex: 1;
        overflow: auto;
        background: #0d1117;
        position: relative;
    }
    .node {
        cursor: pointer;
        transition: transform 0.2s;
    }
    .node:hover {
        filter: brightness(1.2);
    }
    .node-id {
        font-size: 14px;
        font-weight: 600;
    }
    .node-type {
        font-size: 11px;
    }
    svg {
        display: block;
    }
</style>
