<script lang="ts">
    import { onMount } from 'svelte';
    import { api, type Token } from '../api';
    import { Plus, Key, Trash2, Copy, Check } from '@lucide/svelte';

    let tokens: Token[] = [];
    let loading = true;
    let showCreate = false;
    let newToken = {
        name: '',
        role: 'admin'
    };
    let createdToken: string | null = null;

    async function loadTokens() {
        loading = true;
        try {
            tokens = await api.listTokens();
        } catch (e) {
            console.error("Failed to load tokens:", e);
        } finally {
            loading = false;
        }
    }

    async function createToken() {
        if (!newToken.name) return;
        const res = await api.createToken(newToken);
        if (res) {
            createdToken = res.token;
            newToken = { name: '', role: 'admin' };
            await loadTokens();
        }
    }

    async function deleteToken(id: string) {
        if (confirm('Are you sure you want to revoke this token?')) {
            await api.deleteToken(id);
            await loadTokens();
        }
    }

    let copied = false;
    function copyToken() {
        if (!createdToken) return;
        navigator.clipboard.writeText(createdToken);
        copied = true;
        setTimeout(() => copied = false, 2000);
    }

    onMount(loadTokens);
</script>

<div class="view-container">
    <div class="view-header">
        <h1>API Tokens</h1>
        <button class="btn-primary" on:click={() => { showCreate = !showCreate; createdToken = null; }}>
            <Plus size={16} />
            New Token
        </button>
    </div>

    {#if showCreate}
        <div class="create-card card">
            {#if createdToken}
                <div class="token-success">
                    <h3>Token Created Successfully</h3>
                    <p>Make sure to copy your token now. You won't be able to see it again!</p>
                    <div class="token-display">
                        <code>{createdToken}</code>
                        <button class="btn-icon" on:click={copyToken}>
                            {#if copied}<Check size={16} color="var(--green)" />{:else}<Copy size={16} />{/if}
                        </button>
                    </div>
                    <button class="btn-primary" on:click={() => showCreate = false}>Done</button>
                </div>
            {:else}
                <h3>Generate New Token</h3>
                <div class="form-group">
                    <label for="tk-name">Token Name</label>
                    <input id="tk-name" type="text" bind:value={newToken.name} placeholder="cli-laptop" />
                </div>
                <div class="form-group">
                    <label for="tk-role">Role</label>
                    <select id="tk-role" bind:value={newToken.role}>
                        <option value="admin">Admin</option>
                        <option value="operator">Operator</option>
                        <option value="viewer">Viewer</option>
                        <option value="agent">Agent</option>
                    </select>
                </div>
                <div class="form-actions">
                    <button class="btn-secondary" on:click={() => showCreate = false}>Cancel</button>
                    <button class="btn-primary" on:click={createToken} disabled={!newToken.name}>Generate</button>
                </div>
            {/if}
        </div>
    {/if}

    {#if loading}
        <div class="loading">Loading tokens...</div>
    {:else if !tokens || tokens.length === 0}
        <div class="empty-state">
            <Key size={48} />
            <p>No API tokens found.</p>
        </div>
    {:else}
        <div class="grid">
            {#each tokens as token}
                <div class="card item-card">
                    <div class="item-icon">
                        <Key size={20} />
                    </div>
                    <div class="item-content">
                        <div class="item-header">
                            <span class="item-name">{token.name}</span>
                            <span class="badge badge-{token.role}">{token.role}</span>
                        </div>
                        <div class="item-id">ID: {token.id}</div>
                        <div class="item-meta">
                            <span>Created {new Date(token.created_at).toLocaleDateString()}</span>
                            {#if token.expires_at}
                                <span>•</span>
                                <span>Expires {new Date(token.expires_at).toLocaleDateString()}</span>
                            {/if}
                        </div>
                    </div>
                    <button class="btn-icon btn-danger" on:click={() => deleteToken(token.id)} title="Revoke Token">
                        <Trash2 size={16} />
                    </button>
                </div>
            {/each}
        </div>
    {/if}
</div>

<style>
    .view-container {
        padding: 24px;
        max-width: 800px;
        margin: 0 auto;
    }
    .view-header {
        display: flex;
        justify-content: space-between;
        align-items: center;
        margin-bottom: 24px;
    }
    h1 {
        margin: 0;
        font-size: 24px;
        font-weight: 600;
    }
    .grid {
        display: flex;
        flex-direction: column;
        gap: 12px;
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
    .form-group {
        margin-bottom: 16px;
    }
    .form-group label {
        display: block;
        margin-bottom: 8px;
        font-size: 13px;
        color: var(--muted);
    }
    .form-group input, .form-group select {
        width: 100%;
        box-sizing: border-box;
    }
    .form-actions {
        display: flex;
        justify-content: flex-end;
        gap: 8px;
    }
    .item-card {
        display: flex;
        gap: 16px;
        align-items: center;
    }
    .item-icon {
        width: 36px;
        height: 36px;
        background: var(--surface2);
        border-radius: 8px;
        display: flex;
        align-items: center;
        justify-content: center;
        color: var(--yellow);
        flex-shrink: 0;
    }
    .item-content {
        flex: 1;
        min-width: 0;
    }
    .item-header {
        display: flex;
        align-items: center;
        gap: 8px;
        margin-bottom: 2px;
    }
    .item-name {
        font-weight: 600;
        font-size: 15px;
    }
    .item-id {
        font-size: var(--font-xs);
        color: var(--muted);
        font-family: monospace;
        margin-bottom: 6px;
    }
    .item-meta {
        font-size: var(--font-xs);
        color: var(--muted);
        display: flex;
        gap: 8px;
    }
    .badge {
        font-size: var(--font-2xs);
        padding: 1px 6px;
        border-radius: 4px;
        text-transform: uppercase;
        font-weight: 700;
    }
    .badge-admin { background: #211a30; color: #a390e4; }
    .badge-operator { background: #2e1d14; color: #e4a390; }
    .badge-viewer { background: #142e1d; color: #90e4a3; }
    .badge-agent { background: #1a2f4a; color: var(--blue); }

    .token-success {
        text-align: center;
    }
    .token-display {
        display: flex;
        align-items: center;
        background: var(--bg);
        border: 1px solid var(--border);
        border-radius: 4px;
        padding: 8px 12px;
        margin: 16px 0;
        gap: 8px;
    }
    .token-display code {
        flex: 1;
        font-family: monospace;
        font-size: 14px;
        color: var(--accent);
        word-break: break-all;
        text-align: left;
    }
    .empty-state {
        text-align: center;
        padding: 64px 0;
        color: var(--muted);
    }
    .loading {
        text-align: center;
        padding: 32px;
        color: var(--muted);
    }
</style>
