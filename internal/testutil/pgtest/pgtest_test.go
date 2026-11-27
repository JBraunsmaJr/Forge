package pgtest

import (
	"errors"
	"os"
	"testing"
)

func TestParseHostPort(t *testing.T) {
	tests := []struct {
		name    string
		output  string
		want    string
		wantErr bool
	}{
		{"docker, loopback", "127.0.0.1:49153\n", "127.0.0.1:49153", false},
		{"podman, loopback", "127.0.0.1:38211", "127.0.0.1:38211", false},
		{"all interfaces", "0.0.0.0:49153\n", "127.0.0.1:49153", false},
		{"IPv4 preferred over IPv6", "[::]:49154\n0.0.0.0:49153\n", "127.0.0.1:49153", false},
		{"IPv6 only", "[::1]:49154\n", "[::1]:49154", false},
		{"arrow form", "5432/tcp -> 127.0.0.1:49153\n", "127.0.0.1:49153", false},
		{"blank lines", "\n\n127.0.0.1:49153\n\n", "127.0.0.1:49153", false},
		{"empty", "", "", true},
		{"whitespace only", "  \n \n", "", true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := parseHostPort(tt.output)
			if (err != nil) != tt.wantErr {
				t.Fatalf("parseHostPort() error = %v; wantErr %v", err, tt.wantErr)
			}
			if got != tt.want {
				t.Errorf("parseHostPort() = %q; want %q", got, tt.want)
			}
		})
	}
}

func TestBuildURL(t *testing.T) {
	const want = "postgres://forge:forge@127.0.0.1:49153/forge_test?sslmode=disable"
	if got := buildURL("127.0.0.1:49153"); got != want {
		t.Errorf("buildURL() = %q; want %q", got, want)
	}
}

// countingRunner records how often Run was called and what FORGE_DB_URL was
// at the time.
type countingRunner struct {
	calls   int
	seenURL string
}

func (r *countingRunner) Run() int {
	r.calls++
	r.seenURL = os.Getenv(EnvDatabaseURL)
	return 7
}

// isolateEnv clears every variable Run reads, for the duration of the test.
// The tests below describe Run's behavior for one combination each; without
// this, a variable set by the caller (make test-db sets FORGE_TEST_REQUIRE_DB)
// would change which combination a test exercises.
func isolateEnv(t *testing.T) {
	t.Helper()
	for _, name := range []string{EnvDatabaseURL, EnvNoContainers, EnvRequireDB, EnvImage} {
		t.Setenv(name, "")
	}
}

func TestRun_UsesAnExistingDatabaseURL(t *testing.T) {
	isolateEnv(t)
	const existing = "postgres://example/existing"
	t.Setenv(EnvDatabaseURL, existing)

	r := &countingRunner{}
	if code := Run(r); code != 7 {
		t.Errorf("Run() = %d; want the tests' own exit code 7", code)
	}
	if r.calls != 1 || r.seenURL != existing {
		t.Errorf("tests ran %d time(s) with %s=%q; want once with %q", r.calls, EnvDatabaseURL, r.seenURL, existing)
	}
}

func TestRun_NoContainersLeavesTheURLUnset(t *testing.T) {
	isolateEnv(t)
	t.Setenv(EnvNoContainers, "1")

	r := &countingRunner{}
	if code := Run(r); code != 7 {
		t.Errorf("Run() = %d; want the tests' own exit code 7", code)
	}
	if r.calls != 1 || r.seenURL != "" {
		t.Errorf("tests ran %d time(s) with %s=%q; want once with it unset", r.calls, EnvDatabaseURL, r.seenURL)
	}
}

func TestRun_RequireDBFailsWhenNoDatabaseIsAvailable(t *testing.T) {
	isolateEnv(t)
	t.Setenv(EnvNoContainers, "1")
	t.Setenv(EnvRequireDB, "1")

	r := &countingRunner{}
	if code := Run(r); code == 0 || code == 7 {
		t.Errorf("Run() = %d; want a failure of its own", code)
	}
	if r.calls != 0 {
		t.Errorf("tests ran %d time(s); want none when a required database is missing", r.calls)
	}
}

func TestRun_RequireDBIsSatisfiedByAnExistingURL(t *testing.T) {
	isolateEnv(t)
	t.Setenv(EnvDatabaseURL, "postgres://example/existing")
	t.Setenv(EnvRequireDB, "1")

	r := &countingRunner{}
	if code := Run(r); code != 7 {
		t.Errorf("Run() = %d; want the tests' own exit code 7", code)
	}
	if r.calls != 1 {
		t.Errorf("tests ran %d time(s); want once", r.calls)
	}
}

func TestRun_SetupRunsBeforeTheTests(t *testing.T) {
	isolateEnv(t)
	const existing = "postgres://example/existing"
	t.Setenv(EnvDatabaseURL, existing)

	r := &countingRunner{}
	var setupURL string
	var testsHadRunAtSetup bool
	setup := func(databaseURL string) error {
		setupURL = databaseURL
		testsHadRunAtSetup = r.calls > 0
		return nil
	}

	if code := Run(r, WithSetup(setup)); code != 7 {
		t.Errorf("Run() = %d; want the tests' own exit code 7", code)
	}
	if setupURL != existing {
		t.Errorf("setup received %q; want %q", setupURL, existing)
	}
	if testsHadRunAtSetup {
		t.Error("the tests ran before setup")
	}
	if r.calls != 1 {
		t.Errorf("tests ran %d time(s); want once", r.calls)
	}
}

func TestRun_SetupFailureStopsTheRun(t *testing.T) {
	isolateEnv(t)
	t.Setenv(EnvDatabaseURL, "postgres://example/existing")

	r := &countingRunner{}
	setup := func(string) error { return errors.New("schema is broken") }

	if code := Run(r, WithSetup(setup)); code == 0 || code == 7 {
		t.Errorf("Run() = %d; want a failure of its own", code)
	}
	if r.calls != 0 {
		t.Errorf("tests ran %d time(s) after setup failed; want none", r.calls)
	}
}

func TestRun_SetupIsNotCalledWithoutADatabase(t *testing.T) {
	isolateEnv(t)
	t.Setenv(EnvNoContainers, "1")

	called := false
	setup := func(string) error { called = true; return nil }

	Run(&countingRunner{}, WithSetup(setup))
	if called {
		t.Error("setup was called although no database was available")
	}
}
