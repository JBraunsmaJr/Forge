<script lang="ts">
    import { onMount } from 'svelte';
    import Header from './lib/components/Header.svelte';
    import Sidebar from './lib/components/Sidebar.svelte';
    import DAG from './lib/components/DAG.svelte';
    import DetailsPanel from './lib/components/DetailsPanel.svelte';
    import LoginModal from './lib/components/LoginModal.svelte';
    
    import OrgsView from './lib/components/OrgsView.svelte';
    import ProjectsView from './lib/components/ProjectsView.svelte';
    import PoliciesView from './lib/components/PoliciesView.svelte';
    import TokensView from './lib/components/TokensView.svelte';
    import AgentsView from './lib/components/AgentsView.svelte';
    import PipelineEditor from './lib/components/PipelineEditor.svelte';
    import LogSearchView from './lib/components/LogSearchView.svelte';
    import AuditView from './lib/components/AuditView.svelte';

    import { api, authUrl, wsUrl, getToken, type Job, type RunDetail } from './lib/api';
    import { activeRun, selectedJob, connStatus, authRequired, runs, currentView, currentUser, navigateToRunID } from './lib/stores';

    let ws: WebSocket | null = null;
    let debugSession: { sessionID: string | null, expiresInS: number, status: 'starting' | 'ready' | 'closed' } = {
        sessionID: null,
        expiresInS: 0,
        status: 'starting'
    };

    async function selectRun(runID: string) {
        if (ws) {
            ws.close();
            ws = null;
        }

        activeRun.set(null);
        selectedJob.set(null);
        connStatus.set('connecting');

        const detail = await api.runDetail(runID);
        if (!detail) {
            connStatus.set('error');
            return;
        }

        activeRun.set(detail);
        
        // Open WebSocket for live updates
        ws = new WebSocket(wsUrl(`/api/v1/runs/${runID}/events`));
        ws.onopen = () => connStatus.set('live');
        ws.onerror = () => connStatus.set('reconnecting');
        ws.onmessage = (e) => {
            const updated: RunDetail = JSON.parse(e.data);
            activeRun.set(updated);

            if ($selectedJob) {
                /*
                    Keep the currently selected job's details in sync as the
                    run progresses - including the moment it finishes, so a
                    status change, exit code, or root-cause classification
                    that arrives after the job leaves "running"/"waiting"
                    still reaches the details panel. Previously this only
                    refreshed while the job was still running/waiting, so a
                    job that failed mid-stream would freeze its details at
                    the last "running" snapshot until the user re-clicked it.
                 */
                const stillPresent = (updated.jobs || []).find(j => j.job_id === $selectedJob!.job_id);
                if (stillPresent) {
                    selectedJob.set(stillPresent);
                }
            } else {
                // Nothing selected yet — auto-open log streaming for the
                // first running/waiting job.
                for (const job of (updated.jobs || [])) {
                    if (job.status === 'running' || job.status === 'waiting') {
                        selectedJob.set(job);
                        break;
                    }
                }
            }

            if (updated.status === 'passed' || updated.status === 'failed') {
                connStatus.set('done');
                ws?.close();
                ws = null;
            }
        };
    }

    async function openDebug(job: Job) {
        await closeDebug();
        debugSession = { sessionID: '', status: 'starting', expiresInS: 0 };
        
        const info = await api.createDebugSession(job.job_id);
        if (!info) {
            debugSession.status = 'closed';
            return;
        }

        debugSession = { sessionID: info.session_id, status: 'starting', expiresInS: info.expires_in_s };

        const debugWs = new WebSocket(wsUrl(`/api/v1/debug/${info.session_id}/stream`));
        debugWs.onmessage = (e) => {
            const evt = JSON.parse(e.data);
            if (evt.type === 'ready') {
                debugSession.status = 'ready';
            }
            if (evt.type === 'ttl') {
                debugSession.expiresInS = evt.expires_in_s;
            }
            if (evt.type === 'closed') {
                debugSession.status = 'closed';
                debugWs.close();
            }
        };
        debugWs.onerror = () => {
            if (debugSession.status !== 'closed') {
                debugSession.status = 'starting';
            }
        };
    }

    async function closeDebug() {
        if (debugSession.sessionID) {
            await api.deleteDebugSession(debugSession.sessionID);
        }
        debugSession = { sessionID: null, status: 'starting', expiresInS: 0 };
    }

    // Handle navigation requests from other components
    navigateToRunID.subscribe(id => {
        if (id) {
            selectRun(id);
            currentView.set('runs');
            navigateToRunID.set(null);
        }
    });

    let initialRunSelected = false;
    onMount(() => {
        const unsubNav = navigateToRunID.subscribe((id) => {
            if (id) {
                selectRun(id);
                navigateToRunID.set(null);
            }
        });

        // Initial auth check
        (async () => {
            try {
                const status = await api.authStatus();
                if (status.authenticated) {
                    currentUser.set(status.user || null);
                } else if (!getToken()) {
                    authRequired.set(true);
                }
            } catch (e) {
                console.error('Auth check failed:', e);
                if (!getToken()) authRequired.set(true);
            }
        })();

        // Handle URL-based run selection
        const path = window.location.pathname;
        const runMatch = path.match(/^\/runs\/([a-f0-9-]+)\/?$/);
        if (runMatch) {
            const runID = runMatch[1];
            initialRunSelected = true;
            selectRun(runID);
            currentView.set('runs');
        }

        // Initial auto-select (if no run selected via URL)
        const unsubscribe = runs.subscribe(val => {
            if (val.length > 0 && !$activeRun && !initialRunSelected) {
                const newest = val.find(r => r.status !== 'passed' && r.status !== 'failed');
                if (newest) selectRun(newest.run_id);
            }
            if (val.length > 0) {
                initialRunSelected = false; // Allow auto-select for future new runs if activeRun is null
            }
        });

        return () => {
            unsubscribe();
            unsubNav();
            ws?.close();
        };
    });
</script>

<Header />

<div id="layout">
    <Sidebar onSelectRun={selectRun} />
    <main>
        {#if $currentView === 'runs'}
            <div id="runs-view">
                <DAG onOpenDebug={openDebug} />
                <DetailsPanel 
                    debugSession={debugSession}
                    onCloseDebug={closeDebug}
                />
            </div>
        {:else}
            <div class="view-content">
                {#if $currentView === 'projects'}
                    <ProjectsView />
                {:else if $currentView === 'orgs'}
                    <OrgsView />
                {:else if $currentView === 'policies'}
                    <PoliciesView />
                {:else if $currentView === 'tokens'}
                    <TokensView />
                {:else if $currentView === 'agents'}
                    <AgentsView />
                {:else if $currentView === 'editor'}
                    <PipelineEditor />
                {:else if $currentView === 'search'}
                    <LogSearchView />
                {:else if $currentView === 'audit'}
                    <AuditView />
                {/if}
            </div>
        {/if}
    </main>
</div>

<LoginModal />
