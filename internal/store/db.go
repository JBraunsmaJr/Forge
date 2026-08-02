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
func NewGORM(db *sql.DB) (*gorm.DB, error) {
	gdb, err := gorm.Open(postgres.New(postgres.Config{
		Conn: db,
	}), &gorm.Config{
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
	err := db.AutoMigrate(
		&Org{},
		&User{},
		&SSOIdentity{},
		&Project{},
		&ProjectHealthSnapshot{},
		&APIToken{},
		&AuditLog{},
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
