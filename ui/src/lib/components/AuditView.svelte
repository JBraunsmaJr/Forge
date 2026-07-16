<script lang="ts">
    import { onMount } from 'svelte';
    import { api, type AuditEntry, type Org } from '../api';
    import { Shield, Filter, Download, RefreshCw, User, Terminal, Globe } from '@lucide/svelte';

    let logs: AuditEntry[] = [];
    let orgs: Org[] = [];
    let loading = true;
    let filters = {
        org_id: '',
        event_type: '',
        from: '',
        to: ''
    };

    async function loadLogs() {
        loading = true;
        try {
            logs = await api.listAuditLogs(filters.org_id, filters.event_type, filters.from, filters.to);
        } catch (e) {
            console.error("Failed to load audit logs:", e);
        } finally {
            loading = false;
        }
    }

    async function loadOrgs() {
        try {
            orgs = await api.listOrgs();
        } catch (e) {
            console.error("Failed to load orgs:", e);
        }
    }

    function formatDate(iso: string) {
        return new Date(iso).toLocaleString();
    }

    function formatDetails(details: any) {
        if (!details) return '-';
        if (typeof details === 'string') return details;
        return JSON.stringify(details);
    }

    function exportCSV() {
        let url = '/api/v1/audit?format=csv&';
        const params = new URLSearchParams();
        if (filters.org_id) params.append('org_id', filters.org_id);
        if (filters.event_type) params.append('event_type', filters.event_type);
        if (filters.from) params.append('from', filters.from);
        if (filters.to) params.append('to', filters.to);
        
        // We use window.open for download as the endpoint returns CSV
        window.open(url + params.toString(), '_blank');
    }

    onMount(() => {
        loadOrgs();
        loadLogs();
    });
</script>

<div class="view-container">
    <div class="view-header">
        <div class="title-section">
            <Shield size={24} class="header-icon" />
            <h1>Audit Logs</h1>
        </div>
        <div class="actions">
            <button class="btn-secondary" on:click={loadLogs} disabled={loading}>
                <RefreshCw size={16} class={loading ? 'spin' : ''} />
                Refresh
            </button>
            <button class="btn-primary" on:click={exportCSV}>
                <Download size={16} />
                Export CSV
            </button>
        </div>
    </div>

    <div class="filter-bar card">
        <div class="filter-group">
            <label for="org-filter">Organization</label>
            <select id="org-filter" bind:value={filters.org_id} on:change={loadLogs}>
                <option value="">All Organizations</option>
                {#each orgs as org}
                    <option value={org.id}>{org.name}</option>
                {/each}
            </select>
        </div>
        <div class="filter-group">
            <label for="event-filter">Event Type</label>
            <input id="event-filter" type="text" placeholder="e.g. run.trigger" bind:value={filters.event_type} on:change={loadLogs} />
        </div>
        <div class="filter-group">
            <label for="from-filter">From</label>
            <input id="from-filter" type="date" bind:value={filters.from} on:change={loadLogs} />
        </div>
        <div class="filter-group">
            <label for="to-filter">To</label>
            <input id="to-filter" type="date" bind:value={filters.to} on:change={loadLogs} />
        </div>
    </div>

    {#if loading && logs.length === 0}
        <div class="loading">Loading audit logs...</div>
    {:else if !logs || logs.length === 0}
        <div class="empty-state">
            <Shield size={48} />
            <p>No audit logs found matching the filters.</p>
        </div>
    {:else}
        <div class="table-container card">
            <table>
                <thead>
                    <tr>
                        <th>Timestamp</th>
                        <th>Actor</th>
                        <th>Action</th>
                        <th>Target</th>
                        <th>IP Address</th>
                        <th>Details</th>
                    </tr>
                </thead>
                <tbody>
                    {#each logs as log}
                        <tr>
                            <td class="nowrap">{formatDate(log.timestamp)}</td>
                            <td>
                                <div class="actor-info">
                                    <User size={12} />
                                    <span>{log.actor_name || 'System'}</span>
                                    {#if log.actor_id}
                                        <small>{log.actor_id.slice(0, 8)}</small>
                                    {/if}
                                </div>
                            </td>
                            <td>
                                <span class="badge badge-action">{log.action}</span>
                            </td>
                            <td>
                                {#if log.target_type}
                                    <div class="target-info">
                                        <small>{log.target_type}:</small>
                                        <span>{log.target_id}</span>
                                    </div>
                                {:else}
                                    -
                                {/if}
                            </td>
                            <td class="nowrap">
                                {#if log.ip_address}
                                    <div class="ip-info">
                                        <Globe size={12} />
                                        <span>{log.ip_address}</span>
                                    </div>
                                {:else}
                                    -
                                {/if}
                            </td>
                            <td class="details-cell">
                                <pre>{formatDetails(log.details)}</pre>
                            </td>
                        </tr>
                    {/each}
                </tbody>
            </table>
        </div>
    {/if}
</div>

<style>
    .view-container {
        padding: 24px;
        max-width: 1200px;
        margin: 0 auto;
    }
    .view-header {
        display: flex;
        justify-content: space-between;
        align-items: center;
        margin-bottom: 24px;
    }
    .title-section {
        display: flex;
        align-items: center;
        gap: 12px;
    }
    .header-icon {
        color: var(--accent);
    }
    h1 {
        margin: 0;
        font-size: 24px;
        font-weight: 600;
    }
    .actions {
        display: flex;
        gap: 8px;
    }
    .card {
        background: var(--surface);
        border: 1px solid var(--border);
        border-radius: 8px;
    }
    .filter-bar {
        display: grid;
        grid-template-columns: repeat(auto-fit, minmax(200px, 1fr));
        gap: 16px;
        padding: 16px;
        margin-bottom: 24px;
    }
    .filter-group label {
        display: block;
        margin-bottom: 6px;
        font-size: 12px;
        color: var(--muted);
    }
    .filter-group input, .filter-group select {
        width: 100%;
        background: var(--bg);
        border: 1px solid var(--border);
        border-radius: 4px;
        padding: 8px;
        color: var(--text);
        font-size: 14px;
    }
    .table-container {
        overflow-x: auto;
    }
    table {
        width: 100%;
        border-collapse: collapse;
        font-size: 13px;
    }
    th {
        text-align: left;
        padding: 12px 16px;
        border-bottom: 1px solid var(--border);
        color: var(--muted);
        font-weight: 600;
        background: var(--surface2);
    }
    td {
        padding: 12px 16px;
        border-bottom: 1px solid var(--border);
        vertical-align: top;
    }
    tr:last-child td {
        border-bottom: none;
    }
    tr:hover {
        background: rgba(255, 255, 255, 0.02);
    }
    .nowrap {
        white-space: nowrap;
    }
    .actor-info, .target-info, .ip-info {
        display: flex;
        align-items: center;
        gap: 6px;
    }
    .actor-info small, .target-info small {
        color: var(--muted);
        font-family: monospace;
    }
    .badge-action {
        background: var(--surface2);
        color: var(--accent);
        font-family: monospace;
    }
    .details-cell pre {
        margin: 0;
        white-space: pre-wrap;
        word-break: break-all;
        font-family: monospace;
        font-size: 11px;
        color: var(--muted);
        max-width: 300px;
    }
    .loading, .empty-state {
        text-align: center;
        padding: 64px;
        color: var(--muted);
    }
    .spin {
        animation: spin 1s linear infinite;
    }
    @keyframes spin {
        from { transform: rotate(0deg); }
        to { transform: rotate(360deg); }
    }
</style>
