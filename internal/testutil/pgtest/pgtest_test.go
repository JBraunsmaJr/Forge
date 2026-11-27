package pgtest

import (
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

func TestRun_UsesAnExistingDatabaseURL(t *testing.T) {
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
	t.Setenv(EnvDatabaseURL, "")
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
	t.Setenv(EnvDatabaseURL, "")
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
