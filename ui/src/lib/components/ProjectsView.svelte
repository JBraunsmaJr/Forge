<script lang="ts">
    import { onMount } from 'svelte';
    import { api, type Project, type Org } from '../api';
    import { currentView } from '../stores';
    import { Plus, Briefcase, ExternalLink, Play, Key, ChevronDown, ChevronUp, Settings } from '@lucide/svelte';
    import SecretsManager from './SecretsManager.svelte';

    let projects: Project[] = [];
    let orgs: Org[] = [];
    let loading = true;
    let error = '';
    let selectedOrgId = '';
    let openSecretsId: string | null = null;
    
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

    let editingProject: Project | null = null;
    let editForm = {
        name: '',
        repo_url: '',
        pipeline_path: '',
        scm_token: '',
        branch_filter: [] as string[]
    };
    let branchFilterStr = '';

    function startEdit(project: Project) {
        editingProject = project;
        editForm = {
            name: project.name,
            repo_url: project.repo_url,
            pipeline_path: project.pipeline_path,
            scm_token: '', // Never show existing token
            branch_filter: [...(project.branch_filter || [])]
        };
        branchFilterStr = (project.branch_filter || []).join(', ');
        error = '';
    }

    async function updateProject() {
        if (!editingProject) return;
        error = '';
        try {
            const req: any = {
                name: editForm.name,
                repo_url: editForm.repo_url,
                pipeline_path: editForm.pipeline_path,
                branch_filter: branchFilterStr.split(',').map(s => s.trim()).filter(s => s)
            };
            if (editForm.scm_token) {
                req.scm_token = editForm.scm_token;
            }

            const success = await api.updateProject(editingProject.id, req);
            if (success) {
                editingProject = null;
                await refreshProjects();
            } else {
                error = 'Failed to update project. Please check your inputs.';
            }
        } catch (e) {
            console.error("Failed to update project:", e);
            error = 'An error occurred while updating the project.';
        }
    }

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
    }

    async function refreshProjects() {
        try {
            projects = await api.listProjects(selectedOrgId || undefined);
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
            if (res) {
                triggeringId = null;
                currentView.set('runs');
            } else {
                error = 'Failed to trigger pipeline. Make sure the branch exists and contains a valid pipeline file.';
            }
        } catch (e) {
            console.error("Trigger failed:", e);
            error = 'An error occurred while triggering the pipeline.';
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

    {#if editingProject}
        <div class="create-card card">
            <h3>Edit Project: {editingProject.name}</h3>
            <div class="form-grid">
                <div class="form-group">
                    <label for="e-name">Project Name</label>
                    <input id="e-name" type="text" bind:value={editForm.name} />
                </div>
                <div class="form-group">
                    <label for="e-repo">Repository URL</label>
                    <input id="e-repo" type="text" bind:value={editForm.repo_url} />
                </div>
                <div class="form-group">
                    <label for="e-path">Pipeline Path</label>
                    <input id="e-path" type="text" bind:value={editForm.pipeline_path} />
                </div>
                <div class="form-group">
                    <label for="e-token">SCM Token (Leave empty to keep current)</label>
                    <input id="e-token" type="password" bind:value={editForm.scm_token} placeholder="••••••••" />
                </div>
                <div class="form-group full-width">
                    <label for="e-branches">Branch Filter (Optional — comma separated)</label>
                    <input id="e-branches" type="text" bind:value={branchFilterStr} placeholder="main, dev" />
                </div>
            </div>
            <div class="form-actions">
                <button class="btn-secondary" on:click={() => editingProject = null}>Cancel</button>
                <button class="btn-primary" on:click={updateProject} disabled={!editForm.name || !editForm.repo_url}>Save Changes</button>
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
                            {#if project.repo_url}
                                <a href={project.repo_url} target="_blank" rel="noopener noreferrer" class="repo-link">
                                    <ExternalLink size={12} />
                                </a>
                            {/if}
                        </div>
                        <div class="item-id">ID: {project.id}</div>
                        <div class="item-meta">
                            <span>Org: {orgs.find(o => o.id === project.org_id)?.name || 'None'}</span>
                            <span>•</span>
                            <span>Created {new Date(project.created_at).toLocaleDateString()}</span>
                        </div>

                        <button class="btn-text" on:click={() => openSecretsId = openSecretsId === project.id ? null : project.id}>
                            <Key size={14} />
                            Secrets
                            {#if openSecretsId === project.id}
                                <ChevronUp size={14} />
                            {:else}
                                <ChevronDown size={14} />
                            {/if}
                        </button>

                        <button class="btn-text" on:click={() => startEdit(project)}>
                            <Settings size={14} />
                            Settings
                        </button>

                        {#if openSecretsId === project.id}
                            <SecretsManager scope="project" id={project.id} />
                        {/if}
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

<style>
    .view-container {
        padding: 24px;
        max-width: 1000px;
        margin: 0 auto;
    }
    .view-header {
        display: flex;
        justify-content: space-between;
        align-items: center;
        margin-bottom: 24px;
    }
    .header-actions {
        display: flex;
        gap: 12px;
    }
    h1 {
        margin: 0;
        font-size: 24px;
        font-weight: 600;
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
        grid-template-columns: repeat(auto-fill, minmax(320px, 1fr));
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
        padding: 8px 12px;
        background: var(--bg);
        border: 1px solid var(--border);
        border-radius: 4px;
        color: var(--text);
        box-sizing: border-box;
    }
    .form-actions {
        display: flex;
        justify-content: flex-end;
        gap: 8px;
    }
    .btn-primary {
        background: var(--accent);
        color: white;
        border: none;
        padding: 8px 16px;
        border-radius: 4px;
        cursor: pointer;
        display: flex;
        align-items: center;
        gap: 8px;
        font-size: 14px;
        font-weight: 500;
    }
    .btn-primary:disabled {
        opacity: 0.5;
        cursor: not-allowed;
    }
    .btn-secondary {
        background: transparent;
        color: var(--text);
        border: 1px solid var(--border);
        padding: 8px 16px;
        border-radius: 4px;
        cursor: pointer;
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
    .repo-link {
        color: var(--muted);
        display: flex;
    }
    .repo-link:hover {
        color: var(--accent);
    }
    .item-id {
        font-size: 11px;
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
