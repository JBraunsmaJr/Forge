<script lang="ts">
    import { api, type BuildFormatInfo } from '../api';
    import { previewBuildNumber } from '../buildNumberPreview';
    import { Hash, Save, TriangleAlert } from '@lucide/svelte';

    export let id: string; // project ID

    let pipelineName = '';
    let info: BuildFormatInfo | null = null;
    let loading = false;
    let loaded = false;

    let format = '';
    let major = 0;
    let minor = 0;
    let tagFilter = '';

    let savingFormat = false;
    let savingVersion = false;
    let savingFilter = false;
    let formatError = '';
    let versionError = '';
    let filterError = '';
    let savedMsg = '';

    async function load() {
        if (!pipelineName.trim()) return;
        loading = true;
        formatError = '';
        versionError = '';
        filterError = '';
        savedMsg = '';
        try {
            info = await api.getBuildFormat(id, pipelineName.trim());
            if (info) {
                format = info.format;
                major = info.major;
                minor = info.minor;
                tagFilter = info.version_tag_filter || '';
            }
            loaded = true;
        } catch (e) {
            console.error('Failed to load build format:', e);
        } finally {
            loading = false;
        }
    }

    // Instant, offline preview as the format is edited — the
    // authoritative check (unknown/malformed tokens) happens
    // server-side on save; this is a hint, not a gate.
    $: preview = previewBuildNumber(format, major, minor, 1);

    async function saveFormat() {
        savingFormat = true;
        formatError = '';
        savedMsg = '';
        try {
            const res = await api.setBuildFormat(id, pipelineName.trim(), format);
            if (res.ok) {
                savedMsg = 'Format saved.';
                await load();
            } else {
                formatError = res.error || 'Failed to save format.';
            }
        } finally {
            savingFormat = false;
        }
    }

    async function saveVersion() {
        savingVersion = true;
        versionError = '';
        savedMsg = '';
        try {
            const res = await api.setVersion(id, pipelineName.trim(), major, minor);
            if (res.ok) {
                savedMsg = 'Version saved.';
                await load();
            } else {
                versionError = res.error || 'Failed to save version.';
            }
        } finally {
            savingVersion = false;
        }
    }

    async function saveFilter() {
        savingFilter = true;
        filterError = '';
        savedMsg = '';
        try {
            const res = await api.setVersionTagFilter(id, pipelineName.trim(), tagFilter);
            if (res.ok) {
                savedMsg = 'Tag filter saved.';
                await load();
            } else {
                filterError = res.error || 'Failed to save tag filter.';
            }
        } finally {
            savingFilter = false;
        }
    }
</script>

