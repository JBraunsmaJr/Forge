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

    import { api, authUrl, type Job, type RunDetail } from './lib/api';
    import { activeRun, selectedJob, connStatus, authRequired, runs, currentView } from './lib/stores';

    let sse: EventSource | null = null;
    let debugSession: { sessionID: string | null, expiresInS: number, status: 'starting' | 'ready' | 'closed' } = {
        sessionID: null,
        expiresInS: 0,
        status: 'starting'
    };

    async function selectRun(runID: string) {
        if (sse) {
            sse.close();
            sse = null;
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
        
        // Open SSE for live updates
        sse = new EventSource(authUrl(`/api/v1/runs/${runID}/events`));
        sse.onopen = () => connStatus.set('live');
        sse.onerror = () => connStatus.set('reconnecting');
        sse.onmessage = (e) => {
            const updated: RunDetail = JSON.parse(e.data);
            activeRun.set(updated);
            
            // Auto-open log streaming for running jobs
            for (const job of (updated.jobs || [])) {
                if (job.status === 'running') {
                    if (!$selectedJob || $selectedJob.job_id === job.job_id) {
                        selectedJob.set(job);
                    }
                    break;
                }
            }

            if (updated.status === 'passed' || updated.status === 'failed') {
                connStatus.set('done');
                sse?.close();
                sse = null;
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

        const debugSse = new EventSource(authUrl(`/api/v1/debug/${info.session_id}/stream`));
        debugSse.onmessage = (e) => {
            const evt = JSON.parse(e.data);
            if (evt.type === 'ready') {
                debugSession.status = 'ready';
            }
            if (evt.type === 'ttl') {
                debugSession.expiresInS = evt.expires_in_s;
            }
            if (evt.type === 'closed') {
                debugSession.status = 'closed';
                debugSse.close();
            }
        };
        debugSse.onerror = () => {
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

    onMount(() => {
        // Initial auto-select
        const unsubscribe = runs.subscribe(val => {
            if (val.length > 0 && !$activeRun) {
                const newest = val.find(r => r.status !== 'passed' && r.status !== 'failed');
                if (newest) selectRun(newest.run_id);
            }
        });

        return () => {
            unsubscribe();
            sse?.close();
        };
    });
</script>

<Header />

<div id="layout">
    <Sidebar onSelectRun={selectRun} />
    <main>
        {#if $currentView === 'runs'}
            <DAG onOpenDebug={openDebug} />
            <DetailsPanel 
                debugSession={debugSession}
                onCloseDebug={closeDebug}
            />
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
                {/if}
            </div>
        {/if}
    </main>
</div>

<LoginModal />
