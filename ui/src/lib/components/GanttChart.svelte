<script lang="ts">
    import type { Job } from '../api';
    import { selectedJob } from '../stores';

    export let jobs: Job[] = [];

    $: sortedJobs = [...jobs].sort((a, b) => {
        const aTime = a.started_at ? new Date(a.started_at).getTime() : Infinity;
        const bTime = b.started_at ? new Date(b.started_at).getTime() : Infinity;
        return aTime - bTime;
    });

    $: timeBounds = (() => {
        let min = Infinity;
        let max = -Infinity;
        jobs.forEach(j => {
            if (j.started_at) {
                const start = new Date(j.started_at).getTime();
                if (start < min) min = start;
                
                const end = j.finished_at ? new Date(j.finished_at).getTime() : Date.now();
                if (end > max) max = end;
            }
        });
        if (min === Infinity) return { min: Date.now(), max: Date.now() + 1000, duration: 1000 };
        return { min, max, duration: max - min };
    })();

    function getLeft(job: Job) {
        if (!job.started_at) return 0;
        const start = new Date(job.started_at).getTime();
        return ((start - timeBounds.min) / timeBounds.duration) * 100;
    }

    function getWidth(job: Job) {
        if (!job.started_at) return 0;
        const start = new Date(job.started_at).getTime();
        const end = job.finished_at ? new Date(job.finished_at).getTime() : Date.now();
        return ((end - start) / timeBounds.duration) * 100;
    }

    function fmtDuration(ms: number) {
        if (!ms) return '0s';
        if (ms < 1000) return `${ms}ms`;
        return `${(ms/1000).toFixed(1)}s`;
    }
</script>

<div class="gantt-container">
    <h3>Timing</h3>
    <div class="gantt-chart">
        {#each sortedJobs as job}
            {#if job.started_at}
                <div 
                    class="gantt-row" 
                    class:selected={$selectedJob?.job_id === job.job_id}
                    on:click={() => selectedJob.set(job)}
                >
                    <div class="gantt-label">{job.step_id}</div>
                    <div class="gantt-track">
                        <div 
                            class="gantt-bar status-{job.status}" 
                            style="left: {getLeft(job)}%; width: {Math.max(0.5, getWidth(job))}%"
                            title="{job.step_id}: {fmtDuration(job.duration_ms)}"
                        ></div>
                    </div>
                    <div class="gantt-duration">{fmtDuration(job.duration_ms)}</div>
                </div>
            {/if}
        {/each}
    </div>
</div>

<style>
    .gantt-container {
        padding: 20px;
        background: var(--surface);
        border-top: 1px solid var(--border);
    }
    .gantt-container h3 {
        font-size: 11px;
        font-weight: 600;
        letter-spacing: 1px;
        color: var(--muted);
        text-transform: uppercase;
        margin: 0 0 16px 0;
    }
    .gantt-chart {
        display: flex;
        flex-direction: column;
        gap: 8px;
    }
    .gantt-row {
        display: flex;
        align-items: center;
        gap: 12px;
        cursor: pointer;
        padding: 4px;
        border-radius: 4px;
        transition: background 0.2s;
    }
    .gantt-row:hover {
        background: rgba(255,255,255,0.03);
    }
    .gantt-row.selected {
        background: rgba(129, 140, 248, 0.1);
    }
    .gantt-label {
        width: 120px;
        font-size: 12px;
        color: var(--text);
        overflow: hidden;
        text-overflow: ellipsis;
        white-space: nowrap;
    }
    .gantt-track {
        flex: 1;
        height: 12px;
        background: var(--surface2);
        border-radius: 6px;
        position: relative;
        overflow: hidden;
    }
    .gantt-bar {
        position: absolute;
        top: 0;
        height: 100%;
        border-radius: 6px;
        min-width: 4px;
    }
    .gantt-duration {
        width: 60px;
        font-size: 11px;
        color: var(--muted);
        text-align: right;
    }

    .status-passed { background: #10b981; }
    .status-failed { background: #ef4444; }
    .status-timed_out { background: #ef4444; }
    .status-running { background: #3b82f6; }
    .status-queued { background: #6b7280; }
</style>
