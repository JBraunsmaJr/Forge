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
                Enter your API token. Create one with:<br>
                <code>forge token create dev</code>
            </p>
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
        background: #16162a;
        border: 1px solid #7c6af7;
        border-radius: 10px;
        padding: 32px 36px;
        width: 440px;
        max-width: 92vw;
    }
    h2 { margin: 0 0 6px; color: #e0e0e0; font-size: 16px; font-weight: 600; }
    p { margin: 0 0 20px; color: #777; font-size: 13px; line-height: 1.6; }
    code { color: #a78bfa; background: #0d0d1a; padding: 2px 6px; border-radius: 3px; }
    input {
        width: 100%;
        box-sizing: border-box;
        background: #0d0d1a;
        border: 1px solid #333;
        border-radius: 5px;
        padding: 10px 12px;
        color: #e0e0e0;
        font-family: monospace;
        font-size: 13px;
        outline: none;
        margin-bottom: 14px;
    }
    .actions { display: flex; justify-content: flex-end; gap: 8px; }
    button {
        background: #7c6af7;
        color: #fff;
        border: none;
        border-radius: 5px;
        padding: 8px 22px;
        cursor: pointer;
        font-size: 13px;
        font-weight: 600;
    }
</style>
