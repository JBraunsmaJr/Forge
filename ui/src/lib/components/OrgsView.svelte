<script lang="ts">
    import { onMount } from 'svelte';
    import { api, type Org } from '../api';
    import { Plus, Building2, Key, ChevronDown, ChevronUp } from '@lucide/svelte';
    import SecretsManager from './SecretsManager.svelte';

    let orgs: Org[] = [];
    let loading = true;
    let newOrgName = '';
    let showCreate = false;
    let openSecretsId: string | null = null;

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
                        <div class="item-id">ID: {org.id}</div>
                        <div class="item-meta">Created {new Date(org.created_at).toLocaleDateString()}</div>
                        
                        <button class="btn-text" on:click={() => openSecretsId = openSecretsId === org.id ? null : org.id}>
                            <Key size={14} />
                            Secrets
                            {#if openSecretsId === org.id}
                                <ChevronUp size={14} />
                            {:else}
                                <ChevronDown size={14} />
                            {/if}
                        </button>

                        {#if openSecretsId === org.id}
                            <SecretsManager scope="org" id={org.id} />
                        {/if}
                    </div>
                </div>
            {/each}
        </div>
    {/if}
</div>

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
    }
    .form-group {
        margin-bottom: 16px;
    }
    .form-group label {
        display: block;
        margin-bottom: 8px;
        font-size: 13px;
        color: var(--muted);
    }
    .form-group input {
        width: 100%;
        padding: 8px 12px;
        background: var(--bg);
        border: 1px solid var(--border);
        border-radius: 4px;
        color: var(--text);
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
        font-size: 14px;
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
        font-size: 13px;
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
    }
    .item-name {
        font-weight: 600;
        font-size: 16px;
        margin-bottom: 4px;
    }
    .item-id {
        font-size: 12px;
        color: var(--muted);
        font-family: monospace;
        margin-bottom: 8px;
    }
    .item-meta {
        font-size: 12px;
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
