<script lang="ts">
    import type { RootCauseInfo } from '../api';
    import { Search, Lightbulb, History } from '@lucide/svelte';

    export let rootCause: RootCauseInfo;

    const categoryLabels: Record<string, string> = {
        infrastructure: 'Infrastructure',
        dependency: 'Dependency',
        flaky_test: 'Flaky Test',
        code_defect: 'Code Defect',
        configuration: 'Configuration',
        network: 'Network',
        unknown: 'Unclassified',
    };

    $: label = categoryLabels[rootCause.category] || rootCause.category;
</script>

<div class="root-cause-card cat-{rootCause.category}">
    <div class="rc-header">
        <Search size={14} />
        <span class="rc-title">Root cause analysis</span>
        <span class="rc-badge">{label}</span>
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
        background: var(--surface);
        color: var(--muted);
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

    /* Category accents — same dark-surface/bright-text convention used
       for the role badges elsewhere in the app. */
    .cat-infrastructure { border-left-color: #e4a390; }
    .cat-infrastructure .rc-badge { background: #2e1d14; color: #e4a390; }

    .cat-dependency { border-left-color: #a390e4; }
    .cat-dependency .rc-badge { background: #211a30; color: #a390e4; }

    .cat-flaky_test { border-left-color: #e4d490; }
    .cat-flaky_test .rc-badge { background: #302c1a; color: #e4d490; }

    .cat-code_defect { border-left-color: #e49090; }
    .cat-code_defect .rc-badge { background: #301a1a; color: #e49090; }

    .cat-configuration { border-left-color: #90c9e4; }
    .cat-configuration .rc-badge { background: #1a262e; color: #90c9e4; }

    .cat-network { border-left-color: #90e4a3; }
    .cat-network .rc-badge { background: #142e1d; color: #90e4a3; }

    .cat-unknown { border-left-color: var(--muted); }
</style>
