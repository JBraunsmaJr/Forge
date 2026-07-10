<script lang="ts">
    import { onMount } from 'svelte';
    import { api, type Policy, type Org } from '../api';
    import { Plus, ShieldCheck, Trash2, Edit } from '@lucide/svelte';

    let policies: Policy[] = [];
    let orgs: Org[] = [];
    let loading = true;
    let selectedOrgId = '';
    
    let showCreate = false;
    let editingPolicyId: string | null = null;
    let newPolicy = {
        name: '',
        description: '',
        steps_json: '[]',
        transformer_json: '',
        forbid_override: false
    };

    async function loadData() {
        loading = true;
        try {
            orgs = await api.listOrgs();
            if (orgs.length > 0) {
                selectedOrgId = orgs[0].id;
                await refreshPolicies();
            }
        } catch (e) {
            console.error("Failed to load policies data:", e);
        } finally {
            loading = false;
        }
    }

    async function refreshPolicies() {
        if (!selectedOrgId) {
            policies = [];
            return;
        }
        policies = await api.listPolicies(selectedOrgId);
    }

    async function savePolicy() {
        if (!newPolicy.name || !selectedOrgId) return;
        try {
            const steps = JSON.parse(newPolicy.steps_json);
            let transformer = null;
            if (newPolicy.transformer_json && newPolicy.transformer_json.trim()) {
                transformer = JSON.parse(newPolicy.transformer_json);
            }
            
            if (editingPolicyId) {
                const pol = await api.updatePolicy(selectedOrgId, editingPolicyId, {
                    name: newPolicy.name,
                    description: newPolicy.description,
                    steps: steps,
                    transformer: transformer,
                    forbid_override: newPolicy.forbid_override
                });
                if (pol) {
                    resetForm();
                    await refreshPolicies();
                }
            } else {
                const pol = await api.createPolicy(selectedOrgId, {
                    name: newPolicy.name,
                    description: newPolicy.description,
                    steps: steps,
                    transformer: transformer,
                    forbid_override: newPolicy.forbid_override
                });
                if (pol) {
                    resetForm();
                    await refreshPolicies();
                }
            }
        } catch (e) {
            alert('Invalid JSON: ' + e);
        }
    }

    function resetForm() {
        newPolicy = { name: '', description: '', steps_json: '[]', transformer_json: '', forbid_override: false };
        showCreate = false;
        editingPolicyId = null;
    }

    function startEdit(policy: Policy) {
        newPolicy = {
            name: policy.name,
            description: policy.description,
            steps_json: JSON.stringify(policy.steps || [], null, 2),
            transformer_json: policy.transformer ? JSON.stringify(policy.transformer, null, 2) : '',
            forbid_override: policy.forbid_override
        };
        editingPolicyId = policy.id;
        showCreate = true;
    }

    async function deletePolicy(id: string) {
        if (confirm('Are you sure you want to delete this policy?')) {
            await api.deletePolicy(selectedOrgId, id);
            await refreshPolicies();
        }
    }

    onMount(loadData);
</script>

