package store

import (
	"database/sql"
	"fmt"

	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"

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

	return db, nil
}

// NewGORM opens its own native pgx-based connection for GORM, using the
// same DSN the app's lib/pq connection (see Open, above) already uses.
// It deliberately does NOT reuse that lib/pq *sql.DB: gorm.io/driver/
// postgres's Migrator injects a pgx-specific query-exec-mode hint
// (pgx.QueryExecModeSimpleProtocol) as an extra bind variable whenever
// Config.DriverName is empty or "pgx" — see go-gorm/postgres's
// migrator.go. That hint is meaningless to lib/pq, which then sees one
// more parameter than the query has placeholders and fails with "pq:
// got N parameters but the statement requires N-1". This isn't limited
// to diffing old, pre-existing tables — it reproduces on a brand-new
// database too, e.g. resolving sso_identities' foreign key to users
// immediately after users was just created in the same AutoMigrate pass.
//
// IMPORTANT: do not "fix" this by setting Config.DriverName ourselves.
// Leaving DriverName empty is what routes postgres.Open's Initialize
// down its native pgx.ParseConfig/stdlib.OpenDB path in the first place;
// setting DriverName: "postgres" flips that same code path over to
// sql.Open("postgres", dsn) — i.e. lib/pq — which reintroduces exactly
// the mismatch above (empty/"pgx" DriverName + a non-pgx connection).
// The fix here is giving GORM a genuinely pgx-backed connection, not
// silencing the hint.
//
// This does mean two separate connection pools to the same database:
// the lib/pq one for the app's hand-written SQL (Store), and this pgx
// one for GORM. That's intentional — mixing drivers on one pool is
// exactly what caused the bug.
func NewGORM(dsn string) (*gorm.DB, error) {
	gdb, err := gorm.Open(postgres.Open(dsn), &gorm.Config{
		Logger:         logger.Default.LogMode(logger.Silent),
		TranslateError: true,
	})
	if err != nil {
		return nil, fmt.Errorf("initializing gorm: %w", err)
	}

	if err := autoMigrate(gdb); err != nil {
		return nil, fmt.Errorf("running auto-migration: %w", err)
	}

	return gdb, nil
}

func autoMigrate(db *gorm.DB) error {
	// AutoMigrate runs with its own verbose logger (scoped to this call
	// only, via Session — normal runtime queries stay silent) so any
	// migration issue stays visible in the logs going forward.
	migrateDB := db.Session(&gorm.Session{
		Logger: logger.Default.LogMode(logger.Info),
	})

	err := migrateDB.AutoMigrate(
		&Org{},
		&User{},
		&SSOIdentity{},
		&Project{},
		&ProjectHealthSnapshot{},
		&APIToken{},
		&AuditLog{},
		&BuildFormat{},
		&BuildCounter{},
		&Run{},
		&Job{},
		&TestFileDuration{},
		&TestShardAssignment{},
		&JobLog{},
		&JobRootCause{},
		&Policy{},
		&StepResult{},
		&Artifact{},
	)
	if err != nil {
		return fmt.Errorf("AutoMigrate models: %w", err)
	}

	// Custom trigger for audit_logs immutability
	sqlDB, err := db.DB()
	if err != nil {
		return fmt.Errorf("getting raw db connection for trigger: %w", err)
	}

	/*
		The following composite indexes are created via raw SQL rather
		than a second GORM `index:` tag on their columns: a column
		carrying two different named indexes trips a known
		gorm.io/driver/postgres v1.6.0 AutoMigrate bug ("pq: got N
		parameters but the statement requires N-1"). IF NOT EXISTS makes
		both safe to run on every startup.

		job_root_causes(project_id, created_at) — powers FailureBreakdown's
	*/

	if _, err := sqlDB.Exec(`CREATE INDEX IF NOT EXISTS job_root_causes_project_created_idx ON job_root_causes (project_id, created_at DESC)`); err != nil {
		return fmt.Errorf("creating job_root_causes_project_created_idx: %w", err)
	}

	/*
		artifacts(run_id, name) — pre-existing composite index that hit
		the same AutoMigrate bug; it never surfaced before because
		migration aborted on job_root_causes earlier in the model list
		and never reached this table.
	*/
	if _, err := sqlDB.Exec(`CREATE INDEX IF NOT EXISTS artifacts_run_name_idx ON artifacts (run_id, name)`); err != nil {
		return fmt.Errorf("creating artifacts_run_name_idx: %w", err)
	}

	_, err = sqlDB.Exec(`
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
`)
	if err != nil {
		return fmt.Errorf("setup audit_logs trigger: %w", err)
	}
	return nil
}
