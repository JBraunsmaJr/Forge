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

-- ── Runs ──────────────────────────────────────────────────────────────────────
-- References orgs — must come after orgs.
CREATE TABLE IF NOT EXISTS runs (
    id               TEXT        PRIMARY KEY,
    name             TEXT        NOT NULL,
    workspace_dir    TEXT        NOT NULL DEFAULT '',
    applied_policies JSONB       NOT NULL DEFAULT '[]',
    org_id           TEXT        REFERENCES orgs(id) ON DELETE SET NULL,
    project_id       TEXT,
    created_at       TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

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
    policy_source    TEXT        NOT NULL DEFAULT '',
    condition        TEXT        NOT NULL DEFAULT '',
    always_run       BOOLEAN     NOT NULL DEFAULT FALSE,
    pipeline_ref       JSONB,
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
CREATE INDEX IF NOT EXISTS policies_org_id_idx ON policies(org_id);

-- ── Projects ─────────────────────────────────────────────────────────────────
-- References orgs — must come after orgs.
CREATE TABLE IF NOT EXISTS projects (
    id             TEXT        PRIMARY KEY,
    org_id         TEXT        REFERENCES orgs(id) ON DELETE SET NULL,
    name           TEXT        NOT NULL,
    repo_url       TEXT        NOT NULL UNIQUE,
    pipeline_path  TEXT        NOT NULL DEFAULT '.forge/pipeline.json',
    webhook_secret TEXT        NOT NULL,
    scm_token      TEXT        NOT NULL DEFAULT '',
    -- branch_filter: JSON array of branch names/globs. Empty = all branches.
    branch_filter  JSONB       NOT NULL DEFAULT '[]',
    created_at     TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

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
    job_id       TEXT        NOT NULL REFERENCES jobs(id)  ON DELETE CASCADE,
    name         TEXT        NOT NULL,
    filename     TEXT        NOT NULL,
    size_bytes   BIGINT      NOT NULL DEFAULT 0,
    content_type TEXT        NOT NULL DEFAULT 'application/octet-stream',
    storage_key  TEXT        NOT NULL,
    upload_token TEXT,
    confirmed    BOOLEAN     NOT NULL DEFAULT FALSE,
    created_at   TIMESTAMPTZ NOT NULL DEFAULT NOW()
);
CREATE INDEX IF NOT EXISTS artifacts_run_id_idx   ON artifacts(run_id);
CREATE INDEX IF NOT EXISTS artifacts_run_name_idx ON artifacts(run_id, name);
`)
	return err
}
