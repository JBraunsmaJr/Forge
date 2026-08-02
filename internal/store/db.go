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

// NewGORM wraps an existing *sql.DB with GORM and runs migrations.
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

	/*
		gorm.io/driver/postgres v1.6.0 is built around pgx internally, but
		NewGORM hands it a *sql.DB opened via lib/pq (see the Conn config
		above). Under that combination, the driver's column-introspection
		query — which AutoMigrate uses to diff an EXISTING table's columns
		— fails with "pq: got N parameters but the statement requires
		N-1". This isn't specific to any one table or model: it reproduces
		on the very first table AutoMigrate touches (orgs), regardless of
		anything in this feature.

		Until the connection is switched to a native pgx one (the real
		fix — see NewGORM), we avoid the broken code path by skipping
		AutoMigrate for any table that already exists (HasTable itself
		works fine; only the deeper column diff is broken) and only
		letting AutoMigrate CREATE genuinely new tables, which doesn't
		hit this code path. This means schema changes to EXISTING tables
		are no longer applied automatically — they need a hand-written
		migration (like the raw-SQL indexes below) until the connection
		fixed properly.
	*/

	models := []struct {
		name  string
		model any
	}{
		{"orgs", &Org{}},
		{"users", &User{}},
		{"sso_identities", &SSOIdentity{}},
		{"projects", &Project{}},
		{"project_health_snapshots", &ProjectHealthSnapshot{}},
		{"api_tokens", &APIToken{}},
		{"audit_logs", &AuditLog{}},
		{"runs", &Run{}},
		{"jobs", &Job{}},
		{"test_file_durations", &TestFileDuration{}},
		{"test_shard_assignments", &TestShardAssignment{}},
		{"job_logs", &JobLog{}},
		{"job_root_causes", &JobRootCause{}},
		{"policies", &Policy{}},
		{"step_results", &StepResult{}},
		{"artifacts", &Artifact{}},
	}

	for _, m := range models {
		if migrateDB.Migrator().HasTable(m.model) {
			fmt.Printf("[migrate] %s already exists — skipping AutoMigrate diff\n", m.name)
			continue
		}
		fmt.Printf("[migrate] %s does not exist — creating\n", m.name)
		if err := migrateDB.AutoMigrate(m.model); err != nil {
			return fmt.Errorf("AutoMigrate %s: %w", m.name, err)
		}
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
