<script lang="ts">
    import { createEventDispatcher } from 'svelte';
    import { api, type Project, type Org, type ProjectHealth } from '../api';
    import {
        ArrowLeft, Briefcase, ExternalLink, Settings, Key, Webhook, Hash, ChartBar,
        RefreshCw, TriangleAlert, CircleX, Info, Trash2, Save,
    } from '@lucide/svelte';
    import SecretsManager from './SecretsManager.svelte';
    import WebhookPanel from './WebhookPanel.svelte';
    import FailureInsights from './FailureInsights.svelte';
    import BuildSettings from './BuildSettings.svelte';

    export let project: Project;
    export let orgs: Org[] = [];

    const dispatch = createEventDispatcher<{ back: void; updated: Project; deleted: string }>();

    type Tab = 'overview' | 'secrets' | 'webhook' | 'build' | 'insights';
    let activeTab: Tab = 'overview';

    let health: ProjectHealth | null | undefined = undefined; // undefined = not fetched yet
    let checkingHealth = false;

    let form = {
        name: project.name,
        repo_url: project.repo_url,
        pipeline_path: project.pipeline_path,
        cron: project.cron || '',
        scheduled_pipeline_path: project.scheduled_pipeline_path || '',
        scm_token: '',
        branch_filter: (project.branch_filter || []).join(', '),
        org_id: project.org_id || '',
    };
    let saving = false;
    let saveError = '';
    let savedMsg = '';
    let deleting = false;
    let confirmingDelete = false;

    function loadHealth() {
        api.projectHealth(project.id)
            .then((h) => { health = h; })
            .catch(() => { health = null; });
    }
    loadHealth();

    async function triggerHealthCheck() {
        if (checkingHealth) return;
        checkingHealth = true;
        try {
            const result = await api.triggerProjectHealth(project.id);
            if (result.ok && result.data) {
                health = result.data;
            } else if (result.status === 429) {
                alert('A health check ran too recently for this project — try again shortly.');
            } else if (result.status === 401 || result.status === 403) {
                alert('You need operator access to trigger a health check.');
            } else {
                alert('Health check failed. Please try again.');
            }
        } catch (e) {
            console.error('Failed to trigger health check:', e);
            alert('Health check failed. Please try again.');
        } finally {
            checkingHealth = false;
        }
    }

    function healthBand(score: number): 'good' | 'ok' | 'bad' {
        if (score >= 90) return 'good';
        if (score >= 80) return 'ok';
        return 'bad';
    }

    function relativeTime(iso: string): string {
        const diffMs = Date.now() - new Date(iso).getTime();
        const mins = Math.round(diffMs / 60000);
        if (mins < 1) return 'just now';
        if (mins < 60) return `${mins}m ago`;
        const hours = Math.round(mins / 60);
        if (hours < 24) return `${hours}h ago`;
        const days = Math.round(hours / 24);
        return `${days}d ago`;
    }

    async function save() {
        saving = true;
        saveError = '';
        savedMsg = '';
        try {
            const req: any = {
                name: form.name,
                repo_url: form.repo_url,
                pipeline_path: form.pipeline_path,
                cron: form.cron,
                scheduled_pipeline_path: form.scheduled_pipeline_path,
                branch_filter: form.branch_filter.split(',').map(s => s.trim()).filter(s => s),
                org_id: form.org_id,
            };
            if (form.scm_token) {
                req.scm_token = form.scm_token;
            }
            const success = await api.updateProject(project.id, req);
            if (success) {
                savedMsg = 'Saved.';
                project = { ...project, ...req };
                form.scm_token = '';
                dispatch('updated', project);
            } else {
                saveError = 'Failed to update project. Please check your inputs.';
            }
        } catch (e) {
            console.error('Failed to update project:', e);
            saveError = 'An error occurred while updating the project.';
        } finally {
            saving = false;
        }
    }

    async function confirmDelete() {
        deleting = true;
        try {
            await api.deleteProject(project.id);
            dispatch('deleted', project.id);
        } catch (e) {
            console.error('Failed to delete project:', e);
            alert('Failed to delete project. Please try again.');
            deleting = false;
            confirmingDelete = false;
        }
    }
</script>

