<script lang="ts">
    import { onMount } from 'svelte';
    import { api, type FailureBreakdown } from '../api';
    import { categoryLabels, categoryColors } from '../categories';
    import { ChartBar } from '@lucide/svelte';

    export let id: string;

    let breakdown: FailureBreakdown | null = null;
    let loading = true;
    let error = '';

    $: sortedCategories = breakdown
        ? Object.entries(breakdown.categories).sort((a, b) => b[1] - a[1])
        : [];

    $: preventableShare = breakdown && breakdown.total_failures > 0
        ? ((breakdown.categories.infrastructure || 0) +
           (breakdown.categories.flaky_test || 0) +
           (breakdown.categories.configuration || 0) +
           (breakdown.categories.network || 0)) / breakdown.total_failures * 100
        : 0;

    async function load() {
        loading = true;
        error = '';
        try {
            breakdown = await api.getFailureStats(id, 30);
            if (!breakdown) error = 'Failed to load failure insights.';
        } catch (e) {
            console.error('Failed to load failure insights:', e);
            error = 'An error occurred while loading failure insights.';
        } finally {
            loading = false;
        }
    }

    onMount(load);

    function pct(n: number): number {
        if (!breakdown || breakdown.total_failures === 0) return 0;
        return Math.round((n / breakdown.total_failures) * 100);
    }
</script>

<div class="insights-container">
    <div class="insights-header">
        <ChartBar size={16} />
        <span>Failure Insights</span>
        <span class="window">last {breakdown?.window_days ?? 30} days</span>
    </div>

    {#if loading}
        <div class="state-msg">Loading…</div>
    {:else if error}
        <div class="state-msg error">{error}</div>
    {:else if !breakdown || breakdown.total_failures === 0}
        <div class="state-msg">No classified failures in this window.</div>
    {:else}
        <div class="bars">
            {#each sortedCategories as [category, count]}
                <div class="bar-row">
                    <span class="bar-label">{categoryLabels[category] || category}</span>
                    <div class="bar-track">
                        <div
                            class="bar-fill"
                            style="width: {pct(count)}%; background: {categoryColors[category] || '#8a8a8a'}"
                        ></div>
                    </div>
                    <span class="bar-pct">{pct(count)}%</span>
                </div>
            {/each}
        </div>
        <p class="summary">
            {breakdown.total_failures} classified failure{breakdown.total_failures === 1 ? '' : 's'} in the last {breakdown.window_days} days.
            {#if preventableShare > 0}
                ~{Math.round(preventableShare)}% were not caused by a defect in the product code under test — infrastructure, dependencies, configuration, or test flakiness.
            {/if}
        </p>
    {/if}
</div>

<style>
    .insights-container {
        margin-top: 12px;
        padding-top: 12px;
        border-top: 1px solid var(--border);
    }
    .insights-header {
        display: flex;
        align-items: center;
        gap: 6px;
        font-size: 13px;
        font-weight: 600;
        color: var(--text);
        margin-bottom: 12px;
    }
    .window {
        margin-left: auto;
        font-size: var(--font-xs);
        font-weight: 500;
        color: var(--muted);
        text-transform: none;
    }
    .state-msg {
        font-size: 12px;
        color: var(--muted);
        padding: 4px 0;
    }
    .state-msg.error {
        color: var(--red);
    }
    .bars {
        display: flex;
        flex-direction: column;
        gap: 8px;
        margin-bottom: 10px;
    }
    .bar-row {
        display: flex;
        align-items: center;
        gap: 8px;
    }
    .bar-label {
        font-size: var(--font-xs);
        color: var(--muted);
        width: 90px;
        flex-shrink: 0;
    }
    .bar-track {
        flex: 1;
        height: 8px;
        border-radius: 4px;
        background: var(--surface2);
        overflow: hidden;
    }
    .bar-fill {
        height: 100%;
        border-radius: 4px;
    }
    .bar-pct {
        font-size: var(--font-xs);
        color: var(--text);
        width: 34px;
        text-align: right;
        flex-shrink: 0;
    }
    .summary {
        font-size: var(--font-xs);
        color: var(--muted);
        line-height: 1.5;
        margin: 0;
        font-style: italic;
    }
</style>
