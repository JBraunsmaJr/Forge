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

export interface RootCauseInfo {
    category: string;
    pattern_id?: string;
    description: string;
    matched_line?: string;
    suggested_fix?: string;
    recent_matches: number;
    recent_total: number;
}

export interface DockerPublishConfig {
    registry: string;
    repository: string;
    source: string;
    tags: string[];
    delete_source: boolean;
}

export interface DockerPublishResult {
    tags_applied: string[];
    source_digest?: string;
    source_deleted: boolean;
    warnings?: string[];
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
    child_run_id?: string;
    root_cause?: RootCauseInfo;
    // docker_publish/docker_publish_result are populated only for
    // docker_publish-type jobs: the configured promotion and its
    // outcome (tags applied, deletion status, warnings).
    docker_publish?: DockerPublishConfig;
    docker_publish_result?: DockerPublishResult;
}

export interface Run {
    run_id: string;
    name: string;
    status: string;
    job_count: number;
    created_at: string;
    // build_number is the FORGE_BUILD_NUMBER assigned to this run at
    // submission time.
    build_number?: string;
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
    shard_assignments?: Record<string, ShardAssignmentDetail[]>;
    // build_number is the FORGE_BUILD_NUMBER assigned to this run at
    // submission time.
    build_number?: string;
}

export interface ShardAssignmentDetail {
    shard_index: number;
    total_shards: number;
    file_paths: string[];
    estimated_ms: number;
}

export interface LogEvent {
    ts: string;
    level: string;
    message: string;
}

export interface LogSearchResult {
    timestamp: string;
    level: string;
    message: string;
    job_id: string;
    job_name: string;
    run_id: string;
    run_name: string;
    org_id: string;
    project_id: string;
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
    cron?: string;
    scheduled_pipeline_path?: string;
    branch_filter?: string[];
    webhook_secret?: string;
    created_at: string;
}

export interface ProjectWebhook {
    webhook_secret: string;
    github_url: string;
    gitlab_url: string;
    generic_url: string;
}

// BuildFormatInfo describes the build-number format and version state
// configured for a (project, pipeline) scope (issue #57).
export interface BuildFormatInfo {
    project_id: string;
    pipeline_name: string;
    format: string;
    major: number;
    minor: number;
    version_source?: string; // "", "manual", or "tag:<ref>"
    version_set_by?: string;
    version_tag_filter?: string;
    sample_build_number: string;
}

export interface HealthFinding {
    severity: 'critical' | 'warning' | 'suggestion';
    message: string;
}

export interface ProjectHealth {
    project_id: string;
    pipeline_name: string;
    score: number;
    computed_at: string;
    findings: HealthFinding[];
    previous_score?: number;
    previous_at?: string;
    org_average?: number;
    org_project_count?: number;
}

// FailureBreakdown summarizes classified-failure categories for a
// project over a recent window (issue #44).
export interface FailureBreakdown {
    project_id: string;
    window_days: number;
    total_failures: number;
    categories: Record<string, number>;
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

export interface AuditEntry {
    id: string;
    timestamp: string;
    actor_id: string;
    actor_name: string;
    action: string;
    target_type: string;
    target_id: string;
    details: any;
    ip_address?: string;
    org_id?: string;
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

export interface User {
    id: string;
    email: string;
    name: string;
    role: string;
    created_at: string;
}

export interface AuthStatus {
    authenticated: boolean;
    user?: User;
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
    // null means "never checked yet" (backend returns 404) — a normal
    // state for a newly registered project, not an error to surface.
    projectHealth: (id: string): Promise<ProjectHealth | null> =>
        fetchAuth(`/api/v1/projects/${id}/health`).then(r => r?.ok ? r.json() : null),
    // Runs synchronously server-side and returns the fresh result. May
    // 429 if a check ran too recently (see healthTriggerCooldown
    // server-side) — callers should surface response.status on failure,
    // not just treat any non-ok response as "never checked."
    triggerProjectHealth: (id: string): Promise<{ ok: boolean, status: number, data: ProjectHealth | null }> =>
        fetchAuth(`/api/v1/projects/${id}/health/check`, { method: 'POST' }).then(async (r) => ({
            ok: !!r?.ok,
            status: r?.status ?? 0,
            data: r?.ok ? await r.json() : null,
        })),
    createProject: (req: { name: string, repo_url: string, org_id?: string, scm_token?: string, pipeline_path?: string }): Promise<Project | null> => {
        let url = '/api/v1/projects';
        if (req.org_id) url += `?org_id=${req.org_id}`;
        return fetchAuth(url, {
            method: 'POST',
            headers: { 'Content-Type': 'application/json' },
            body: JSON.stringify(req),
        }).then(r => r?.ok ? r.json() : null);
    },
    updateProject: (id: string, req: { name?: string, repo_url?: string, pipeline_path?: string, scm_token?: string, branch_filter?: string[], org_id?: string }): Promise<boolean> =>
        fetchAuth(`/api/v1/projects/${id}`, {
            method: 'PATCH',
            headers: { 'Content-Type': 'application/json' },
            body: JSON.stringify(req),
        }).then(r => r?.status === 204),
    deleteProject: (id: string): Promise<void> =>
        fetchAuth(`/api/v1/projects/${id}`, { method: 'DELETE' }).then(() => {}),
    // Admin-only: reveals the webhook URLs and secret for a project so it
    // can be (re-)configured in the SCM provider (issue #55).
    getProjectWebhook: (id: string): Promise<{ ok: boolean, status: number, data: ProjectWebhook | null }> =>
        fetchAuth(`/api/v1/projects/${id}/webhook`).then(async (r) => ({
            ok: !!r?.ok,
            status: r?.status ?? 0,
            data: r?.ok ? await r.json() : null,
        })),
    // Category breakdown of automatically-classified failures over the
    // last `days` (default 30) — powers the failure-insights panel
    getFailureStats: (id: string, days = 30): Promise<FailureBreakdown | null> =>
        fetchAuth(`/api/v1/projects/${id}/failure-stats?days=${days}`).then(r => r?.ok ? r.json() : null),

