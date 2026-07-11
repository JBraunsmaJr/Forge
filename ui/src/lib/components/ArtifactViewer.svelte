<script lang="ts">
    import { onDestroy } from 'svelte';
    import { selectedJob, artifacts } from '../stores';
    import { authUrl, type Artifact } from '../api';

    let viewingArtifact: Artifact | null = null;
    let objectUrl: string | null = null;

    $: filteredArtifacts = $selectedJob 
        ? $artifacts.filter(a => a.job_id === $selectedJob.job_id)
        : $artifacts;

    $: if ($selectedJob) closeViewer();

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

    export let hideHeader = false;

    function viewArtifact(a: Artifact) {
        closeViewer();
        viewingArtifact = a;
        objectUrl = authUrl(a.download_url);
    }

    function closeViewer() {
        viewingArtifact = null;
        objectUrl = null;
    }

    onDestroy(() => {
        // no cleanup needed for direct URLs
    });
</script>

<div id="artifact-panel" class:visible={$artifacts.length > 0} class:no-header={hideHeader}>
    {#if !hideHeader}
    <div id="artifact-header">
        <h2>Artifacts {#if $selectedJob}(this job){:else}(this run){/if}</h2>
        <span id="artifact-count">
            {filteredArtifacts.length} file{filteredArtifacts.length !== 1 ? 's' : ''}
        </span>
    </div>
    {/if}
    <div id="artifact-body">
        {#if filteredArtifacts.length === 0}
            <div id="artifact-empty">
                {#if $selectedJob}
                    No artifacts for step "{$selectedJob.step_id}".
                {:else}
                    No artifacts for this run.
                {/if}
            </div>
        {:else}
            {#each filteredArtifacts as a}
                <div class="artifact-row">
                    <span class="artifact-name">
                        {a.name}{#if a.name !== a.filename} ({a.filename}){/if}
                    </span>
                    <span class="artifact-size">{formatBytes(a.size_bytes)}</span>
                    <div class="artifact-actions">
                        {#if isViewable(a)}
                            <button class="artifact-view" on:click={() => viewArtifact(a)}>
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
                    src={objectUrl}
                    sandbox="allow-same-origin allow-scripts allow-popups allow-forms"
                ></iframe>
            </div>
        </div>
    </div>
{/if}

<style>
    #artifact-panel { border-top: 1px solid var(--border); background: var(--surface);
        display: none; flex-direction: column; flex: 1; overflow: hidden; }
    #artifact-panel.no-header { border-top: none; }
    #artifact-panel.visible { display: flex; }
    #artifact-header { display: flex; align-items: center; gap: 10px; padding: 6px 16px;
        border-bottom: 1px solid var(--border); flex-shrink: 0; }
    #artifact-header h2 { font-size: 11px; font-weight: 600; letter-spacing: 1px;
        color: var(--muted); text-transform: uppercase; }
    #artifact-count { font-size: 11px; color: var(--muted); margin-left: auto; }
    #artifact-body { flex: 1; overflow-y: auto; padding: 8px 16px; font-size: 12px; font-family: var(--font-mono); }
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
