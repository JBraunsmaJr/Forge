<script lang="ts">
    import { api, type ProjectWebhook } from '../api';
    import { Webhook, Eye, EyeOff, Copy, ShieldAlert } from '@lucide/svelte';

    export let id: string;

    let webhook: ProjectWebhook | null = null;
    let loading = false;
    let revealed = false;
    let error = '';
    let copiedField = '';
    let copyTimer: ReturnType<typeof setTimeout>;

    async function reveal() {
        loading = true;
        error = '';
        try {
            const res = await api.getProjectWebhook(id);
            if (res.ok && res.data) {
                webhook = res.data;
                revealed = true;
            } else if (res.status === 401) {
                error = 'Your session token is invalid or expired — please log out and sign in again.';
            } else if (res.status === 403) {
                error = 'You need admin access to view the webhook secret.';
            } else {
                error = 'Failed to load webhook details.';
            }
        } catch (e) {
            console.error('Failed to load webhook details:', e);
            error = 'An error occurred while loading the webhook details.';
        } finally {
            loading = false;
        }
    }

    function hide() {
        revealed = false;
        webhook = null;
        error = '';
    }

    async function copy(text: string, field: string) {
        try {
            await navigator.clipboard.writeText(text);
            copiedField = field;
            clearTimeout(copyTimer);
            copyTimer = setTimeout(() => { copiedField = ''; }, 1500);
        } catch (e) {
            console.error('Copy failed:', e);
        }
    }
</script>

<div class="webhook-container">
    <div class="webhook-header">
        <div class="title">
            <Webhook size={16} />
            <span>Webhook</span>
        </div>
        {#if revealed}
            <button class="btn-small" on:click={hide}>
                <EyeOff size={14} />
                Hide
            </button>
        {/if}
    </div>

    {#if error}
        <div class="error-msg">{error}</div>
    {/if}

    {#if !revealed}
        <button class="btn-small btn-reveal" on:click={reveal} disabled={loading}>
            <Eye size={14} />
            {loading ? 'Loading…' : 'Reveal webhook secret'}
        </button>
    {:else if webhook}
        <div class="webhook-field">
            <span class="field-label">GitHub URL</span>
            <div class="field-row">
                <code>{webhook.github_url}</code>
                <button class="btn-copy" on:click={() => copy(webhook.github_url, 'github')} title="Copy URL">
                    <Copy size={12} />
                </button>
            </div>
        </div>
        <div class="webhook-field">
            <span class="field-label">GitLab URL</span>
            <div class="field-row">
                <code>{webhook.gitlab_url}</code>
                <button class="btn-copy" on:click={() => copy(webhook.gitlab_url, 'gitlab')} title="Copy URL">
                    <Copy size={12} />
                </button>
            </div>
        </div>
        <div class="webhook-field">
            <span class="field-label">Generic URL</span>
            <div class="field-row">
                <code>{webhook.generic_url}</code>
                <button class="btn-copy" on:click={() => copy(webhook.generic_url, 'generic')} title="Copy URL">
                    <Copy size={12} />
                </button>
            </div>
        </div>
        <div class="webhook-field">
            <span class="field-label">Secret</span>
            <div class="field-row">
                <code class="secret">{webhook.webhook_secret}</code>
                <button class="btn-copy" on:click={() => copy(webhook.webhook_secret, 'secret')} title="Copy secret">
                    <Copy size={12} />
                </button>
            </div>
        </div>
        {#if copiedField}
            <div class="copied-msg">Copied to clipboard.</div>
        {/if}
    {/if}

    <div class="disclaimer">
        <ShieldAlert size={12} />
        <span>Configure this URL and secret in your SCM provider's webhook settings. Admin access required.</span>
    </div>
</div>

<style>
    .webhook-container {
        margin-top: 12px;
        padding-top: 12px;
        border-top: 1px solid var(--border);
    }
    .webhook-header {
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
        font-size: var(--font-xs);
        cursor: pointer;
        display: flex;
        align-items: center;
        gap: 4px;
    }
    .btn-small:hover {
        background: var(--surface2);
    }
    .btn-small:disabled {
        opacity: 0.5;
        cursor: not-allowed;
    }
    .btn-reveal {
        width: 100%;
        justify-content: center;
        padding: 6px 8px;
    }
    .webhook-field {
        margin-bottom: 8px;
    }
    .webhook-field:last-of-type {
        margin-bottom: 0;
    }
    .field-label {
        display: block;
        font-size: var(--font-xs);
        color: var(--muted);
        margin-bottom: 2px;
    }
    .field-row {
        display: flex;
        align-items: center;
        gap: 6px;
        background: var(--surface2);
        border-radius: 4px;
        padding: 4px 6px;
    }
    .field-row code {
        flex: 1;
        min-width: 0;
        overflow-x: auto;
        white-space: nowrap;
        font-size: 12px;
        color: var(--text);
    }
    .field-row code.secret {
        color: var(--accent);
    }
    .btn-copy {
        background: transparent;
        border: none;
        color: var(--muted);
        cursor: pointer;
        padding: 4px;
        border-radius: 4px;
        flex-shrink: 0;
        display: flex;
    }
    .btn-copy:hover {
        color: var(--accent);
        background: var(--surface);
    }
    .copied-msg {
        font-size: var(--font-xs);
        color: #3fb950;
        margin-top: 6px;
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
        font-size: var(--font-xs);
        color: var(--muted);
        margin-top: 12px;
        font-style: italic;
    }
</style>
