<script lang="ts">
    import { onMount } from 'svelte';
    import { api, type Project, type Org, type ProjectHealth } from '../api';
    import { currentView } from '../stores';
    import { Plus, Briefcase, ExternalLink, Play, RefreshCw, ArrowRight } from '@lucide/svelte';
    import ProjectDetailView from './ProjectDetailView.svelte';

    let projects: Project[] = [];
    let orgs: Org[] = [];
    let loading = true;
    let error = '';
    let healthByProject: Record<string, ProjectHealth | null> = {};
    let checkingHealth: Record<string, boolean> = {};
    let selectedOrgId = '';
    // The one project currently open in the detail view — the card
    // grid below is an overview only; anything beyond a quick glance
    // and the trigger action lives there instead, not stacked as
    // inline toggles on the card itself.
    let selectedProject: Project | null = null;

    let showCreate = false;
    let triggeringId: string | null = null;
    let triggering = false;
    let triggerBranch = 'main';
    let branches: string[] = [];
    let loadingBranches = false;
    
    let newProject = {
        name: '',
        repo_url: '',
        org_id: '',
        pipeline_path: '',
        scm_token: ''
    };

    async function loadData() {
        loading = true;
        error = '';
        try {
            const [orgsRes, projectsRes] = await Promise.all([
                api.listOrgs(),
                api.listProjects(selectedOrgId || undefined)
            ]);
            orgs = orgsRes;
            projects = projectsRes;
        } catch (e) {
            console.error("Failed to load projects data:", e);
            error = 'Failed to load projects. Please try again.';
        } finally {
            loading = false;
        }
        loadHealth();
    }

    function loadHealth() {
        for (const project of projects) {
            api.projectHealth(project.id)
                .then((h) => { healthByProject = { ...healthByProject, [project.id]: h }; })
                .catch(() => { healthByProject = { ...healthByProject, [project.id]: null }; });
        }
    }

    function healthBand(score: number): 'good' | 'ok' | 'bad' {
        if (score >= 90) return 'good';
        if (score >= 80) return 'ok';
        return 'bad';
    }

    async function triggerHealthCheck(projectId: string) {
        if (checkingHealth[projectId]) return; // already in flight
        checkingHealth = { ...checkingHealth, [projectId]: true };
        try {
            const result = await api.triggerProjectHealth(projectId);
            if (result.ok && result.data) {
                healthByProject = { ...healthByProject, [projectId]: result.data };
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
            checkingHealth = { ...checkingHealth, [projectId]: false };
        }
    }

    async function refreshProjects() {
        try {
            projects = await api.listProjects(selectedOrgId || undefined);
            loadHealth();
        } catch (e) {
            console.error("Failed to refresh projects:", e);
        }
    }

    async function createProject() {
        if (!newProject.name || !newProject.repo_url) return;
        error = '';
        try {
            const proj = await api.createProject({
                ...newProject,
                org_id: newProject.org_id || undefined
            });
            if (proj) {
                newProject = { name: '', repo_url: '', org_id: '', pipeline_path: '', scm_token: '' };
                showCreate = false;
                await refreshProjects();
            } else {
                error = 'Failed to create project. Check your inputs.';
            }
        } catch (e) {
            console.error("Failed to create project:", e);
            error = 'An error occurred while creating the project.';
        }
    }

    async function trigger(id: string) {
        if (!triggerBranch || triggering) return;
        triggering = true;
        error = '';
        try {
            const res = await api.triggerProject(id, triggerBranch);
            triggeringId = null;
            currentView.set('runs');
        } catch (e: any) {
            console.error("Trigger failed:", e);
            error = e.message || 'Failed to trigger pipeline. Make sure the branch exists and contains a valid pipeline file.';
        } finally {
            triggering = false;
        }
    }

    async function openTrigger(project: Project) {
        triggeringId = project.id;
        triggerBranch = 'main';
        branches = [];
        loadingBranches = true;
        error = '';
        try {
            const res = await api.listBranches(project.id);
            if (res && triggeringId === project.id) {
                branches = res.branches;
                triggerBranch = res.default;
            }
        } catch (e) {
            console.error("Failed to load branches:", e);
        } finally {
            if (triggeringId === project.id) {
                loadingBranches = false;
            }
        }
    }

    function cancelTrigger() {
        triggeringId = null;
        loadingBranches = false;
        triggering = false;
    }

    onMount(loadData);
</script>

{#if selectedProject}
    <div class="view-container">
        <ProjectDetailView
            project={selectedProject}
            {orgs}
            on:back={() => selectedProject = null}
            on:updated={(e) => {
                projects = projects.map(p => p.id === e.detail.id ? e.detail : p);
                selectedProject = e.detail;
            }}
            on:deleted={(e) => {
                projects = projects.filter(p => p.id !== e.detail);
                selectedProject = null;
            }}
        />
    </div>
{:else}
<div class="view-container">
    <div class="view-header">
        <h1>Projects</h1>
        <div class="header-actions">
            <select bind:value={selectedOrgId} on:change={refreshProjects}>
                <option value="">All Organizations</option>
                {#each orgs as org}
                    <option value={org.id}>{org.name}</option>
                {/each}
            </select>
            <button class="btn-primary" on:click={() => { showCreate = !showCreate; error = ''; }}>
                <Plus size={16} />
                New Project
            </button>
        </div>
    </div>

    {#if error}
        <div class="error-banner">
            {error}
            <button on:click={() => error = ''}>&times;</button>
        </div>
    {/if}

    {#if showCreate}
        <div class="create-card card">
            <h3>Register New Project</h3>
            <div class="form-grid">
                <div class="form-group">
                    <label for="p-name">Project Name</label>
                    <input id="p-name" type="text" bind:value={newProject.name} placeholder="my-awesome-app" />
                </div>
                <div class="form-group">
                    <label for="p-repo">Repository URL</label>
                    <input id="p-repo" type="text" bind:value={newProject.repo_url} placeholder="https://github.com/user/repo" />
                </div>
                <div class="form-group">
                    <label for="p-org">Organization (Optional)</label>
                    <select id="p-org" bind:value={newProject.org_id}>
                        <option value="">None</option>
                        {#each orgs as org}
                            <option value={org.id}>{org.name}</option>
                        {/each}
                    </select>
                </div>
                <div class="form-group">
                    <label for="p-path">Pipeline Path (Optional)</label>
                    <input id="p-path" type="text" bind:value={newProject.pipeline_path} placeholder=".forge/pipeline.yml" />
                </div>
                <div class="form-group full-width">
                    <label for="p-token">SCM Token (Optional — for private repos)</label>
                    <input id="p-token" type="password" bind:value={newProject.scm_token} placeholder="ghp_..." />
                </div>
            </div>
            <div class="form-actions">
                <button class="btn-secondary" on:click={() => showCreate = false}>Cancel</button>
                <button class="btn-primary" on:click={createProject} disabled={!newProject.name || !newProject.repo_url}>Register</button>
            </div>
        </div>
    {/if}

    {#if loading}
        <div class="loading">Loading projects...</div>
    {:else if !projects || projects.length === 0}
        <div class="empty-state">
            <Briefcase size={48} />
            <p>No projects found.</p>
        </div>
    {:else}
        <div class="grid">
            {#each projects as project}
                <div class="card item-card">
                    <div class="item-icon">
                        <Briefcase size={24} />
                    </div>
                    <div class="item-content">
                        <div class="item-header">
                            <span class="item-name">{project.name}</span>
                            {#if healthByProject[project.id]}
                                {@const h = healthByProject[project.id]}
                                {@const band = healthBand(h.score)}
                                {@const delta = h.previous_score !== undefined ? h.score - h.previous_score : null}
                                <span
                                    class="health-badge health-{band}"
                                    title="{h.findings.length} finding{h.findings.length === 1 ? '' : 's'} — open the project for details{h.org_average !== undefined ? ` · org avg ${h.org_average.toFixed(0)}` : ''}"
                                >
                                    {h.score}
                                    {#if delta !== null && delta !== 0}
                                        <span class="health-delta">{delta > 0 ? '↑' : '↓'}{Math.abs(delta)}</span>
                                    {/if}
                                </span>
                            {:else if project.id in healthByProject}
                                <!-- Fetched and confirmed never-checked (explicit null), not just
                                     still loading — only now is "Check now" an honest affordance. -->
                                <button
                                    class="health-check-btn health-check-btn-text"
                                    disabled={checkingHealth[project.id]}
                                    on:click|stopPropagation={() => triggerHealthCheck(project.id)}
                                >
                                    <span class:spin={checkingHealth[project.id]}><RefreshCw size={12} /></span>
                                    {checkingHealth[project.id] ? 'Checking…' : 'Check health'}
                                </button>
                            {/if}
                            {#if project.repo_url}
                                <a href={project.repo_url} target="_blank" rel="noopener noreferrer" class="repo-link">
                                    <ExternalLink size={12} />
                                </a>
                            {/if}
                        </div>
                        <div class="item-meta">
                            <span>Org: {orgs.find(o => o.id === project.org_id)?.name || 'None'}</span>
                            <span>•</span>
                            <span>Created {new Date(project.created_at).toLocaleDateString()}</span>
                        </div>

                        <button class="btn-text" on:click={() => selectedProject = project}>
                            Manage
                            <ArrowRight size={14} />
                        </button>
                    </div>
                    <div class="item-actions">
                        {#if triggeringId === project.id}
                            <div class="trigger-form">
                                {#if loadingBranches}
                                    <span class="loading-small">...</span>
                                {:else if branches.length > 0}
                                    <select bind:value={triggerBranch}>
                                        {#each branches as branch}
                                            <option value={branch}>{branch}</option>
                                        {/each}
                                    </select>
                                {:else}
                                    <input type="text" bind:value={triggerBranch} placeholder="branch" on:keydown={(e) => e.key === 'Enter' && trigger(project.id)} />
                                {/if}
                                <button class="btn-confirm" on:click={() => trigger(project.id)} disabled={loadingBranches || triggering}>
                                    {triggering ? '...' : 'Go'}
                                </button>
                                <button class="btn-cancel" on:click={cancelTrigger}>&times;</button>
                            </div>
                        {:else}
                            <button class="btn-icon btn-trigger" on:click={() => openTrigger(project)} title="Trigger Pipeline">
                                <Play size={16} />
                            </button>
                        {/if}
                    </div>
                </div>
            {/each}
        </div>
    {/if}
</div>
{/if}

<style>
    .header-actions {
        display: flex;
        gap: 12px;
    }
    .error-banner {
        background: var(--red-muted);
        color: var(--red);
        padding: 12px 16px;
        border-radius: 6px;
        margin-bottom: 20px;
        display: flex;
        justify-content: space-between;
        align-items: center;
        font-size: 14px;
    }
    .error-banner button {
        background: transparent;
        border: none;
        color: var(--red);
        cursor: pointer;
        font-size: 18px;
    }
    .grid {
        display: grid;
        grid-template-columns: repeat(auto-fill, minmax(400px, 1fr));
        gap: 16px;
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
    .form-grid {
        display: grid;
        grid-template-columns: 1fr 1fr;
        gap: 16px;
        margin-bottom: 20px;
    }
    @media (max-width: 600px) {
        .form-grid {
            grid-template-columns: 1fr;
        }
        .full-width {
            grid-column: auto;
        }
    }
    .full-width {
        grid-column: span 2;
    }
    .form-group label {
        display: block;
        margin-bottom: 8px;
        font-size: 13px;
        color: var(--muted);
    }
    .form-group input, .form-group select {
        width: 100%;
        box-sizing: border-box;
    }
    .form-actions {
        display: flex;
        justify-content: flex-end;
        gap: 8px;
    }
    .btn-text {
        background: transparent;
        border: none;
        color: var(--muted);
        padding: 4px 0;
        cursor: pointer;
        display: flex;
        align-items: center;
        gap: 6px;
        font-size: 13px;
        margin-top: 8px;
    }
    .btn-text:hover {
        color: var(--accent);
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
    .btn-trigger:hover {
        background: var(--surface2);
        color: var(--accent);
    }
    .item-card {
        display: flex;
        gap: 16px;
        align-items: flex-start;
    }
    .item-icon {
        width: 48px;
        height: 48px;
        background: var(--surface2);
        border-radius: 8px;
        display: flex;
        align-items: center;
        justify-content: center;
        color: var(--blue);
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
        margin-bottom: 4px;
    }
    .item-name {
        font-weight: 600;
        font-size: 16px;
        white-space: nowrap;
        overflow: hidden;
        text-overflow: ellipsis;
    }
    .health-badge {
        display: inline-flex;
        align-items: center;
        gap: 3px;
        font-size: var(--font-xs);
        font-weight: 700;
        padding: 2px 6px 2px 7px;
        border-radius: 10px;
        flex-shrink: 0;
        font-family: inherit;
    }
    .color-warning { color: #e79504;}
    .color-bad { color: #f85149; }
    .color-info { color: #478bea;}
    .health-delta { font-weight: 500; opacity: 0.85; }
    .health-check-btn {
        display: inline-flex;
        align-items: center;
        gap: 4px;
        background: none;
        border: none;
        color: var(--muted);
        cursor: pointer;
        padding: 2px 4px;
        border-radius: 6px;
        font-size: var(--font-xs);
        flex-shrink: 0;
    }
    .health-check-btn:hover:not(:disabled) {
        background: rgba(255, 255, 255, 0.08);
        color: var(--accent);
    }
    .health-check-btn:disabled {
        opacity: 0.6;
        cursor: default;
    }
    .health-check-btn-text {
        border: 1px dashed var(--border, #30363d);
        color: var(--muted);
    }
    .health-panel {
        margin: 8px 0;
        padding: 10px 12px;
        background: rgba(255, 255, 255, 0.02);
        border: 1px solid var(--border, #30363d);
        border-radius: 8px;
        font-size: 12px;
    }
    .health-panel-header {
        color: var(--muted);
        font-size: var(--font-xs);
        margin-bottom: 8px;
        display: flex;
        flex-wrap: wrap;
        gap: 4px;
    }
    .health-panel-empty {
        color: #3fb950;
        font-weight: 600;
    }
    .health-group {
        margin-bottom: 10px;
    }
    .health-group:last-child {
        margin-bottom: 0;
    }
    .health-group-title {
        font-size: var(--font-2xs);
        font-weight: 700;
        letter-spacing: 0.5px;
        margin-bottom: 4px;
    }
    .health-group-critical { color: #f85149; }
    .health-group-warning { color: #d29922; }
    .health-group-suggestion { color: #8b949e; }
    .health-finding {
        line-height: 1.5;
        margin-top: 8px;
        color: var(--text);
        padding-left: 2px;
        word-break: break-word;
    }
    .spin {
        display: inline-flex;
        animation: health-spin 0.8s linear infinite;
    }
    @keyframes health-spin {
        from { transform: rotate(0deg); }
        to { transform: rotate(360deg); }
    }
    .repo-link {
        color: var(--muted);
        display: flex;
    }
    .repo-link:hover {
        color: var(--accent);
    }
    .item-id {
        font-size: var(--font-xs);
        color: var(--muted);
        font-family: monospace;
        margin-bottom: 8px;
    }
    .item-meta {
        font-size: 12px;
        color: var(--muted);
        display: flex;
        gap: 8px;
        flex-wrap: wrap;
    }
    .item-actions {
        display: flex;
        align-items: center;
        gap: 4px;
    }
    .trigger-form {
        display: flex;
        align-items: center;
        gap: 4px;
        background: var(--surface2);
        padding: 2px;
        border-radius: 4px;
    }
    .trigger-form input, .trigger-form select {
        width: 100px;
        background: var(--bg);
        border: 1px solid var(--border);
        border-radius: 4px;
        color: var(--text);
        font-size: 12px;
        padding: 4px 8px;
    }
    .loading-small {
        font-size: 12px;
        color: var(--muted);
        padding: 0 8px;
    }
    .btn-confirm {
        background: var(--accent);
        color: white;
        border: none;
        border-radius: 4px;
        padding: 4px 8px;
        font-size: 12px;
        cursor: pointer;
    }
    .btn-cancel {
        background: transparent;
        border: none;
        color: var(--muted);
        cursor: pointer;
        font-size: 16px;
        padding: 0 4px;
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
</style>