<div class="detail-view">
    <div class="detail-header">
        <button class="btn-back" on:click={() => dispatch('back')}>
            <ArrowLeft size={16} />
            Projects
        </button>
    </div>

    <div class="detail-title-row">
        <div class="detail-icon"><Briefcase size={26} /></div>
        <div class="detail-title-text">
            <h1>{project.name}</h1>
            <div class="detail-subtitle">
                {#if project.repo_url}
                    <a href={project.repo_url} target="_blank" rel="noopener noreferrer">
                        {project.repo_url}
                        <ExternalLink size={12} />
                    </a>
                {/if}
                <span class="dot-sep">·</span>
                <span>Org: {orgs.find(o => o.id === project.org_id)?.name || 'None'}</span>
                <span class="dot-sep">·</span>
                <span>Created {new Date(project.created_at).toLocaleDateString()}</span>
            </div>
        </div>
        {#if health}
            {@const band = healthBand(health.score)}
            <div class="detail-health health-{band}" title="{health.findings.length} finding{health.findings.length === 1 ? '' : 's'}">
                <span class="health-score">{health.score}</span>
                <span class="health-label">health</span>
            </div>
        {/if}
    </div>

    <div class="tab-bar">
        <button class:active={activeTab === 'overview'} on:click={() => activeTab = 'overview'}>
            <Settings size={14} /> Overview
        </button>
        <button class:active={activeTab === 'secrets'} on:click={() => activeTab = 'secrets'}>
            <Key size={14} /> Secrets
        </button>
        <button class:active={activeTab === 'webhook'} on:click={() => activeTab = 'webhook'}>
            <Webhook size={14} /> Webhook
        </button>
        <button class:active={activeTab === 'build'} on:click={() => activeTab = 'build'}>
            <Hash size={14} /> Build Numbers
        </button>
        <button class:active={activeTab === 'insights'} on:click={() => activeTab = 'insights'}>
            <ChartBar size={14} /> Insights
        </button>
    </div>

    <div class="tab-content">
        {#if activeTab === 'overview'}
            <div class="overview-grid">
                <section class="panel">
                    <h2>Settings</h2>
                    <div class="form-grid">
                        <div class="form-group">
                            <label for="d-name">Project Name</label>
                            <input id="d-name" type="text" bind:value={form.name} />
                        </div>
                        <div class="form-group">
                            <label for="d-repo">Repository URL</label>
                            <input id="d-repo" type="text" bind:value={form.repo_url} />
                        </div>
                        <div class="form-group">
                            <label for="d-org">Organization</label>
                            <select id="d-org" bind:value={form.org_id}>
                                <option value="">None</option>
                                {#each orgs as org}
                                    <option value={org.id}>{org.name}</option>
                                {/each}
                            </select>
                        </div>
                        <div class="form-group">
                            <label for="d-path">Pipeline Path</label>
                            <input id="d-path" type="text" bind:value={form.pipeline_path} />
                        </div>
                        <div class="form-group">
                            <label for="d-token">SCM Token (leave empty to keep current)</label>
                            <input id="d-token" type="password" bind:value={form.scm_token} placeholder="••••••••" />
                        </div>
                        <div class="form-group full-width">
                            <label for="d-branches">Branch Filter (comma separated)</label>
                            <input id="d-branches" type="text" bind:value={form.branch_filter} placeholder="main, dev" />
                        </div>
                        <div class="form-group">
                            <label for="d-cron">Cron Schedule</label>
                            <input id="d-cron" type="text" bind:value={form.cron} placeholder="0 2 * * *" />
                        </div>
                        <div class="form-group">
                            <label for="d-scheduled-path">Scheduled Pipeline Path</label>
                            <input id="d-scheduled-path" type="text" bind:value={form.scheduled_pipeline_path} placeholder=".forge/nightly.yml" />
                        </div>
                    </div>
                    {#if saveError}<div class="error-msg">{saveError}</div>{/if}
                    {#if savedMsg}<div class="saved-msg">{savedMsg}</div>{/if}
                    <div class="form-actions">
                        <button class="btn-primary" on:click={save} disabled={saving || !form.name || !form.repo_url}>
                            <Save size={14} />
                            {saving ? 'Saving…' : 'Save Changes'}
                        </button>
                    </div>
                </section>

                <section class="panel">
                    <h2>Health</h2>
                    {#if health === undefined}
                        <p class="muted">Loading…</p>
                    {:else if health === null}
                        <button class="btn-secondary" disabled={checkingHealth} on:click={triggerHealthCheck}>
                            <span class:spin={checkingHealth}><RefreshCw size={14} /></span>
                            {checkingHealth ? 'Checking…' : 'Check health'}
                        </button>
                    {:else}
                        {@const critical = health.findings.filter(f => f.severity === 'critical')}
                        {@const warnings = health.findings.filter(f => f.severity === 'warning')}
                        {@const suggestions = health.findings.filter(f => f.severity === 'suggestion')}
                        <div class="health-summary-row">
                            <span>Checked {relativeTime(health.computed_at)}</span>
                            {#if health.previous_score !== undefined && health.previous_at !== undefined}
                                <span>· previous {health.previous_score} ({relativeTime(health.previous_at)})</span>
                            {/if}
                            {#if health.org_average !== undefined}
                                <span>· org avg {health.org_average.toFixed(0)} across {health.org_project_count} project{health.org_project_count === 1 ? '' : 's'}</span>
                            {/if}
                            <button class="btn-icon" title="Check health now" disabled={checkingHealth} on:click={triggerHealthCheck}>
                                <span class:spin={checkingHealth}><RefreshCw size={14} /></span>
                            </button>
                        </div>
                        {#if health.findings.length === 0}
                            <div class="health-panel-empty">✓ No issues found</div>
                        {:else}
                            {#if critical.length > 0}
                                <div class="health-group">
                                    <div class="health-group-title health-group-critical">CRITICAL</div>
                                    {#each critical as f}
                                        <div class="health-finding"><span class="color-bad"><CircleX size={14}/></span>{f.message}</div>
                                    {/each}
                                </div>
                            {/if}
                            {#if warnings.length > 0}
                                <div class="health-group">
                                    <div class="health-group-title health-group-warning">WARNINGS</div>
                                    {#each warnings as f}
                                        <div class="health-finding"><span class="color-warning"><TriangleAlert size={14}/></span>{f.message}</div>
                                    {/each}
                                </div>
                            {/if}
                            {#if suggestions.length > 0}
                                <div class="health-group">
                                    <div class="health-group-title health-group-suggestion">SUGGESTIONS</div>
                                    {#each suggestions as f}
                                        <div class="health-finding"><span class="color-info"><Info size={14}/></span>{f.message}</div>
                                    {/each}
                                </div>
                            {/if}
                        {/if}
                    {/if}
                </section>

                <section class="panel panel-danger">
                    <h2>Danger Zone</h2>
                    {#if !confirmingDelete}
                        <button class="btn-danger-outline" on:click={() => confirmingDelete = true}>
                            <Trash2 size={14} />
                            Delete Project
                        </button>
                    {:else}
                        <p>This permanently deletes <strong>{project.name}</strong> and its configuration. This cannot be undone.</p>
                        <div class="form-actions">
                            <button class="btn-secondary" on:click={() => confirmingDelete = false} disabled={deleting}>Cancel</button>
                            <button class="btn-danger-solid" on:click={confirmDelete} disabled={deleting}>
                                {deleting ? 'Deleting…' : 'Yes, delete it'}
                            </button>
                        </div>
                    {/if}
                </section>
            </div>
        {:else if activeTab === 'secrets'}
            <SecretsManager scope="project" id={project.id} />
        {:else if activeTab === 'webhook'}
            <WebhookPanel id={project.id} />
        {:else if activeTab === 'build'}
            <BuildSettings id={project.id} />
        {:else if activeTab === 'insights'}
            <FailureInsights id={project.id} />
        {/if}
    </div>
</div>

<style>
    .detail-view {
        display: flex;
        flex-direction: column;
        gap: 20px;
    }
    .detail-header {
        display: flex;
    }
    .btn-back {
        background: transparent;
        border: none;
        color: var(--muted);
        cursor: pointer;
        display: flex;
        align-items: center;
        gap: 6px;
        font-size: var(--font-sm);
        padding: 4px 0;
    }
    .btn-back:hover { color: var(--accent); }

    .detail-title-row {
        display: flex;
        align-items: center;
        gap: 16px;
    }
    .detail-icon {
        width: 52px;
        height: 52px;
        background: var(--surface2);
        border-radius: 10px;
        display: flex;
        align-items: center;
        justify-content: center;
        color: var(--blue);
        flex-shrink: 0;
    }
    .detail-title-text { flex: 1; min-width: 0; }
    .detail-title-text h1 {
        margin: 0 0 4px;
        font-size: var(--font-2xl);
        font-weight: 700;
    }
    .detail-subtitle {
        display: flex;
        align-items: center;
        gap: 8px;
        flex-wrap: wrap;
        font-size: var(--font-sm);
        color: var(--muted);
    }
    .detail-subtitle a {
        color: var(--muted);
        display: inline-flex;
        align-items: center;
        gap: 4px;
        text-decoration: none;
    }
    .detail-subtitle a:hover { color: var(--accent); }
    .dot-sep { opacity: 0.5; }

    .detail-health {
        display: flex;
        flex-direction: column;
        align-items: center;
        justify-content: center;
        width: 60px;
        height: 60px;
        border-radius: 10px;
        flex-shrink: 0;
    }
    .health-score { font-size: var(--font-xl); font-weight: 800; line-height: 1; }
    .health-label { font-size: var(--font-2xs); text-transform: uppercase; letter-spacing: 0.5px; opacity: 0.8; }
    .health-good { background: rgba(62, 207, 142, 0.12); color: var(--green); }
    .health-ok   { background: rgba(245, 158, 11, 0.12); color: var(--amber); }
    .health-bad  { background: rgba(248, 113, 113, 0.12); color: var(--red); }

    .tab-bar {
        display: flex;
        gap: 4px;
        border-bottom: 1px solid var(--border);
    }
    .tab-bar button {
        background: transparent;
        border: none;
        border-bottom: 2px solid transparent;
        color: var(--muted);
        padding: 10px 14px;
        cursor: pointer;
        display: flex;
        align-items: center;
        gap: 6px;
        font-size: var(--font-sm);
        font-weight: 500;
    }
    .tab-bar button:hover { color: var(--text); }
    .tab-bar button.active { color: var(--accent); border-bottom-color: var(--accent); }

    .tab-content { min-height: 200px; }

    .overview-grid {
        display: grid;
        grid-template-columns: 1fr 1fr;
        gap: 20px;
    }
    .panel {
        background: var(--surface);
        border: 1px solid var(--border);
        border-radius: 10px;
        padding: 20px;
    }
    .panel h2 {
        margin: 0 0 16px;
        font-size: var(--font-md);
        font-weight: 600;
    }
    .panel-danger {
        grid-column: span 2;
        border-color: rgba(248, 113, 113, 0.3);
    }
    @media (max-width: 900px) {
        .overview-grid { grid-template-columns: 1fr; }
        .panel-danger { grid-column: span 1; }
    }

    .form-grid {
        display: grid;
        grid-template-columns: 1fr 1fr;
        gap: 16px;
        margin-bottom: 16px;
    }
    .full-width { grid-column: span 2; }
    .form-group label {
        display: block;
        margin-bottom: 6px;
        font-size: var(--font-xs);
        color: var(--muted);
    }
    .form-group input, .form-group select { width: 100%; box-sizing: border-box; }
    .form-actions { display: flex; justify-content: flex-end; gap: 8px; }
    .error-msg { color: var(--red); font-size: var(--font-sm); margin-bottom: 12px; }
    .saved-msg { color: var(--green); font-size: var(--font-sm); margin-bottom: 12px; }
    .muted { color: var(--muted); font-size: var(--font-sm); }

    .health-summary-row {
        display: flex;
        align-items: center;
        gap: 6px;
        flex-wrap: wrap;
        font-size: var(--font-xs);
        color: var(--muted);
        margin-bottom: 14px;
    }
    .btn-icon {
        background: transparent; border: none; color: var(--muted); cursor: pointer;
        padding: 4px; border-radius: 4px; display: flex; align-items: center; margin-left: auto;
    }
    .btn-icon:hover { background: var(--surface2); color: var(--accent); }
    .health-panel-empty { color: var(--green); font-size: var(--font-sm); }
    .health-group { margin-bottom: 14px; }
    .health-group:last-child { margin-bottom: 0; }
    .health-group-title {
        font-size: var(--font-2xs); font-weight: 700; letter-spacing: 0.5px;
        margin-bottom: 8px; text-transform: uppercase;
    }
    .health-group-critical { color: var(--red); }
    .health-group-warning { color: var(--amber); }
    .health-group-suggestion { color: var(--blue); }
    .health-finding {
        display: flex; align-items: flex-start; gap: 8px;
        font-size: var(--font-sm); color: var(--text); margin-bottom: 8px; line-height: 1.4;
    }
    .color-bad { color: var(--red); flex-shrink: 0; margin-top: 1px; }
    .color-warning { color: var(--amber); flex-shrink: 0; margin-top: 1px; }
    .color-info { color: var(--blue); flex-shrink: 0; margin-top: 1px; }

    .btn-danger-outline {
        background: transparent;
        border: 1px solid var(--red);
        color: var(--red);
        padding: 8px 16px;
        border-radius: 6px;
        display: flex;
        align-items: center;
        gap: 8px;
        cursor: pointer;
        font-size: var(--font-sm);
    }
    .btn-danger-outline:hover { background: rgba(248, 113, 113, 0.1); }
    .btn-danger-solid {
        background: var(--red);
        color: #1a0a0a;
        border: none;
        padding: 8px 16px;
        border-radius: 6px;
        font-weight: 600;
        cursor: pointer;
        font-size: var(--font-sm);
    }
    .panel-danger p { font-size: var(--font-sm); color: var(--muted); margin-bottom: 14px; line-height: 1.5; }
</style>
