<script lang="ts">
    import { activeRun, selectedJob } from '../stores';
    import { api, authUrl, type Artifact } from '../api';

    let artifacts: Artifact[] = [];
    let loading = false;
    let viewingArtifact: Artifact | null = null;

    $: hasJobArtifacts = $selectedJob && artifacts.some(a => a.job_id === $selectedJob.job_id);
    $: filteredArtifacts = hasJobArtifacts 
        ? artifacts.filter(a => a.job_id === $selectedJob.job_id)
        : artifacts;

    async function handleRunChange(runID: string | undefined) {
        if (!runID) {
            artifacts = [];
            viewingArtifact = null;
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

    function isViewable(a: Artifact) {
        const ext = a.filename.split('.').pop()?.toLowerCase();
        const viewableExts = ['html', 'pdf', 'txt', 'log', 'png', 'jpg', 'jpeg'];
        if (viewableExts.includes(ext || '')) return true;
        
        if (a.content_type) {
            return a.content_type.startsWith('text/') || 
                   a.content_type === 'application/pdf' || 
                   a.content_type.startsWith('image/');
        }
        return false;
    }

    function closeViewer() {
        viewingArtifact = null;
    }
</script>

<div id="artifact-panel" class:visible={artifacts.length > 0}>
    <div id="artifact-header">
        <h2>Artifacts {#if hasJobArtifacts}(this job){:else}(this run){/if}</h2>
        <span id="artifact-count">
            {filteredArtifacts.length} file{filteredArtifacts.length !== 1 ? 's' : ''}
        </span>
    </div>
    <div id="artifact-body">
        {#if loading}
            <div id="artifact-empty">Loading...</div>
        {:else if filteredArtifacts.length === 0}
            <div id="artifact-empty">No artifacts for this run.</div>
        {:else}
            {#each filteredArtifacts as a}
                <div class="artifact-row">
                    <span class="artifact-name">
                        {a.name}{#if a.name !== a.filename} ({a.filename}){/if}
                    </span>
                    <span class="artifact-size">{formatBytes(a.size_bytes)}</span>
                    <div class="artifact-actions">
                        {#if isViewable(a)}
                            <button class="artifact-view" on:click={() => viewingArtifact = a}>
                                view
                            </button>
                        {/if}
                        <a 
                            class="artifact-dl" 
                            href={authUrl(a.download_url)} 
                            download={a.filename}
                        >
                            ↓ download
                        </a>
                    </div>
                </div>
            {/each}
        {/if}
    </div>
</div>

{#if viewingArtifact}
    <div class="artifact-viewer-modal" on:click={closeViewer}>
        <div class="viewer-content" on:click|stopPropagation>
            <div class="viewer-header">
                <h3>{viewingArtifact.name}</h3>
                <button on:click={closeViewer}>✕</button>
            </div>
            <div class="viewer-body">
                <iframe 
                    title={viewingArtifact.name}
                    src={authUrl(viewingArtifact.download_url)}
                ></iframe>
            </div>
        </div>
    </div>
{/if}

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
    .artifact-actions { display: flex; align-items: center; gap: 8px; }
    .artifact-view { background: none; border: none; color: var(--accent); cursor: pointer; font-size: 11px; padding: 0; }
    .artifact-view:hover { text-decoration: underline; }
    .artifact-dl { color: var(--accent); text-decoration: none; font-size: 11px; }
    .artifact-dl:hover { text-decoration: underline; }

    .artifact-viewer-modal {
        position: fixed; top: 0; left: 0; width: 100%; height: 100%;
        background: rgba(0,0,0,0.7); z-index: 1000;
        display: flex; align-items: center; justify-content: center;
        padding: 40px;
    }
    .viewer-content {
        background: var(--surface); border: 1px solid var(--border);
        width: 100%; max-width: 1200px; height: 100%; display: flex; flex-direction: column;
        border-radius: 6px; overflow: hidden; box-shadow: 0 10px 30px rgba(0,0,0,0.5);
    }
    .viewer-header {
        display: flex; align-items: center; justify-content: space-between;
        padding: 12px 20px; border-bottom: 1px solid var(--border); background: var(--surface2);
    }
    .viewer-header h3 { font-size: 14px; margin: 0; color: var(--text); }
    .viewer-header button { background: none; border: none; color: var(--muted); cursor: pointer; font-size: 20px; line-height: 1; }
    .viewer-header button:hover { color: var(--text); }
    .viewer-body { flex: 1; position: relative; background: white; }
    iframe { width: 100%; height: 100%; border: none; }
</style>
