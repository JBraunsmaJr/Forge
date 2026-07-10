<script lang="ts">
    import { onMount, tick } from 'svelte';
    import { selectedJob } from '../stores';
    import { api, authUrl, wsUrl, type LogEvent, type Job } from '../api';

    let logs: LogEvent[] = [];
    let logWs: WebSocket | null = null;
    let logBody: HTMLDivElement;
    let loading = false;
    let currentJobId: string | null = null;

    async function scrollToBottom() {
        await tick();
        if (logBody) {
            logBody.scrollTop = logBody.scrollHeight;
        }
    }

    function closeLogStream() {
        if (logWs) {
            logWs.close();
            logWs = null;
        }
        currentJobId = null;
    }

    function openLogWS(jobID: string) {
        if (currentJobId === jobID && logWs) return;
        closeLogStream();
        currentJobId = jobID;
        logWs = new WebSocket(wsUrl(`/api/v1/jobs/${jobID}/logs/stream`));
        logWs.onmessage = (e) => {
            if (!logWs) return;
            const evt: LogEvent = JSON.parse(e.data);
            logs = [...logs, evt];
            scrollToBottom();
        };
    }

    async function handleJobChange(job: Job | null) {
        if (!job) {
            closeLogStream();
            logs = [];
            return;
        }

        if (currentJobId === job.job_id) {
            if (job.status !== 'running' && logWs) {
                logWs.close();
                logWs = null;
            }
            return;
        }

        closeLogStream();
        logs = [];

        const statusMsg = ['pending', 'queued', 'canceled'].includes(job.status);
        if (statusMsg) return;

        if (job.status === 'running') {
            openLogWS(job.job_id);
        } else {
            currentJobId = job.job_id;
            loading = true;
            const result = await api.jobLogs(job.job_id);
            logs = result || [];
            loading = false;
            scrollToBottom();
        }
    }

    export let hideHeader = false;

    $: handleJobChange($selectedJob);

    function formatTime(ts: string) {
        return new Date(ts).toLocaleTimeString('en', {
            hour12: false, hour: '2-digit', minute: '2-digit', second: '2-digit'
        });
    }

    onMount(() => {
        return () => closeLogStream();
    });
</script>

<div id="log-panel" class:no-header={hideHeader}>
    {#if !hideHeader}
    <div id="log-header">
        <h2>Logs</h2>
        <span id="log-job-id">{$selectedJob?.step_id || ''}</span>
    </div>
    {/if}
    <div id="log-body" bind:this={logBody}>
        {#if !$selectedJob}
            <div id="log-empty">Click a job node to view its logs.</div>
        {:else if loading}
            <div class="loading-msg">Loading logs…</div>
        {:else if logs.length === 0}
            <div id="log-empty">
                {#if $selectedJob.status === 'pending'}
                    Job is waiting for its dependencies to complete.
                {:else if $selectedJob.status === 'queued'}
                    Job is queued — waiting for an available agent.
                {:else if $selectedJob.status === 'canceled'}
                    Job was canceled before it ran.
                {:else}
                    No logs stored for this job{#if $selectedJob.status === 'running'} — still running{/if}.
                {/if}
            </div>
        {:else}
            {#each logs as log}
                <div class="log-line">
                    <span class="log-ts">{formatTime(log.ts)}</span>
                    <span class="log-level {log.level}">{log.level}</span>
                    <span class="log-msg">{log.message}</span>
                </div>
            {/each}
        {/if}
    </div>
</div>

<style>
    #log-panel { height: 240px; border-top: 1px solid var(--border); background: var(--surface);
        display: flex; flex-direction: column; flex-shrink: 0; }
    #log-panel.no-header { height: 100%; border-top: none; flex: 1; flex-shrink: 1; }
    #log-header { display: flex; align-items: center; gap: 10px; padding: 8px 16px;
        border-bottom: 1px solid var(--border); flex-shrink: 0; }
    #log-header h2 { font-size: 11px; font-weight: 600; letter-spacing: 1px; color: var(--muted);
        text-transform: uppercase; }
    #log-job-id { font-family: var(--font-mono); font-size: 11px; color: var(--accent); }
    #log-body { flex: 1; overflow-y: auto; padding: 8px 0; }
    .log-line { display: flex; gap: 12px; padding: 2px 16px; font-family: var(--font-mono);
        font-size: 12px; line-height: 1.6; }
    .log-line:hover { background: var(--surface2); }
    .log-ts { color: var(--muted); flex-shrink: 0; }
    .log-level { font-weight: 700; flex-shrink: 0; width: 38px; }
    .log-level.INFO  { color: var(--green); }
    .log-level.WARN  { color: var(--amber); }
    .log-level.ERROR { color: var(--red); }
    .log-msg { color: var(--text); white-space: pre-wrap; word-break: break-all; }
    #log-empty, .loading-msg { color: var(--muted); font-size: 13px; text-align: center; padding: 24px;
        font-family: system-ui, sans-serif; }
    .loading-msg { font-family: var(--font-mono); font-size: 12px; }
</style>
