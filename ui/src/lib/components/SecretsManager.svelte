<script lang="ts">
    import { onMount } from 'svelte';
    import { api } from '../api';
    import { Key, Trash2, Plus, ShieldAlert } from '@lucide/svelte';

    export let scope: 'org' | 'project';
    export let id: string;

    let secretNames: string[] = [];
    let loading = true;
    let newSecret = { name: '', value: '' };
    let showAdd = false;
    let error = '';

    async function loadSecrets() {
        loading = true;
        error = '';
        try {
            if (scope === 'org') {
                secretNames = await api.listOrgSecrets(id);
            } else {
                secretNames = await api.listProjectSecrets(id);
            }
        } catch (e) {
            console.error("Failed to load secrets:", e);
            error = "Failed to load secrets.";
        } finally {
            loading = false;
        }
    }

    async function addSecret() {
        if (!newSecret.name || !newSecret.value) return;
        error = '';
        try {
            let success = false;
            if (scope === 'org') {
                success = await api.setOrgSecret(id, newSecret.name, newSecret.value);
            } else {
                success = await api.setProjectSecret(id, newSecret.name, newSecret.value);
            }
            if (success) {
                newSecret = { name: '', value: '' };
                showAdd = false;
                await loadSecrets();
            } else {
                error = "Failed to store secret.";
            }
        } catch (e) {
            console.error("Failed to add secret:", e);
            error = "An error occurred.";
        }
    }

    async function deleteSecret(name: string) {
        if (!confirm(`Are you sure you want to delete secret "${name}"?`)) return;
        try {
            let success = false;
            if (scope === 'org') {
                success = await api.deleteOrgSecret(id, name);
            } else {
                success = await api.deleteProjectSecret(id, name);
            }
            if (success) {
                await loadSecrets();
            }
        } catch (e) {
            console.error("Failed to delete secret:", e);
        }
    }

    onMount(loadSecrets);
</script>

<div class="secrets-container">
    <div class="secrets-header">
        <div class="title">
            <Key size={16} />
            <span>Secrets</span>
        </div>
        <button class="btn-small" on:click={() => showAdd = !showAdd}>
            <Plus size={14} />
            Add
        </button>
    </div>

    {#if error}
        <div class="error-msg">{error}</div>
    {/if}

    {#if showAdd}
        <div class="add-form">
            <input type="text" bind:value={newSecret.name} placeholder="Name (e.g. DB_PASSWORD)" />
            <input type="password" bind:value={newSecret.value} placeholder="Value" />
            <div class="actions">
                <button class="btn-confirm" on:click={addSecret} disabled={!newSecret.name || !newSecret.value}>Save</button>
                <button class="btn-cancel" on:click={() => showAdd = false}>Cancel</button>
            </div>
        </div>
    {/if}

    {#if loading}
        <div class="loading-small">Loading secrets...</div>
    {:else if secretNames.length === 0}
        <div class="empty-small">No secrets configured.</div>
    {:else}
        <ul class="secrets-list">
            {#each secretNames as name}
                <li>
                    <span class="name">{name}</span>
                    <button class="btn-delete" on:click={() => deleteSecret(name)} title="Delete Secret">
                        <Trash2 size={14} />
                    </button>
                </li>
            {/each}
        </ul>
    {/if}

    <div class="disclaimer">
        <ShieldAlert size={12} />
        <span>Secrets are write-only and not visible after creation.</span>
    </div>
</div>

<style>
    .secrets-container {
        margin-top: 12px;
        padding-top: 12px;
        border-top: 1px solid var(--border);
    }
    .secrets-header {
        display: flex;
        justify-content: space-between;
        align-items: center;
        margin-bottom: 12px;
    }
    .title {
        display: flex;
        align-items: center;
        gap: 6px;
        font-size: 13px;
        font-weight: 600;
        color: var(--text);
    }
    .btn-small {
        background: transparent;
        border: 1px solid var(--border);
        color: var(--text);
        padding: 2px 8px;
        border-radius: 4px;
        font-size: 11px;
        cursor: pointer;
        display: flex;
        align-items: center;
        gap: 4px;
    }
    .btn-small:hover {
        background: var(--surface2);
    }
    .add-form {
        background: var(--surface2);
        padding: 12px;
        border-radius: 6px;
        margin-bottom: 12px;
        display: flex;
        flex-direction: column;
        gap: 8px;
    }
    .add-form input {
        width: 100%;
        background: var(--bg);
        border: 1px solid var(--border);
        border-radius: 4px;
        color: var(--text);
        padding: 6px 10px;
        font-size: 12px;
        box-sizing: border-box;
    }
    .add-form .actions {
        display: flex;
        justify-content: flex-end;
        gap: 8px;
    }
    .btn-confirm {
        background: var(--accent);
        color: white;
        border: none;
        padding: 4px 12px;
        border-radius: 4px;
        font-size: 12px;
        cursor: pointer;
    }
    .btn-confirm:disabled {
        opacity: 0.5;
        cursor: not-allowed;
    }
    .btn-cancel {
        background: transparent;
        color: var(--muted);
        border: none;
        font-size: 12px;
        cursor: pointer;
    }
    .secrets-list {
        list-style: none;
        padding: 0;
        margin: 0;
    }
    .secrets-list li {
        display: flex;
        justify-content: space-between;
        align-items: center;
        padding: 4px 0;
        border-bottom: 1px solid var(--border-light);
    }
    .secrets-list li:last-child {
        border-bottom: none;
    }
    .name {
        font-family: monospace;
        font-size: 12px;
    }
    .btn-delete {
        background: transparent;
        border: none;
        color: var(--muted);
        cursor: pointer;
        padding: 4px;
        border-radius: 4px;
    }
    .btn-delete:hover {
        color: var(--red);
        background: var(--red-muted);
    }
    .loading-small, .empty-small {
        font-size: 12px;
        color: var(--muted);
        text-align: center;
        padding: 8px;
    }
    .error-msg {
        color: var(--red);
        font-size: 12px;
        margin-bottom: 8px;
    }
    .disclaimer {
        display: flex;
        align-items: center;
        gap: 6px;
        font-size: 11px;
        color: var(--muted);
        margin-top: 12px;
        font-style: italic;
    }
</style>
