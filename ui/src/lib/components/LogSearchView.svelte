<script lang="ts">
    import { api } from '../api';
    import { Search, Loader2, ExternalLink } from '@lucide/svelte';
    import { navigateToRunID } from '../stores';

    let query = '';
    let results: any[] = [];
    let loading = false;
    let limit = 50;

    async function handleSearch() {
        if (!query.trim()) return;
        loading = true;
        try {
            results = await api.searchLogs(query, limit);
        } catch (err) {
            console.error(err);
        } finally {
            loading = false;
        }
    }

    function handleKeydown(e: KeyboardEvent) {
        if (e.key === 'Enter') handleSearch();
    }

    function formatDate(iso: string) {
        return new Date(iso).toLocaleString();
    }
</script>

<div class="log-search">
    <div class="search-header">
        <h1>🔍 Cross-Run Log Search</h1>
        <p>Search through logs across all historical pipeline runs.</p>
    </div>

    <div class="search-bar">
        <div class="input-wrapper">
            <Search size={18} class="search-icon" />
            <input 
                type="text" 
                placeholder="Search for errors, keywords, or specific log lines..." 
                bind:value={query}
                on:keydown={handleKeydown}
            />
        </div>
        <button on:click={handleSearch} disabled={loading || !query.trim()}>
            {#if loading}
                <Loader2 size={16} class="spin" />
            {:else}
                Search
            {/if}
        </button>
    </div>

    <div class="results-area">
        {#if loading}
            <div class="status-msg">Searching logs...</div>
        {:else if results.length > 0}
            <div class="results-list">
                {#each results as res}
                    <div class="log-item">
                        <div class="log-meta">
                            <span class="timestamp">{formatDate(res.timestamp)}</span>
                            <span class="context">{res.run_name || 'unknown run'} / {res.job_name || 'unknown job'}</span>
                            <button class="link-btn" on:click={() => navigateToRunID.set(res.run_id)} title="View Run">
                                <ExternalLink size={12} />
                            </button>
                        </div>
                        <div class="log-line">
                            <code>{res.message}</code>
                        </div>
                    </div>
                {/each}
            </div>
        {:else if query && !loading}
            <div class="status-msg">No results found for "{query}".</div>
        {:else}
            <div class="empty-state">
                <p>Enter a search term to find logs across all runs.</p>
                <div class="tips">
                    <strong>Search Tips:</strong>
                    <ul>
                        <li>Search for "error" or "failed" to find common failures.</li>
                        <li>Search for a specific step name to see its history.</li>
                        <li>Use unique identifiers like commit SHAs or artifact names.</li>
                    </ul>
                </div>
            </div>
        {/if}
    </div>
</div>

<style>
    .log-search {
        padding: 24px;
        max-width: 1000px;
        margin: 0 auto;
        display: flex;
        flex-direction: column;
        height: 100%;
        box-sizing: border-box;
    }
    .search-header h1 {
        margin: 0 0 8px;
        font-size: 20px;
        color: var(--text);
    }
    .search-header p {
        margin: 0 0 24px;
        color: var(--muted);
        font-size: 14px;
    }
    .search-bar {
        display: flex;
        gap: 12px;
        margin-bottom: 32px;
    }
    .input-wrapper {
        flex: 1;
        position: relative;
    }
    .input-wrapper :global(.search-icon) {
        position: absolute;
        left: 12px;
        top: 50%;
        transform: translateY(-50%);
        color: var(--muted);
        pointer-events: none;
    }
    input {
        width: 100%;
        background: var(--surface);
        border: 1px solid var(--border);
        border-radius: 8px;
        padding: 12px 12px 12px 40px;
        color: var(--text);
        font-size: 14px;
        outline: none;
        box-sizing: border-box;
    }
    input:focus {
        border-color: var(--accent);
        box-shadow: 0 0 0 2px rgba(var(--accent-rgb), 0.1);
    }
    button {
        background: var(--accent);
        color: white;
        border: none;
        border-radius: 8px;
        padding: 0 24px;
        font-weight: 600;
        cursor: pointer;
        display: flex;
        align-items: center;
        gap: 8px;
        transition: opacity .2s;
    }
    button:disabled {
        opacity: 0.5;
        cursor: not-allowed;
    }
    .results-area {
        flex: 1;
        overflow-y: auto;
        background: var(--bg);
        border: 1px solid var(--border);
        border-radius: 8px;
        padding: 0;
    }
    .results-list {
        display: flex;
        flex-direction: column;
    }
    .log-item {
        padding: 12px 16px;
        border-bottom: 1px solid var(--border);
    }
    .log-item:hover {
        background: var(--surface);
    }
    .log-meta {
        display: flex;
        align-items: center;
        gap: 12px;
        margin-bottom: 6px;
        font-size: var(--font-xs);
    }
    .timestamp {
        color: var(--muted);
        font-family: var(--font-mono);
    }
    .context {
        color: var(--accent);
        font-weight: 600;
    }
    .link-btn {
        background: none;
        border: none;
        padding: 4px;
        color: var(--muted);
        cursor: pointer;
        display: flex;
        align-items: center;
        justify-content: center;
        border-radius: 4px;
        transition: all .2s;
    }
    .link-btn:hover {
        background: var(--surface2);
        color: var(--text);
    }
    .log-line {
        font-family: var(--font-mono);
        font-size: 13px;
        word-break: break-all;
        color: var(--text);
    }
    .status-msg {
        padding: 40px;
        text-align: center;
        color: var(--muted);
    }
    .empty-state {
        padding: 60px 40px;
        text-align: center;
        color: var(--muted);
    }
    .tips {
        margin-top: 32px;
        text-align: left;
        display: inline-block;
        max-width: 400px;
        background: var(--surface);
        padding: 20px;
        border-radius: 8px;
        font-size: 13px;
    }
    .tips ul {
        margin: 10px 0 0;
        padding-left: 20px;
    }
    .tips li {
        margin-bottom: 4px;
    }
    /* .spin is defined globally in app.css (it's a real stylesheet,
       not Svelte-scoped, so it already reaches this button's icon
       without needing :global() here) — no local copy needed. */
</style>
