const TOKEN_KEY = 'forge_token';

export function getToken(): string {
    return localStorage.getItem(TOKEN_KEY) || '';
}

export function setToken(t: string): void {
    localStorage.setItem(TOKEN_KEY, t);
}

export function authHeaders(extra: Record<string, string> = {}): Record<string, string> {
    const h = { ...extra };
    const t = getToken();
    if (t) h['Authorization'] = 'Bearer ' + t;
    return h;
}

export function authUrl(url: string): string {
    if (url.startsWith('http')) {
        try {
            const u = new URL(url);
            // Convert to relative if it's the same host OR it points to our own API.
            // This prevents "Mixed Content" blocks when the backend returns absolute HTTP URLs.
            if (u.host === location.host || u.pathname.startsWith('/api/v1/')) {
                url = u.pathname + u.search + u.hash;
            } else {
                return url;
            }
        } catch (e) {
            if (!url.includes(location.host)) {
                return url;
            }
        }
    }
    const t = getToken();
    if (!t) return url;
    return url + (url.includes('?') ? '&' : '?') + 'token=' + encodeURIComponent(t);
}

export function wsUrl(url: string): string {
    const protocol = location.protocol === 'https:' ? 'wss:' : 'ws:';
    const host = location.host;
    const fullUrl = protocol + '//' + host + url;
    return authUrl(fullUrl);
}

import { authRequired } from './stores';

async function fetchAuth(url: string, opts: RequestInit = {}): Promise<Response | null> {
    const resp = await fetch(url, { ...opts, headers: authHeaders(opts.headers as Record<string, string> || {}) });
    if (resp.status === 401) {
        authRequired.set(true);
        return resp;
    }
    return resp;
}

export interface Job {
    job_id: string;
    step_id: string;
    status: string;
    duration_ms: number;
    timeout_ns: number;
    started_at?: string;
    finished_at?: string;
    depends_on: string[];
    policy_source?: string;
}

export interface Run {
    run_id: string;
    name: string;
    status: string;
    job_count: number;
    created_at: string;
}

export interface RunComparison {
    run_id: string;
    duration_ms: number;
    avg_duration_ms: number;
    diff_ms: number;
    percent_change: number;
    regression_detected: boolean;
}

export interface RunDetail extends Run {
    jobs: Job[];
    applied_policies: string[];
}

export interface LogEvent {
    ts: string;
    level: string;
    message: string;
}

export interface Artifact {
    id: string;
    run_id: string;
    job_id: string;
    name: string;
    filename: string;
    size_bytes: number;
    content_type: string;
    download_url: string;
}

export interface Org {
    id: string;
    name: string;
    created_at: string;
}

export interface Project {
    id: string;
    org_id: string;
    name: string;
    repo_url: string;
    pipeline_path: string;
    branch_filter?: string[];
    webhook_secret?: string;
    created_at: string;
}

export interface Policy {
    id: string;
    org_id: string;
    name: string;
    description: string;
    steps?: any[];
    transformer?: any;
    forbid_override: boolean;
    created_at: string;
}

export interface Token {
    id: string;
    name: string;
    role: string;
    expires_at?: string;
    created_at: string;
}

export interface AgentInfo {
    id: string;
    last_heartbeat: string;
    concurrency: number;
    active_jobs_count: number;
    docker_images: number;
    version: string;
    labels: Record<string, string>;
    connected: boolean;
}

