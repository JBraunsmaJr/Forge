<script lang="ts">
    import { onMount } from 'svelte';
    import { api, type Org } from '../api';
    import { Plus, Building2, ArrowRight } from '@lucide/svelte';
    import OrgDetailView from './OrgDetailView.svelte';

    let orgs: Org[] = [];
    let loading = true;
    let newOrgName = '';
    let showCreate = false;
    // The one org currently open in the detail view — mirrors
    // ProjectsView's selectedProject pattern for consistency.
    let selectedOrg: Org | null = null;

    async function loadOrgs() {
        loading = true;
        try {
            orgs = await api.listOrgs();
        } catch (e) {
            console.error("Failed to load orgs:", e);
        } finally {
            loading = false;
        }
    }

    async function createOrg() {
        if (!newOrgName) return;
        const org = await api.createOrg(newOrgName);
        if (org) {
            newOrgName = '';
            showCreate = false;
            loadOrgs();
        }
    }

    onMount(loadOrgs);
</script>

{#if selectedOrg}
    <div class="view-container">
        <OrgDetailView org={selectedOrg} on:back={() => selectedOrg = null} />
    </div>
{:else}
<div class="view-container">
    <div class="view-header">
        <h1>Organizations</h1>
        <button class="btn-primary" on:click={() => showCreate = !showCreate}>
            <Plus size={16} />
            New Org
        </button>
    </div>

    {#if showCreate}
        <div class="create-card card">
            <h3>Create New Organization</h3>
            <div class="form-group">
                <label for="org-name">Name</label>
                <input id="org-name" type="text" bind:value={newOrgName} placeholder="Acme Corp" />
            </div>
            <div class="form-actions">
                <button class="btn-secondary" on:click={() => showCreate = false}>Cancel</button>
                <button class="btn-primary" on:click={createOrg} disabled={!newOrgName}>Create</button>
            </div>
        </div>
    {/if}

    {#if loading}
        <div class="loading">Loading organizations...</div>
    {:else if !orgs || orgs.length === 0}
        <div class="empty-state">
            <Building2 size={48} />
            <p>No organizations found.</p>
        </div>
    {:else}
        <div class="grid">
            {#each orgs as org}
                <div class="card item-card">
                    <div class="item-icon">
                        <Building2 size={24} />
                    </div>
                    <div class="item-details">
                        <div class="item-name">{org.name}</div>
                        <div class="item-meta">Created {new Date(org.created_at).toLocaleDateString()}</div>

                        <button class="btn-text" on:click={() => selectedOrg = org}>
                            Manage
                            <ArrowRight size={14} />
                        </button>
                    </div>
                </div>
            {/each}
        </div>
    {/if}
</div>
{/if}

<style>
    .grid {
        display: grid;
        grid-template-columns: repeat(auto-fill, minmax(300px, 1fr));
        gap: 16px;
    }
    .card {
        background: var(--surface);
        border: 1px solid var(--border);
        border-radius: 8px;
        padding: 16px;
    }
    .create-card {
        margin-bottom: 24px;
    }
    .create-card h3 {
        margin-top: 0;
        margin-bottom: 16px;
        font-size: var(--font-lg);
    }
    .form-group {
        margin-bottom: 16px;
    }
    .form-group label {
        display: block;
        margin-bottom: 8px;
        font-size: var(--font-sm);
        color: var(--muted);
    }
    .form-group input {
        width: 100%;
        padding: 8px 12px;
        background: var(--bg);
        border: 1px solid var(--border);
        border-radius: 4px;
        color: var(--text);
        font-size: var(--font-base);
    }
    .form-actions {
        display: flex;
        justify-content: flex-end;
        gap: 8px;
    }
    .btn-primary {
        background: var(--accent);
        color: white;
        border: none;
        padding: 8px 16px;
        border-radius: 4px;
        cursor: pointer;
        display: flex;
        align-items: center;
        gap: 8px;
        font-size: var(--font-base);
        font-weight: 500;
    }
    .btn-primary:disabled {
        opacity: 0.5;
        cursor: not-allowed;
    }
    .btn-secondary {
        background: transparent;
        color: var(--text);
        border: 1px solid var(--border);
        padding: 8px 16px;
        border-radius: 4px;
        cursor: pointer;
        font-size: var(--font-base);
    }
    .btn-text {
        background: transparent;
        border: none;
        color: var(--muted);
        padding: 4px 0;
        cursor: pointer;
        display: flex;
        align-items: center;
        gap: 6px;
        font-size: var(--font-sm);
        margin-top: 8px;
    }
    .btn-text:hover {
        color: var(--accent);
    }
    .item-card {
        display: flex;
        gap: 16px;
        align-items: flex-start;
    }
    .item-icon {
        width: 48px;
        height: 48px;
        background: var(--surface2);
        border-radius: 8px;
        display: flex;
        align-items: center;
        justify-content: center;
        color: var(--accent);
        flex-shrink: 0;
    }
    .item-name {
        font-weight: 600;
        font-size: var(--font-md);
        margin-bottom: 4px;
    }
    .item-meta {
        font-size: var(--font-xs);
        color: var(--muted);
    }
    .empty-state {
        text-align: center;
        padding: 64px 0;
        color: var(--muted);
    }
    .empty-state p {
        margin-top: 16px;
    }
    .loading {
        text-align: center;
        padding: 32px;
        color: var(--muted);
    }
</style>
