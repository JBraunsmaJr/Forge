package store

import (
	"database/sql"
	"fmt"

	// lib/pq is the pure-Go Postgres driver.
	// It registers itself as a "postgres" driver with database/sql.
	// We blank-import it for its side effect (driver registration).
	_ "github.com/lib/pq"
)

func Open(connStr string) (*sql.DB, error) {
	db, err := sql.Open("postgres", connStr)
	if err != nil {
		return nil, fmt.Errorf("opening database: %w", err)
	}

	if err := db.Ping(); err != nil {
		return nil, fmt.Errorf("connecting to database at %s: %w", connStr, err)
	}

	// Run migrations synchronously on startup.
	if err := migrate(db); err != nil {
		return nil, fmt.Errorf("running migrations: %w", err)
	}

	return db, nil
}

func migrate(db *sql.DB) error {
	_, err := db.Exec(`

-- ── Orgs ─────────────────────────────────────────────────────────────────────
-- Must come first: runs, policies, and projects all reference orgs.
CREATE TABLE IF NOT EXISTS orgs (
    id         TEXT        PRIMARY KEY,
    name       TEXT        NOT NULL UNIQUE,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

-- ── Users & SSO ──────────────────────────────────────────────────────────────
CREATE TABLE IF NOT EXISTS users (
    id         TEXT        PRIMARY KEY,
    email      TEXT        NOT NULL UNIQUE,
    name       TEXT        NOT NULL,
    role       TEXT        NOT NULL DEFAULT 'viewer',
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE TABLE IF NOT EXISTS sso_identities (
    id           TEXT        PRIMARY KEY,
    user_id      TEXT        NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    provider     TEXT        NOT NULL,
    external_id  TEXT        NOT NULL,
    last_login   TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    UNIQUE(provider, external_id)
);

-- ── Projects ─────────────────────────────────────────────────────────────────
-- References orgs — must come after orgs.
CREATE TABLE IF NOT EXISTS projects (
    id             TEXT        PRIMARY KEY,
    org_id         TEXT        REFERENCES orgs(id) ON DELETE SET NULL,
    name           TEXT        NOT NULL,
    repo_url       TEXT        NOT NULL UNIQUE,
    pipeline_path  TEXT        NOT NULL DEFAULT '',
    webhook_secret TEXT        NOT NULL,
    scm_token      TEXT        NOT NULL DEFAULT '',
    -- branch_filter: JSON array of branch names/globs. Empty = all branches.
    branch_filter  JSONB       NOT NULL DEFAULT '[]',
    cron           TEXT        NOT NULL DEFAULT '',
    scheduled_pipeline_path TEXT NOT NULL DEFAULT '',
    last_scheduled_at TIMESTAMPTZ,
    created_at     TIMESTAMPTZ NOT NULL DEFAULT NOW()
);
ALTER TABLE projects ADD COLUMN IF NOT EXISTS org_id TEXT REFERENCES orgs(id) ON DELETE SET NULL;

-- project_health_snapshots: one row per scheduled pipeline health check
-- (issue #46). Score and findings are computed by compiler.Score against
-- the project's pipeline file at the time of the check; history here is
-- what drives the "↓ from N last week" trend and org-average comparison —
-- neither is knowable from a single point-in-time score alone.
CREATE TABLE IF NOT EXISTS project_health_snapshots (
    id             TEXT        PRIMARY KEY,
    project_id     TEXT        NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
    computed_at    TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    pipeline_name  TEXT        NOT NULL DEFAULT '',
    score          INTEGER     NOT NULL,
    findings       JSONB       NOT NULL DEFAULT '[]'
);
CREATE INDEX IF NOT EXISTS idx_health_snapshots_project_time
    ON project_health_snapshots (project_id, computed_at DESC);
ALTER TABLE projects ADD COLUMN IF NOT EXISTS scm_token TEXT NOT NULL DEFAULT '';
ALTER TABLE projects ADD COLUMN IF NOT EXISTS branch_filter JSONB NOT NULL DEFAULT '[]';
ALTER TABLE projects ADD COLUMN IF NOT EXISTS pipeline_path TEXT NOT NULL DEFAULT '';
ALTER TABLE projects ADD COLUMN IF NOT EXISTS cron TEXT NOT NULL DEFAULT '';
ALTER TABLE projects ADD COLUMN IF NOT EXISTS scheduled_pipeline_path TEXT NOT NULL DEFAULT '';
ALTER TABLE projects ADD COLUMN IF NOT EXISTS last_scheduled_at TIMESTAMPTZ;

-- ── API tokens ────────────────────────────────────────────────────────────────
-- Raw tokens are never stored — only their SHA-256 hash.
-- Roles: "admin" (full access) | "agent" (job queue + log streaming only).
CREATE TABLE IF NOT EXISTS api_tokens (
    id         TEXT        PRIMARY KEY,
    token_hash TEXT        NOT NULL UNIQUE,
    name       TEXT        NOT NULL,
    role       TEXT        NOT NULL DEFAULT 'admin',
    expires_at TIMESTAMPTZ,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);
ALTER TABLE api_tokens ADD COLUMN IF NOT EXISTS role TEXT NOT NULL DEFAULT 'admin';
ALTER TABLE api_tokens ADD COLUMN IF NOT EXISTS org_id TEXT;
ALTER TABLE api_tokens ADD COLUMN IF NOT EXISTS project_id TEXT;
ALTER TABLE api_tokens ADD COLUMN IF NOT EXISTS user_id TEXT REFERENCES users(id) ON DELETE SET NULL;

-- ── Audit Logs ────────────────────────────────────────────────────────────────
CREATE TABLE IF NOT EXISTS audit_logs (
    id          TEXT        PRIMARY KEY,
    timestamp   TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    actor_id    TEXT,
    actor_name  TEXT,
    action      TEXT        NOT NULL,
    target_type TEXT,
    target_id   TEXT,
    details     JSONB,
    ip_address  TEXT,
    org_id      TEXT
);
ALTER TABLE audit_logs ADD COLUMN IF NOT EXISTS ip_address TEXT;
ALTER TABLE audit_logs ADD COLUMN IF NOT EXISTS org_id TEXT;

-- Enforce insert-only on audit_logs
DO $$
BEGIN
    IF NOT EXISTS (SELECT 1 FROM pg_trigger WHERE tgname = 'audit_logs_immutable') THEN
        CREATE OR REPLACE FUNCTION block_audit_mod() RETURNS TRIGGER AS $body$
        BEGIN
            RAISE EXCEPTION 'audit_logs are immutable';
        END;
        $body$ LANGUAGE plpgsql;

        CREATE TRIGGER audit_logs_immutable
        BEFORE UPDATE OR DELETE ON audit_logs
        FOR EACH STATEMENT EXECUTE FUNCTION block_audit_mod();
    END IF;
END $$;

-- ── Runs ──────────────────────────────────────────────────────────────────────
-- References orgs — must come after orgs.
CREATE TABLE IF NOT EXISTS runs (
    id               TEXT        PRIMARY KEY,
    name             TEXT        NOT NULL,
    workspace_dir    TEXT        NOT NULL DEFAULT '',
    applied_step_ids JSONB       NOT NULL DEFAULT '[]',
    org_id           TEXT        REFERENCES orgs(id) ON DELETE SET NULL,
    project_id       TEXT,
    ref              TEXT,
    commit_sha       TEXT,
    scm_provider     TEXT,
    parent_run_id    TEXT,
    created_at       TIMESTAMPTZ NOT NULL DEFAULT NOW()
);
ALTER TABLE runs ADD COLUMN IF NOT EXISTS org_id TEXT REFERENCES orgs(id) ON DELETE SET NULL;
ALTER TABLE runs ADD COLUMN IF NOT EXISTS project_id TEXT;
ALTER TABLE runs ADD COLUMN IF NOT EXISTS ref TEXT;
ALTER TABLE runs ADD COLUMN IF NOT EXISTS commit_sha TEXT;
ALTER TABLE runs ADD COLUMN IF NOT EXISTS scm_provider TEXT;
ALTER TABLE runs ADD COLUMN IF NOT EXISTS parent_run_id TEXT;
ALTER TABLE runs ADD COLUMN IF NOT EXISTS parent_job_id TEXT;
ALTER TABLE runs ADD COLUMN IF NOT EXISTS preferred_agent_id TEXT;
ALTER TABLE runs ADD COLUMN IF NOT EXISTS applied_step_ids JSONB NOT NULL DEFAULT '[]';
-- Stable pipeline identity, distinct from the human-readable run name.
-- runs.name is decorated per run ("ci @ ab12cd34 [main]", "rerun: ...") so it
-- cannot be used to correlate history (test durations, splitting) across runs.
ALTER TABLE runs ADD COLUMN IF NOT EXISTS pipeline_name TEXT NOT NULL DEFAULT '';

-- ── Jobs ─────────────────────────────────────────────────────────────────────
-- References runs — must come after runs.
CREATE TABLE IF NOT EXISTS jobs (
    id               TEXT        PRIMARY KEY,
    run_id           TEXT        NOT NULL REFERENCES runs(id) ON DELETE CASCADE,
    step_id          TEXT        NOT NULL,
    step_type        TEXT        NOT NULL DEFAULT 'task',
    image            TEXT        NOT NULL DEFAULT '',
    command          JSONB       NOT NULL DEFAULT '[]',
    work_dir         TEXT        NOT NULL DEFAULT '/workspace',
    env              JSONB       NOT NULL DEFAULT '{}',
    inputs           JSONB       NOT NULL DEFAULT '[]',
    timeout_ns       BIGINT      NOT NULL DEFAULT 1800000000000,
    depends_on       JSONB       NOT NULL DEFAULT '[]',
    secret_names     JSONB       NOT NULL DEFAULT '[]',
    docker_socket    BOOLEAN     NOT NULL DEFAULT FALSE,
    policy_source    TEXT        NOT NULL DEFAULT '',
    condition        TEXT        NOT NULL DEFAULT '',
    always_run       BOOLEAN     NOT NULL DEFAULT FALSE,
    entrypoint       JSONB       NOT NULL DEFAULT '[]',
    pipeline_ref       JSONB,
    release_config     JSONB,
    artifact_uploads   JSONB   NOT NULL DEFAULT '[]',
    artifact_downloads JSONB   NOT NULL DEFAULT '[]',
    emitted_step_ids JSONB       NOT NULL DEFAULT '[]',
    status           TEXT        NOT NULL DEFAULT 'pending',
    lease_id         TEXT        NOT NULL DEFAULT '',
    agent_id         TEXT        NOT NULL DEFAULT '',
    leased_at        TIMESTAMPTZ,
    heartbeat_at     TIMESTAMPTZ,
    exit_code        INTEGER     NOT NULL DEFAULT 0,
    duration_ms      BIGINT      NOT NULL DEFAULT 0,
    started_at       TIMESTAMPTZ,
    finished_at      TIMESTAMPTZ
);
ALTER TABLE jobs ADD COLUMN IF NOT EXISTS pipeline_ref JSONB;
ALTER TABLE jobs ADD COLUMN IF NOT EXISTS artifact_uploads JSONB NOT NULL DEFAULT '[]';
ALTER TABLE jobs ADD COLUMN IF NOT EXISTS artifact_downloads JSONB NOT NULL DEFAULT '[]';
ALTER TABLE jobs ADD COLUMN IF NOT EXISTS emitted_step_ids JSONB NOT NULL DEFAULT '[]';
ALTER TABLE jobs ADD COLUMN IF NOT EXISTS policy_source TEXT NOT NULL DEFAULT '';
ALTER TABLE jobs ADD COLUMN IF NOT EXISTS release_config JSONB;
ALTER TABLE jobs ADD COLUMN IF NOT EXISTS condition TEXT NOT NULL DEFAULT '';
ALTER TABLE jobs ADD COLUMN IF NOT EXISTS always_run BOOLEAN NOT NULL DEFAULT FALSE;
ALTER TABLE jobs ADD COLUMN IF NOT EXISTS docker_socket BOOLEAN NOT NULL DEFAULT FALSE;
ALTER TABLE jobs ADD COLUMN IF NOT EXISTS entrypoint JSONB NOT NULL DEFAULT '[]';
ALTER TABLE jobs ADD COLUMN IF NOT EXISTS test_report TEXT NOT NULL DEFAULT '';
ALTER TABLE jobs ADD COLUMN IF NOT EXISTS split JSONB;
-- Quoted: WITH is a reserved SQL keyword (CTEs), so every reference to this
-- column elsewhere must also quote it as "with".
ALTER TABLE jobs ADD COLUMN IF NOT EXISTS "with" JSONB NOT NULL DEFAULT '{}';

-- ── Test Split History ───────────────────────────────────────────────────────

-- Per-file timing recorded after each run.
-- The splitting algorithm reads from this table.
CREATE TABLE IF NOT EXISTS test_file_durations (
    id            BIGSERIAL   PRIMARY KEY,
    run_id        TEXT        NOT NULL REFERENCES runs(id) ON DELETE CASCADE,
    job_id        TEXT        NOT NULL REFERENCES jobs(id) ON DELETE CASCADE,
    project_id    TEXT        REFERENCES projects(id) ON DELETE SET NULL,
    pipeline_name TEXT        NOT NULL,
    step_id       TEXT        NOT NULL,
    file_path     TEXT        NOT NULL,
    duration_ms   BIGINT      NOT NULL,
    test_count    INT         NOT NULL DEFAULT 0,
    passed        INT         NOT NULL DEFAULT 0,
    failed        INT         NOT NULL DEFAULT 0,
    skipped       INT         NOT NULL DEFAULT 0,
    created_at    TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

-- Index for the splitting query: find recent durations for a given step.
CREATE INDEX IF NOT EXISTS test_file_dur_step_idx
    ON test_file_durations(project_id, pipeline_name, step_id, created_at DESC);

-- Shard assignments computed at job submission time.
-- Stored so reruns and the UI can show the plan.
CREATE TABLE IF NOT EXISTS test_shard_assignments (
    id            BIGSERIAL   PRIMARY KEY,
    run_id        TEXT        NOT NULL REFERENCES runs(id) ON DELETE CASCADE,
    step_id       TEXT        NOT NULL,
    shard_index   INT         NOT NULL,  -- 0-based
    total_shards  INT         NOT NULL,
    file_paths    JSONB       NOT NULL,  -- ["internal/auth/auth_test.go", ...]
    estimated_ms  BIGINT      NOT NULL,  -- estimated total duration for this shard
    created_at    TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX IF NOT EXISTS test_shard_run_idx
    ON test_shard_assignments(run_id, step_id, shard_index);

CREATE UNIQUE INDEX IF NOT EXISTS runs_parent_job_idx ON runs(parent_job_id) WHERE parent_job_id IS NOT NULL;

CREATE INDEX IF NOT EXISTS jobs_run_id_idx ON jobs(run_id);
CREATE INDEX IF NOT EXISTS jobs_status_idx ON jobs(status);
CREATE INDEX IF NOT EXISTS jobs_hb_idx     ON jobs(heartbeat_at) WHERE status = 'running';

-- ── Job logs ──────────────────────────────────────────────────────────────────
-- References jobs — must come after jobs.
CREATE TABLE IF NOT EXISTS job_logs (
    id      BIGSERIAL   PRIMARY KEY,
    job_id  TEXT        NOT NULL REFERENCES jobs(id) ON DELETE CASCADE,
    ts      TIMESTAMPTZ NOT NULL,
    level   TEXT        NOT NULL,
    message TEXT        NOT NULL
);
CREATE INDEX IF NOT EXISTS job_logs_job_id_idx ON job_logs(job_id);

-- ── Policies ─────────────────────────────────────────────────────────────────
-- References orgs — must come after orgs.
CREATE TABLE IF NOT EXISTS policies (
    id              TEXT        PRIMARY KEY,
    org_id          TEXT        NOT NULL REFERENCES orgs(id) ON DELETE CASCADE,
    name            TEXT        NOT NULL,
    description     TEXT        NOT NULL DEFAULT '',
    steps           JSONB       NOT NULL DEFAULT '[]',
    transformer     JSONB,
    forbid_override BOOLEAN     NOT NULL DEFAULT FALSE,
    created_at      TIMESTAMPTZ NOT NULL DEFAULT NOW()
);
ALTER TABLE policies ADD COLUMN IF NOT EXISTS transformer JSONB;
ALTER TABLE policies ADD COLUMN IF NOT EXISTS forbid_override BOOLEAN NOT NULL DEFAULT FALSE;
CREATE INDEX IF NOT EXISTS policies_org_id_idx ON policies(org_id);

-- ── Step results (flaky test detection) ──────────────────────────────────────
CREATE TABLE IF NOT EXISTS step_results (
    id            BIGSERIAL   PRIMARY KEY,
    run_id        TEXT        NOT NULL REFERENCES runs(id) ON DELETE CASCADE,
    pipeline_name TEXT        NOT NULL,
    step_id       TEXT        NOT NULL,
    status        TEXT        NOT NULL,
    duration_ms   BIGINT      NOT NULL DEFAULT 0,
    created_at    TIMESTAMPTZ NOT NULL DEFAULT NOW()
);
CREATE INDEX IF NOT EXISTS step_results_step_idx ON step_results(pipeline_name, step_id);
CREATE INDEX IF NOT EXISTS step_results_time_idx ON step_results(created_at DESC);

-- ── Artifacts ────────────────────────────────────────────────────────────────
-- References runs and jobs — must come after both.
CREATE TABLE IF NOT EXISTS artifacts (
    id           TEXT        PRIMARY KEY,
    run_id       TEXT        NOT NULL REFERENCES runs(id)  ON DELETE CASCADE,
    job_id       TEXT        REFERENCES jobs(id)  ON DELETE CASCADE,
    name         TEXT        NOT NULL,
    filename     TEXT        NOT NULL,
    size_bytes   BIGINT      NOT NULL DEFAULT 0,
    content_type TEXT        NOT NULL DEFAULT 'application/octet-stream',
    storage_key  TEXT        NOT NULL,
    upload_token TEXT,
    confirmed    BOOLEAN     NOT NULL DEFAULT FALSE,
    created_at   TIMESTAMPTZ NOT NULL DEFAULT NOW()
);
ALTER TABLE artifacts ADD COLUMN IF NOT EXISTS upload_token TEXT;
ALTER TABLE artifacts ADD COLUMN IF NOT EXISTS confirmed BOOLEAN NOT NULL DEFAULT FALSE;
ALTER TABLE artifacts ALTER COLUMN job_id DROP NOT NULL;
CREATE INDEX IF NOT EXISTS artifacts_run_id_idx   ON artifacts(run_id);
CREATE INDEX IF NOT EXISTS artifacts_run_name_idx ON artifacts(run_id, name);

`)
	return err
}