<div class="build-container">
    <div class="build-header">
        <div class="title">
            <Hash size={16} />
            <span>Build Numbers</span>
        </div>
    </div>

    <div class="field-label">Pipeline name</div>
    <div class="field-row">
        <input
            type="text"
            bind:value={pipelineName}
            placeholder="e.g. ci"
            on:keydown={(e) => e.key === 'Enter' && load()}
        />
        <button class="btn-small" on:click={load} disabled={!pipelineName.trim() || loading}>
            {loading ? 'Loading…' : 'Load'}
        </button>
    </div>
    <p class="hint">Build format and version are configured per pipeline within this project — enter the pipeline's stable name (as it appears in run history) to load or create its settings.</p>

    {#if loaded}
        <div class="section">
            <div class="field-label">Format string</div>
            <input type="text" bind:value={format} placeholder="%year%-%month%.%counter%" class="mono" />
            <div class="preview" class:preview-error={!!preview.error}>
                {#if preview.error}
                    <TriangleAlert size={12} />
                    <span>{preview.error}</span>
                {:else}
                    <span class="preview-label">Preview:</span>
                    <code>{preview.text}</code>
                {/if}
            </div>
            {#if formatError}<div class="error-msg">{formatError}</div>{/if}
            <button class="btn-small btn-save" on:click={saveFormat} disabled={savingFormat || !format.trim()}>
                <Save size={12} />
                {savingFormat ? 'Saving…' : 'Save format'}
            </button>
        </div>

        <div class="section">
            <div class="field-label">
                Major / minor version
                {#if info?.version_source === 'manual'}
                    <span class="source-badge">manually set{info.version_set_by ? ` by ${info.version_set_by}` : ''}</span>
                {:else if info?.version_source?.startsWith('tag:')}
                    <span class="source-badge">tag-derived from {info.version_source.slice(4)}</span>
                {:else}
                    <span class="source-badge source-unset">never set</span>
                {/if}
            </div>
            <div class="field-row">
                <input type="number" bind:value={major} min="0" class="num-input" />
                <span class="dot-sep">.</span>
                <input type="number" bind:value={minor} min="0" class="num-input" />
                <button class="btn-small btn-save" on:click={saveVersion} disabled={savingVersion}>
                    <Save size={12} />
                    {savingVersion ? 'Saving…' : 'Save'}
                </button>
            </div>
            {#if versionError}<div class="error-msg">{versionError}</div>{/if}
            <p class="hint">A later matching tag push can still override a manually-set version, and a manual set can override a tag-derived one — whichever happens most recently wins.</p>
        </div>

        <div class="section">
            <div class="field-label">Tag-derived version branch filter</div>
            <div class="field-row">
                <input type="text" bind:value={tagFilter} placeholder="(empty = project's default branch)" />
                <button class="btn-small btn-save" on:click={saveFilter} disabled={savingFilter}>
                    <Save size={12} />
                    {savingFilter ? 'Saving…' : 'Save'}
                </button>
            </div>
            {#if filterError}<div class="error-msg">{filterError}</div>{/if}
            <p class="hint">Restricts which branch a pushed tag must target for it to update the version above — prevents a tag on a stale or feature branch from changing mainline builds.</p>
        </div>

        {#if savedMsg}
            <div class="saved-msg">{savedMsg}</div>
        {/if}
    {/if}
</div>

<style>
    .build-container {
        margin-top: 12px;
        padding-top: 12px;
        border-top: 1px solid var(--border);
    }
    .build-header {
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
    .section {
        margin-top: 14px;
        padding-top: 12px;
        border-top: 1px dashed var(--border);
    }
    .field-label {
        display: flex;
        align-items: center;
        gap: 8px;
        font-size: 11px;
        color: var(--muted);
        margin-bottom: 6px;
    }
    .field-row {
        display: flex;
        align-items: center;
        gap: 6px;
    }
    .build-container input[type="text"] {
        flex: 1;
        min-width: 0;
        width: 100%;
        background: var(--surface2);
        border: 1px solid var(--border);
        border-radius: 4px;
        color: var(--text);
        font-size: 12px;
        padding: 6px 8px;
        box-sizing: border-box;
    }
    input.mono {
        font-family: var(--font-mono);
    }
    .num-input {
        flex: 0 0 70px;
        text-align: center;
    }
    .dot-sep {
        color: var(--muted);
        font-weight: 700;
    }
    .hint {
        font-size: 11px;
        color: var(--muted);
        margin: 6px 0 0;
        line-height: 1.5;
    }
    .preview {
        display: flex;
        align-items: center;
        gap: 6px;
        font-size: 12px;
        color: var(--muted);
        margin-top: 6px;
    }
    .preview code {
        color: var(--accent);
        font-size: 12px;
    }
    .preview-error {
        color: var(--amber, #d29922);
    }
    .source-badge {
        font-size: 10px;
        font-weight: 600;
        padding: 1px 6px;
        border-radius: 8px;
        background: var(--surface2);
        color: var(--accent);
    }
    .source-unset {
        color: var(--muted);
    }
    .btn-small {
        background: transparent;
        border: 1px solid var(--border);
        color: var(--text);
        padding: 4px 8px;
        border-radius: 4px;
        font-size: 11px;
        cursor: pointer;
        display: flex;
        align-items: center;
        gap: 4px;
        white-space: nowrap;
    }
    .btn-small:hover:not(:disabled) {
        background: var(--surface2);
    }
    .btn-small:disabled {
        opacity: 0.5;
        cursor: not-allowed;
    }
    .btn-save {
        margin-top: 8px;
    }
    .error-msg {
        color: var(--red);
        font-size: 11px;
        margin-top: 6px;
    }
    .saved-msg {
        color: #3fb950;
        font-size: 11px;
        margin-top: 10px;
    }
</style>
