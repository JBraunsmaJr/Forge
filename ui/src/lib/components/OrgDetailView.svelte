<script lang="ts">
    import { createEventDispatcher } from 'svelte';
    import type { Org } from '../api';
    import { ArrowLeft, Building2, Key, Settings } from '@lucide/svelte';
    import SecretsManager from './SecretsManager.svelte';

    export let org: Org;

    const dispatch = createEventDispatcher<{ back: void }>();

    type Tab = 'overview' | 'secrets';
    let activeTab: Tab = 'overview';
</script>

<div class="detail-view">
    <div class="detail-header">
        <button class="btn-back" on:click={() => dispatch('back')}>
            <ArrowLeft size={16} />
            Organizations
        </button>
    </div>

    <div class="detail-title-row">
        <div class="detail-icon"><Building2 size={26} /></div>
        <div class="detail-title-text">
            <h1>{org.name}</h1>
            <div class="detail-subtitle">
                <span>ID: {org.id}</span>
                <span class="dot-sep">·</span>
                <span>Created {new Date(org.created_at).toLocaleDateString()}</span>
            </div>
        </div>
    </div>

    <div class="tab-bar">
        <button class:active={activeTab === 'overview'} on:click={() => activeTab = 'overview'}>
            <Settings size={14} /> Overview
        </button>
        <button class:active={activeTab === 'secrets'} on:click={() => activeTab = 'secrets'}>
            <Key size={14} /> Secrets
        </button>
    </div>

    <div class="tab-content">
        {#if activeTab === 'overview'}
            <section class="panel">
                <h2>Details</h2>
                <div class="detail-row">
                    <span class="detail-label">Name</span>
                    <span>{org.name}</span>
                </div>
                <div class="detail-row">
                    <span class="detail-label">ID</span>
                    <span class="mono">{org.id}</span>
                </div>
                <div class="detail-row">
                    <span class="detail-label">Created</span>
                    <span>{new Date(org.created_at).toLocaleString()}</span>
                </div>
                <p class="hint">Projects are assigned to this organization from the Projects page. Org-scoped secrets below are available to every project in this org that doesn't override them at the project level.</p>
            </section>
        {:else if activeTab === 'secrets'}
            <SecretsManager scope="org" id={org.id} />
        {/if}
    </div>
</div>

<style>
    .detail-view {
        display: flex;
        flex-direction: column;
        gap: 20px;
    }
    .detail-header {
        display: flex;
    }
    .btn-back {
        background: transparent;
        border: none;
        color: var(--muted);
        cursor: pointer;
        display: flex;
        align-items: center;
        gap: 6px;
        font-size: var(--font-sm);
        padding: 4px 0;
    }
    .btn-back:hover { color: var(--accent); }

    .detail-title-row {
        display: flex;
        align-items: center;
        gap: 16px;
    }
    .detail-icon {
        width: 52px;
        height: 52px;
        background: var(--surface2);
        border-radius: 10px;
        display: flex;
        align-items: center;
        justify-content: center;
        color: var(--accent);
        flex-shrink: 0;
    }
    .detail-title-text { flex: 1; min-width: 0; }
    .detail-title-text h1 {
        margin: 0 0 4px;
        font-size: var(--font-2xl);
        font-weight: 700;
    }
    .detail-subtitle {
        display: flex;
        align-items: center;
        gap: 8px;
        font-size: var(--font-sm);
        color: var(--muted);
    }
    .dot-sep { opacity: 0.5; }

    .tab-bar {
        display: flex;
        gap: 4px;
        border-bottom: 1px solid var(--border);
    }
    .tab-bar button {
        background: transparent;
        border: none;
        border-bottom: 2px solid transparent;
        color: var(--muted);
        padding: 10px 14px;
        cursor: pointer;
        display: flex;
        align-items: center;
        gap: 6px;
        font-size: var(--font-sm);
        font-weight: 500;
    }
    .tab-bar button:hover { color: var(--text); }
    .tab-bar button.active { color: var(--accent); border-bottom-color: var(--accent); }

    .tab-content { min-height: 200px; }

    .panel {
        background: var(--surface);
        border: 1px solid var(--border);
        border-radius: 10px;
        padding: 20px;
        max-width: 560px;
    }
    .panel h2 {
        margin: 0 0 16px;
        font-size: var(--font-md);
        font-weight: 600;
    }
    .detail-row {
        display: flex;
        justify-content: space-between;
        gap: 16px;
        padding: 10px 0;
        border-bottom: 1px solid var(--border);
        font-size: var(--font-sm);
    }
    .detail-row:last-of-type { border-bottom: none; }
    .detail-label { color: var(--muted); }
    .mono { font-family: var(--font-mono); font-size: var(--font-xs); }
    .hint {
        font-size: var(--font-xs);
        color: var(--muted);
        margin-top: 16px;
        line-height: 1.5;
    }
</style>
