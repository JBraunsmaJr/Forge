package scheduler

import (
	"os"
	"testing"

	"github.com/JBraunsmaJr/forge/internal/testutil/pgtest"
)

// TestMain gives the package's database tests a PostgreSQL instance. See
// pgtest for how one is chosen or started.
func TestMain(m *testing.M) {
	os.Exit(pgtest.Run(m))
}
