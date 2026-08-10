<script lang="ts">
    import type { DockerPublishConfig, DockerPublishResult } from '../api';
    import { Container, Tag, Trash2, TriangleAlert, CheckCircle2, Loader2, XCircle, Clock } from '@lucide/svelte';

    export let config: DockerPublishConfig | undefined;
    export let result: DockerPublishResult | undefined;
    // status is the job's own status (queued/docker_publish/running/
    // failed/etc) — server.go only calls SetDockerPublishResult once
    // the step actually starts executing, so a queued, running, or
    // early-failed job (e.g. it never got as far as fetching the
    // source manifest) has no result yet. Without this, those states
    // rendered an empty card with no explanation.
    export let status: string | undefined = undefined;

    const STATUS_LABEL: Record<string, string> = {
        pending: 'Waiting on dependencies',
        queued: 'Queued',
        docker_publish: 'Queued for publish',
        running: 'Publishing…',
        failed: 'Failed',
        timed_out: 'Timed out',
        canceled: 'Canceled',
        skipped: 'Skipped',
    };
</script>

<div class="publish-card">
    <div class="pc-header">
        <Container size={14} />
        <span class="pc-title">Docker publish</span>
        {#if config}
            <span class="pc-target" title="Configured registry/repository — may still contain unresolved template tokens until this step actually runs">{config.registry}/{config.repository}</span>
        {/if}
    </div>

    {#if !result && status && STATUS_LABEL[status]}
        <div class="pc-status pc-status-{status}">
            {#if status === 'running' || status === 'docker_publish'}
                <Loader2 size={12} class="pc-spin" />
            {:else if status === 'failed' || status === 'timed_out'}
                <XCircle size={12} />
            {:else}
                <Clock size={12} />
            {/if}
            <span>{STATUS_LABEL[status]}</span>
        </div>
    {/if}

    {#if config}
        <div class="pc-source">
            <span class="pc-label">Configured source</span>
            <code>{config.source}</code>
        </div>
    {/if}

    {#if result}
        <div class="pc-tags">
            <span class="pc-label">Tags applied</span>
            {#if result.tags_applied?.length}
                <div class="tag-list">
                    {#each result.tags_applied as tag}
                        <span class="tag-chip">
                            <Tag size={10} />
                            {tag}
                        </span>
                    {/each}
                </div>
            {:else}
                <span class="pc-empty">none</span>
            {/if}
        </div>

        {#if result.source_digest}
            <div class="pc-digest">
                <span class="pc-label">Source digest</span>
                <code>{result.source_digest}</code>
            </div>
        {/if}

        {#if config?.delete_source}
            <div class="pc-deletion">
                {#if result.source_deleted}
                    <CheckCircle2 size={12} />
                    <span>Source tag deleted</span>
                {:else}
                    <Trash2 size={12} />
                    <span>Source tag not deleted</span>
                {/if}
            </div>
        {/if}

        {#if result.warnings?.length}
            <div class="pc-warnings">
                {#each result.warnings as w}
                    <div class="pc-warning">
                        <TriangleAlert size={12} />
                        <span>{w}</span>
                    </div>
                {/each}
            </div>
        {/if}
    {/if}
</div>

<style>
    .publish-card {
        margin: 12px;
        padding: 12px 14px;
        border-radius: 8px;
        border: 1px solid var(--border);
        background: var(--surface2);
    }
    .pc-header {
        display: flex;
        align-items: center;
        gap: 8px;
        margin-bottom: 10px;
        color: var(--text);
    }
    .pc-title {
        font-size: 12px;
        font-weight: 700;
        text-transform: uppercase;
        letter-spacing: 0.3px;
    }
    .pc-target {
        margin-left: auto;
        font-size: var(--font-xs);
        font-family: var(--font-mono);
        color: var(--muted);
    }
    .pc-label {
        display: block;
        font-size: var(--font-xs);
        color: var(--muted);
        margin-bottom: 4px;
    }
    .pc-status {
        display: flex;
        align-items: center;
        gap: 6px;
        font-size: 12px;
        color: var(--muted);
        margin-bottom: 10px;
    }
    .pc-status-failed, .pc-status-timed_out {
        color: var(--red, #f85149);
    }
    :global(.pc-spin) {
        animation: pc-spin 1s linear infinite;
    }
    @keyframes pc-spin {
        from { transform: rotate(0deg); }
        to { transform: rotate(360deg); }
    }
    .pc-source, .pc-digest {
        margin-bottom: 10px;
    }
    .pc-source code, .pc-digest code {
        font-family: var(--font-mono);
        font-size: var(--font-xs);
        color: var(--text);
        background: var(--bg);
        border-radius: 4px;
        padding: 3px 6px;
        word-break: break-all;
    }
    .pc-tags {
        margin-bottom: 10px;
    }
    .pc-empty {
        font-size: 12px;
        color: var(--muted);
        font-style: italic;
    }
    .tag-list {
        display: flex;
        flex-wrap: wrap;
        gap: 6px;
    }
    .tag-chip {
        display: inline-flex;
        align-items: center;
        gap: 4px;
        background: var(--bg);
        border: 1px solid var(--border);
        border-radius: 10px;
        padding: 2px 8px;
        font-size: var(--font-xs);
        font-family: var(--font-mono);
        color: var(--accent);
    }
    .pc-deletion {
        display: flex;
        align-items: center;
        gap: 6px;
        font-size: 12px;
        color: var(--muted);
        margin-bottom: 8px;
    }
    .pc-warnings {
        padding-top: 8px;
        border-top: 1px solid var(--border);
    }
    .pc-warning {
        display: flex;
        align-items: flex-start;
        gap: 8px;
        font-size: 12px;
        color: var(--amber, #d29922);
        line-height: 1.5;
        margin-bottom: 6px;
    }
    .pc-warning:last-child {
        margin-bottom: 0;
    }
    .pc-warning :global(svg) {
        flex-shrink: 0;
        margin-top: 2px;
    }
</style>
