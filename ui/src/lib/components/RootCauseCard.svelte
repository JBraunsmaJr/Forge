<script lang="ts">
    import type { RootCauseInfo } from '../api';
    import { categoryLabel, categoryColors, categoryBadgeBg } from '../categories';
    import { Search, Lightbulb, History } from '@lucide/svelte';

    export let rootCause: RootCauseInfo;

    $: label = categoryLabel(rootCause.category);
    $: accent = categoryColors[rootCause.category] || '#8a8a8a';
    $: badgeBg = categoryBadgeBg[rootCause.category] || 'var(--surface)';
</script>

<div class="root-cause-card" style="border-left-color: {accent}">
    <div class="rc-header">
        <Search size={14} />
        <span class="rc-title">Root cause analysis</span>
        <span class="rc-badge" style="background: {badgeBg}; color: {accent}">{label}</span>
    </div>

    <p class="rc-description">{rootCause.description}</p>

    {#if rootCause.matched_line}
        <div class="rc-matched-line">
            <code>{rootCause.matched_line}</code>
        </div>
    {/if}

    {#if rootCause.recent_total > 1}
        <div class="rc-history">
            <History size={12} />
            <span>
                {rootCause.recent_matches} of the last {rootCause.recent_total} failures on this step had the same pattern
            </span>
        </div>
    {/if}

    {#if rootCause.suggested_fix}
        <div class="rc-fix">
            <Lightbulb size={13} />
            <span>{rootCause.suggested_fix}</span>
        </div>
    {/if}
</div>

<style>
    .root-cause-card {
        margin: 12px;
        padding: 12px 14px;
        border-radius: 8px;
        border: 1px solid var(--border);
        background: var(--surface2);
        border-left: 3px solid var(--muted);
    }
    .rc-header {
        display: flex;
        align-items: center;
        gap: 8px;
        margin-bottom: 8px;
        color: var(--text);
    }
    .rc-title {
        font-size: 12px;
        font-weight: 700;
        text-transform: uppercase;
        letter-spacing: 0.3px;
    }
    .rc-badge {
        margin-left: auto;
        font-size: 10px;
        padding: 2px 8px;
        border-radius: 4px;
        font-weight: 700;
        text-transform: uppercase;
    }
    .rc-description {
        margin: 0 0 8px;
        font-size: 13px;
        color: var(--text);
        line-height: 1.5;
    }
    .rc-matched-line {
        background: var(--bg);
        border-radius: 4px;
        padding: 6px 10px;
        margin-bottom: 8px;
        overflow-x: auto;
    }
    .rc-matched-line code {
        font-family: var(--font-mono);
        font-size: 11px;
        color: var(--muted);
        white-space: pre;
    }
    .rc-history {
        display: flex;
        align-items: center;
        gap: 6px;
        font-size: 11px;
        color: var(--muted);
        margin-bottom: 8px;
    }
    .rc-fix {
        display: flex;
        align-items: flex-start;
        gap: 8px;
        font-size: 12px;
        color: var(--text);
        padding-top: 8px;
        border-top: 1px solid var(--border);
        line-height: 1.5;
    }
    .rc-fix :global(svg) {
        flex-shrink: 0;
        margin-top: 1px;
        color: var(--amber, #e4a390);
    }
</style>
