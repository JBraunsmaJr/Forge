<script lang="ts">
    import { activeRun } from '../stores';
    import { api, authUrl, type Artifact } from '../api';

    let artifacts: Artifact[] = [];
    let loading = false;

    async function handleRunChange(runID: string | undefined) {
        if (!runID) {
            artifacts = [];
            return;
        }
        loading = true;
        artifacts = await api.runArtifacts(runID);
        loading = false;
    }

    $: handleRunChange($activeRun?.run_id);

    function formatBytes(bytes: number) {
        if (bytes === 0) return '0 B';
        const k = 1024, sizes = ['B','KB','MB','GB'];
        const i = Math.floor(Math.log(bytes) / Math.log(k));
        return parseFloat((bytes / Math.pow(k, i)).toFixed(1)) + ' ' + sizes[i];
    }
</script>

<div id="artifact-panel" class:visible={artifacts.length > 0}>
    <div id="artifact-header">
        <h2>Artifacts</h2>
        <span id="artifact-count">
            {artifacts.length} file{artifacts.length !== 1 ? 's' : ''}
        </span>
    </div>
    <div id="artifact-body">
        {#if loading}
            <div id="artifact-empty">Loading...</div>
        {:else if artifacts.length === 0}
            <div id="artifact-empty">No artifacts for this run.</div>
        {:else}
            {#each artifacts as a}
                <div class="artifact-row">
                    <span class="artifact-name">
                        {a.name}{#if a.name !== a.filename} ({a.filename}){/if}
                    </span>
                    <span class="artifact-size">{formatBytes(a.size_bytes)}</span>
                    <a 
                        class="artifact-dl" 
                        href={authUrl(a.download_url)} 
                        download={a.filename}
                    >
                        ↓ download
                    </a>
                </div>
            {/each}
        {/if}
    </div>
</div>

<style>
    #artifact-panel { border-top: 1px solid var(--border); background: var(--surface);
        display: none; flex-direction: column; }
    #artifact-panel.visible { display: flex; }
    #artifact-header { display: flex; align-items: center; gap: 10px; padding: 6px 16px;
        border-bottom: 1px solid var(--border); flex-shrink: 0; }
    #artifact-header h2 { font-size: 11px; font-weight: 600; letter-spacing: 1px;
        color: var(--muted); text-transform: uppercase; }
    #artifact-count { font-size: 11px; color: var(--muted); margin-left: auto; }
    #artifact-body { padding: 8px 16px; font-size: 12px; font-family: var(--font-mono); }
    #artifact-empty { color: var(--muted); font-size: 12px; padding: 8px 0; }
    .artifact-row { display: flex; align-items: center; gap: 12px; padding: 4px 0;
        border-bottom: 1px solid var(--border); }
    .artifact-row:last-child { border-bottom: none; }
    .artifact-name { color: var(--text); flex: 1; }
    .artifact-size { color: var(--muted); font-size: 11px; }
    .artifact-dl { color: var(--accent); text-decoration: none; font-size: 11px; }
    .artifact-dl:hover { text-decoration: underline; }
</style>
