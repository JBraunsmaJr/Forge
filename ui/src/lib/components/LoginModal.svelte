<script lang="ts">
    import { authRequired } from '../stores';
    import { setToken } from '../api';

    let token = '';

    function submit() {
        if (!token.trim()) return;
        setToken(token.trim());
        authRequired.set(false);
        location.reload();
    }

    function handleKeydown(e: KeyboardEvent) {
        if (e.key === 'Enter') submit();
    }
</script>

{#if $authRequired}
    <div class="modal-overlay">
        <div class="modal">
            <h2>🔐 Forge — Authentication Required</h2>
            <p>
                Sign in with your identity provider or use a personal API token.
            </p>

            <div class="sso-options">
                <a href="/api/v1/auth/login/github" class="sso-btn github">
                    Continue with GitHub
                </a>
                <a href="/api/v1/auth/login/gitlab" class="sso-btn gitlab">
                    Continue with GitLab
                </a>
            </div>

            <div class="divider">
                <span>or use token</span>
            </div>

            <input 
                type="password" 
                placeholder="fgt_…" 
                bind:value={token}
                on:keydown={handleKeydown}
                use:autofocus
            />
            <div class="actions">
                <button on:click={submit}>Connect</button>
            </div>
        </div>
    </div>
{/if}

<script lang="ts" context="module">
    function autofocus(el: HTMLInputElement) {
        setTimeout(() => el.focus(), 50);
    }
</script>

<style>
    .modal-overlay {
        position: fixed;
        inset: 0;
        background: rgba(0,0,0,.75);
        display: flex;
        align-items: center;
        justify-content: center;
        z-index: 9999;
        font-family: system-ui, sans-serif;
    }
    .modal {
        background: var(--surface);
        border: 1px solid var(--accent);
        border-radius: 10px;
        padding: 32px 36px;
        width: 440px;
        max-width: 92vw;
    }
    h2 { margin: 0 0 6px; color: var(--text); font-size: 16px; font-weight: 600; }
    p { margin: 0 0 24px; color: var(--muted); font-size: 13px; line-height: 1.6; }

    .sso-options {
        display: flex;
        flex-direction: column;
        gap: 12px;
        margin-bottom: 24px;
    }
    .sso-btn {
        display: flex;
        align-items: center;
        justify-content: center;
        gap: 10px;
        color: white;
        text-decoration: none;
        padding: 10px;
        border-radius: 6px;
        font-size: 14px;
        font-weight: 600;
        transition: opacity .2s;
    }
    .sso-btn:hover { opacity: 0.9; }
    .github { background: #24292e; }
    .gitlab { background: #e24329; }

    .divider {
        display: flex;
        align-items: center;
        text-align: center;
        margin-bottom: 24px;
        color: var(--muted);
        font-size: var(--font-xs);
        text-transform: uppercase;
        letter-spacing: 0.05em;
    }
    .divider::before, .divider::after {
        content: '';
        flex: 1;
        border-bottom: 1px solid var(--border);
    }
    .divider span {
        padding: 0 10px;
    }

    input {
        width: 100%;
        box-sizing: border-box;
        background: var(--bg);
        border: 1px solid var(--border);
        border-radius: 6px;
        padding: 10px 12px;
        color: var(--text);
        font-family: var(--font-mono);
        font-size: 13px;
        outline: none;
        margin-bottom: 14px;
    }
    .actions { display: flex; justify-content: flex-end; gap: 8px; }
    button {
        background: var(--accent);
        color: #fff;
        border: none;
        border-radius: 6px;
        padding: 8px 22px;
        cursor: pointer;
        font-size: 13px;
        font-weight: 600;
    }
</style>