    // Build number format & version (issue #57)
    getBuildFormat: (projectID: string, pipelineName: string): Promise<BuildFormatInfo | null> =>
        fetchAuth(`/api/v1/projects/${projectID}/build-format?pipeline=${encodeURIComponent(pipelineName)}`)
            .then(r => r?.ok ? r.json() : null),
    // Returns { ok, status } rather than a boolean so the caller can
    // show the server's rejection reason (e.g. an unknown token) —
    // this is the "rejected at validation time" save path.
    setBuildFormat: async (projectID: string, pipelineName: string, format: string): Promise<{ ok: boolean, error?: string }> => {
        const r = await fetchAuth(`/api/v1/projects/${projectID}/build-format`, {
            method: 'PUT',
            headers: { 'Content-Type': 'application/json' },
            body: JSON.stringify({ pipeline_name: pipelineName, format }),
        });
        if (r?.status === 204) return { ok: true };
        const body = await r?.json().catch(() => null);
        return { ok: false, error: body?.error || 'Failed to save format' };
    },
    setVersion: async (projectID: string, pipelineName: string, major: number, minor: number): Promise<{ ok: boolean, error?: string }> => {
        const r = await fetchAuth(`/api/v1/projects/${projectID}/version`, {
            method: 'PUT',
            headers: { 'Content-Type': 'application/json' },
            body: JSON.stringify({ pipeline_name: pipelineName, major, minor }),
        });
        if (r?.status === 204) return { ok: true };
        const body = await r?.json().catch(() => null);
        return { ok: false, error: body?.error || 'Failed to save version' };
    },
    setVersionTagFilter: async (projectID: string, pipelineName: string, filter: string): Promise<{ ok: boolean, error?: string }> => {
        const r = await fetchAuth(`/api/v1/projects/${projectID}/version-tag-filter`, {
            method: 'PUT',
            headers: { 'Content-Type': 'application/json' },
            body: JSON.stringify({ pipeline_name: pipelineName, filter }),
        });
        if (r?.status === 204) return { ok: true };
        const body = await r?.json().catch(() => null);
        return { ok: false, error: body?.error || 'Failed to save tag filter' };
    },

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

    // Logs & Search
    searchLogs: (query: string, limit = 50, orgID = '', projectID = '', runID = '', jobID = ''): Promise<LogSearchResult[]> => {
        let url = `/api/v1/logs/search?q=${encodeURIComponent(query)}&limit=${limit}`;
        if (orgID) url += `&org_id=${orgID}`;
        if (projectID) url += `&project_id=${projectID}`;
        if (runID) url += `&run_id=${runID}`;
        if (jobID) url += `&job_id=${jobID}`;
        return fetchAuth(url).then(r => r?.json()).then(data => data || []);
    },

    // Agent management
    listAgents: (): Promise<AgentInfo[]> =>
        fetchAuth('/api/v1/agents').then(r => r?.json()).then(data => data || []),

    // Audit management
    listAuditLogs: (orgID = '', eventType = '', from = '', to = ''): Promise<AuditEntry[]> => {
        let url = `/api/v1/audit?org_id=${orgID}&event_type=${eventType}`;
        if (from) url += `&from=${from}`;
        if (to) url += `&to=${to}`;
        return fetchAuth(url).then(r => r?.json()).then(data => data || []);
    },

    // Auth & SSO
    authStatus: (): Promise<AuthStatus> =>
        fetchAuth('/api/v1/auth/status').then(r => r?.json()),
    logout: (): Promise<void> =>
        fetchAuth('/api/v1/auth/logout', { method: 'POST' }).then(() => {
            localStorage.removeItem(TOKEN_KEY);
        }),
};