export const api = {
    listRuns: (limit = 50, offset = 0, search = '', status = ''): Promise<Run[]> => {
        let url = `/api/v1/runs?limit=${limit}&offset=${offset}`;
        if (search) url += `&search=${encodeURIComponent(search)}`;
        if (status) url += `&status=${encodeURIComponent(status)}`;
        return fetchAuth(url).then(r => r?.json()).then(data => data || []);
    },
    runDetail: (id: string): Promise<RunDetail | null> => 
        fetchAuth(`/api/v1/runs/${id}/detail`).then(r => r?.ok ? r.json() : null),
    runComparison: (id: string): Promise<RunComparison | null> =>
        fetchAuth(`/api/v1/runs/${id}/comparison`).then(r => r?.ok ? r.json() : null),
    jobLogs: (id: string): Promise<LogEvent[] | null> => 
        fetchAuth(`/api/v1/jobs/${id}/logs`).then(r => r?.ok ? r.json() : null),
    runArtifacts: (runID: string): Promise<Artifact[]> =>
        fetchAuth(`/api/v1/runs/${runID}/artifacts`).then(r => r?.ok ? r.json() : []).then(data => data || []),
    cancelRun: (id: string): Promise<void> =>
        fetchAuth(`/api/v1/runs/${id}/cancel`, { method: 'POST' }).then(() => {}),
    rerunRun: (id: string): Promise<{ run_id: string } | null> =>
        fetchAuth(`/api/v1/runs/${id}/rerun`, { method: 'POST' }).then(r => r?.ok ? r.json() : null),
    rerunFailed: (id: string): Promise<{ run_id: string } | null> =>
        fetchAuth(`/api/v1/runs/${id}/rerun-failed`, { method: 'POST' }).then(r => r?.ok ? r.json() : null),
    rerunJob: (id: string): Promise<{ run_id: string } | null> =>
        fetchAuth(`/api/v1/jobs/${id}/rerun`, { method: 'POST' }).then(r => r?.ok ? r.json() : null),
    approveJob: (id: string): Promise<boolean> =>
        fetchAuth(`/api/v1/jobs/${id}/approve`, { method: 'POST' }).then(r => r?.ok || false),
    createDebugSession: (jobID: string): Promise<{ session_id: string, expires_in_s: number } | null> =>
        fetchAuth('/api/v1/debug', {
            method: 'POST',
            headers: { 'Content-Type': 'application/json' },
            body: JSON.stringify({ job_id: jobID }),
        }).then(r => r?.ok ? r.json() : null),
    deleteDebugSession: (sessionID: string): Promise<void> =>
        fetchAuth(`/api/v1/debug/${sessionID}`, { method: 'DELETE' }).then(() => {}),

    // Org management
    listOrgs: (): Promise<Org[]> =>
        fetchAuth('/api/v1/orgs').then(r => r?.json()).then(data => data || []),
    createOrg: (name: string): Promise<Org | null> =>
        fetchAuth('/api/v1/orgs', {
            method: 'POST',
            headers: { 'Content-Type': 'application/json' },
            body: JSON.stringify({ name }),
        }).then(r => r?.ok ? r.json() : null),

    // Project management
    listProjects: (orgID?: string): Promise<Project[]> => {
        let url = '/api/v1/projects';
        if (orgID) url += `?org_id=${orgID}`;
        return fetchAuth(url).then(r => r?.json()).then(data => data || []);
    },
    listBranches: (id: string): Promise<{ branches: string[], default: string } | null> =>
        fetchAuth(`/api/v1/projects/${id}/branches`).then(r => r?.ok ? r.json() : null),
    createProject: (req: { name: string, repo_url: string, org_id?: string, scm_token?: string, pipeline_path?: string }): Promise<Project | null> => {
        let url = '/api/v1/projects';
        if (req.org_id) url += `?org_id=${req.org_id}`;
        return fetchAuth(url, {
            method: 'POST',
            headers: { 'Content-Type': 'application/json' },
            body: JSON.stringify(req),
        }).then(r => r?.ok ? r.json() : null);
    },
    updateProject: (id: string, req: { name?: string, repo_url?: string, pipeline_path?: string, scm_token?: string, branch_filter?: string[] }): Promise<boolean> =>
        fetchAuth(`/api/v1/projects/${id}`, {
            method: 'PUT',
            headers: { 'Content-Type': 'application/json' },
            body: JSON.stringify(req),
        }).then(r => r?.status === 204),
    triggerProject: async (id: string, branch: string, commit?: string): Promise<{ run_id: string }> => {
        const r = await fetchAuth(`/api/v1/projects/${id}/trigger`, {
            method: 'POST',
            headers: { 'Content-Type': 'application/json' },
            body: JSON.stringify({ branch, commit }),
        });
        if (!r?.ok) {
            const err = await r?.json().catch(() => ({ error: 'Unknown error' }));
            throw new Error(err?.error || 'Failed to trigger pipeline');
        }
        return r.json();
    },

    // Policy management
    listPolicies: (orgID: string): Promise<Policy[]> =>
        fetchAuth(`/api/v1/orgs/${orgID}/policies`).then(r => r?.json()).then(data => data || []),
    createPolicy: (orgID: string, req: { name: string, description: string, steps: any[], transformer?: any, forbid_override: boolean }): Promise<Policy | null> =>
        fetchAuth(`/api/v1/orgs/${orgID}/policies`, {
            method: 'POST',
            headers: { 'Content-Type': 'application/json' },
            body: JSON.stringify(req),
        }).then(r => r?.ok ? r.json() : null),
    updatePolicy: (orgID: string, policyID: string, req: { name: string, description: string, steps: any[], transformer?: any, forbid_override: boolean }): Promise<Policy | null> =>
        fetchAuth(`/api/v1/orgs/${orgID}/policies/${policyID}`, {
            method: 'PUT',
            headers: { 'Content-Type': 'application/json' },
            body: JSON.stringify(req),
        }).then(r => r?.ok ? r.json() : null),
    deletePolicy: (orgID: string, policyID: string): Promise<void> =>
        fetchAuth(`/api/v1/orgs/${orgID}/policies/${policyID}`, { method: 'DELETE' }).then(() => {}),

    // Secrets
    listOrgSecrets: (orgID: string): Promise<string[]> =>
        fetchAuth(`/api/v1/orgs/${orgID}/secrets`).then(r => r?.ok ? r.json() : []).then(data => data || []),
    setOrgSecret: (orgID: string, name: string, value: string): Promise<boolean> =>
        fetchAuth(`/api/v1/orgs/${orgID}/secrets`, {
            method: 'POST',
            body: JSON.stringify({ name, value })
        }).then(r => r?.ok || false),
    deleteOrgSecret: (orgID: string, name: string): Promise<boolean> =>
        fetchAuth(`/api/v1/orgs/${orgID}/secrets/${name}`, { method: 'DELETE' }).then(r => r?.ok || false),

    listProjectSecrets: (projectID: string): Promise<string[]> =>
        fetchAuth(`/api/v1/projects/${projectID}/secrets`).then(r => r?.ok ? r.json() : []).then(data => data || []),
    setProjectSecret: (projectID: string, name: string, value: string): Promise<boolean> =>
        fetchAuth(`/api/v1/projects/${projectID}/secrets`, {
            method: 'POST',
            body: JSON.stringify({ name, value })
        }).then(r => r?.ok || false),
    deleteProjectSecret: (projectID: string, name: string): Promise<boolean> =>
        fetchAuth(`/api/v1/projects/${projectID}/secrets/${name}`, { method: 'DELETE' }).then(r => r?.ok || false),

    // Token management
    listTokens: (): Promise<Token[]> =>
        fetchAuth('/api/v1/tokens').then(r => r?.json()).then(data => data || []),
    createToken: (req: { name: string, role?: string, expires_at?: string }): Promise<{ token: string, info: Token } | null> =>
        fetchAuth('/api/v1/tokens', {
            method: 'POST',
            headers: { 'Content-Type': 'application/json' },
            body: JSON.stringify(req),
        }).then(r => r?.ok ? r.json() : null),
    deleteToken: (id: string): Promise<void> =>
        fetchAuth(`/api/v1/tokens/${id}`, { method: 'DELETE' }).then(() => {}),

    // Agent management
    listAgents: (): Promise<AgentInfo[]> =>
        fetchAuth('/api/v1/agents').then(r => r?.json()).then(data => data || []),
};