<div class="view-container">
    <div class="view-header">
        <h1>Policies</h1>
        <div class="header-actions">
            <select bind:value={selectedOrgId} on:change={refreshPolicies}>
                {#each orgs as org}
                    <option value={org.id}>{org.name}</option>
                {/each}
            </select>
            <button class="btn-primary" on:click={() => showCreate = !showCreate} disabled={!selectedOrgId}>
                <Plus size={16} />
                New Policy
            </button>
        </div>
    </div>

    {#if showCreate}
        <div class="create-card card">
            <h3>{editingPolicyId ? 'Edit Policy' : 'Create New Policy'}</h3>
            <div class="form-group">
                <label for="pol-name">Policy Name</label>
                <input id="pol-name" type="text" bind:value={newPolicy.name} placeholder="security-scan" />
            </div>
            <div class="form-group">
                <label for="pol-desc">Description</label>
                <input id="pol-desc" type="text" bind:value={newPolicy.description} placeholder="Runs security tools on every build" />
            </div>
            <div class="form-group">
                <label for="pol-steps">Steps (JSON Array)</label>
                <textarea id="pol-steps" bind:value={newPolicy.steps_json} rows="6"></textarea>
                <small>See <a href="https://github.com/JBraunsmaJr/forge/blob/main/docs/pipeline-reference.md" target="_blank" rel="noopener noreferrer">docs</a> for step schema.</small>
            </div>
            <div class="form-group">
                <label for="pol-transformer">Transformer (JSON Object)</label>
                <textarea id="pol-transformer" bind:value={newPolicy.transformer_json} rows="6"></textarea>
                <small>Optional. Dynamic transformation logic. Use lowercase keys (e.g. "image", "command").</small>
            </div>
            <div class="form-group checkbox">
                <label>
                    <input type="checkbox" bind:checked={newPolicy.forbid_override} />
                    Forbid Override
                </label>
            </div>
            <div class="form-actions">
                <button class="btn-secondary" on:click={resetForm}>Cancel</button>
                <button class="btn-primary" on:click={savePolicy} disabled={!newPolicy.name}>
                    {editingPolicyId ? 'Save Changes' : 'Create'}
                </button>
            </div>
        </div>
    {/if}

    {#if loading}
        <div class="loading">Loading policies...</div>
    {:else if !selectedOrgId}
        <div class="empty-state">
            <p>Select or create an organization first.</p>
        </div>
    {:else if !policies || policies.length === 0}
        <div class="empty-state">
            <ShieldCheck size={48} />
            <p>No policies found for this organization.</p>
        </div>
    {:else}
        <div class="grid">
            {#each policies as policy}
                <div class="card item-card">
                    <div class="item-icon">
                        <ShieldCheck size={24} />
                    </div>
                    <div class="item-content">
                        <div class="item-header">
                            <span class="item-name">{policy.name}</span>
                            {#if policy.forbid_override}
                                <span class="badge badge-locked">Locked</span>
                            {/if}
                        </div>
                        <div class="item-desc">{policy.description}</div>
                        <div class="item-meta">
                            <span>ID: {policy.id}</span>
                            <span>•</span>
                            <span>Created {new Date(policy.created_at).toLocaleDateString()}</span>
                        </div>
                    </div>
                    <div class="item-actions">
                        <button class="btn-icon" on:click={() => startEdit(policy)} title="Edit Policy">
                            <Edit size={16} />
                        </button>
                        <button class="btn-icon btn-danger" on:click={() => deletePolicy(policy.id)} title="Delete Policy">
                            <Trash2 size={16} />
                        </button>
                    </div>
                </div>
            {/each}
        </div>
    {/if}
</div>

<style>
    .view-container {
        padding: 24px;
        max-width: 1000px;
        margin: 0 auto;
    }
    .view-header {
        display: flex;
        justify-content: space-between;
        align-items: center;
        margin-bottom: 24px;
    }
    .header-actions {
        display: flex;
        gap: 12px;
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
    .form-group input, .form-group textarea {
        width: 100%;
        padding: 8px 12px;
        background: var(--bg);
        border: 1px solid var(--border);
        border-radius: 4px;
        color: var(--text);
        box-sizing: border-box;
        font-family: inherit;
    }
    textarea {
        font-family: monospace;
        font-size: 13px;
    }
    .checkbox label {
        display: flex;
        align-items: center;
        gap: 8px;
        cursor: pointer;
        color: var(--text);
    }
    .checkbox input {
        width: auto;
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
    .btn-icon {
        background: transparent;
        border: none;
        color: var(--muted);
        cursor: pointer;
        padding: 8px;
        border-radius: 4px;
        display: flex;
        align-items: center;
        justify-content: center;
    }
    .btn-danger:hover {
        background: var(--red-muted);
        color: var(--red);
    }
    .item-actions {
        display: flex;
        gap: 4px;
    }
    .item-card {
        display: flex;
        gap: 16px;
        align-items: center;
    }
    .item-icon {
        width: 40px;
        height: 40px;
        background: var(--surface2);
        border-radius: 8px;
        display: flex;
        align-items: center;
        justify-content: center;
        color: var(--green);
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
    .item-desc {
        font-size: 13px;
        color: var(--text);
        margin-bottom: 6px;
    }
    .item-meta {
        font-size: 11px;
        color: var(--muted);
        display: flex;
        gap: 8px;
    }
    .badge-locked {
        background: #2e2614;
        color: #f0b429;
        font-size: 10px;
        padding: 1px 6px;
        border-radius: 4px;
        text-transform: uppercase;
        font-weight: 700;
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
    small {
        display: block;
        margin-top: 4px;
        font-size: 11px;
        color: var(--muted);
    }
    small a {
        color: var(--accent);
        text-decoration: none;
    }
</style>
