package scheduler

import (
	"os"
	"testing"

	"github.com/JBraunsmaJr/forge/internal/store"
	"github.com/JBraunsmaJr/forge/internal/testutil/pgtest"
)

// TestMain gives the package's database tests a PostgreSQL instance with the
// schema in place. See pgtest for how the instance is chosen or started.
func TestMain(m *testing.M) {
	os.Exit(pgtest.Run(m, pgtest.WithSetup(migrateTestDatabase)))
}

// migrateTestDatabase creates the schema the same way the scheduler does at
// startup: store.NewGORM runs the migrations as it connects.
func migrateTestDatabase(databaseURL string) error {
	gdb, err := store.NewGORM(databaseURL)
	if err != nil {
		return err
	}
	sqlDB, err := gdb.DB()
	if err != nil {
		return err
	}
	return sqlDB.Close()
}
