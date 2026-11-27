// Package pgtest gives tests a PostgreSQL database without any setup.
//
// Call Run from a package's TestMain:
//
//	func TestMain(m *testing.M) { os.Exit(pgtest.Run(m)) }
//
// Run makes sure FORGE_DB_URL points at a usable database for the duration of
// the package's tests:
//
//   - If FORGE_DB_URL is already set, it is used as is. This is how CI points
//     the tests at a database it manages.
//   - Otherwise, if a container CLI (docker or podman) is installed, Run
//     starts a throwaway PostgreSQL container, waits for it, exports its URL
//     as FORGE_DB_URL, and removes the container when the tests finish.
//   - Otherwise FORGE_DB_URL stays unset and database tests skip themselves.
//
// Set FORGE_TEST_NO_CONTAINERS=1 to forbid starting a container.
//
// Skipping is convenient on a laptop and dangerous in CI, where a passing run
// with every database test skipped looks the same as a real pass. Set
// FORGE_TEST_REQUIRE_DB=1 to turn "no database" into a failure.
package pgtest

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"

	"github.com/JBraunsmaJr/forge/internal/dockerutil"
)

const (
	// EnvDatabaseURL is the variable database tests read their DSN from.
	EnvDatabaseURL = "FORGE_DB_URL"

	// EnvNoContainers forbids pgtest from starting a container when set to
	// a non-empty value.
	EnvNoContainers = "FORGE_TEST_NO_CONTAINERS"

	// EnvRequireDB makes Run fail, rather than let database tests skip, when
	// no database can be provided. Set it to a non-empty value in CI.
	EnvRequireDB = "FORGE_TEST_REQUIRE_DB"

	// EnvImage overrides the PostgreSQL image, for example to pull from an
	// internal mirror.
	EnvImage = "FORGE_TEST_POSTGRES_IMAGE"

	defaultImage = "postgres:16-alpine"

	dbUser     = "forge"
	dbPassword = "forge"
	dbName     = "forge_test"

	containerPort = "5432/tcp"
	startTimeout  = 90 * time.Second
	pollInterval  = 500 * time.Millisecond
	stopTimeout   = 20 * time.Second
)

// runner is the part of testing.M that Run needs.
type runner interface {
	Run() int
}

// Run prepares a database as described in the package comment, runs the
// tests, cleans up, and returns the exit code for os.Exit.
func Run(m runner) int {
	if os.Getenv(EnvDatabaseURL) != "" {
		return m.Run()
	}
	if os.Getenv(EnvNoContainers) != "" {
		return runWithoutDatabase(m, EnvNoContainers+" is set")
	}
	cli := dockerutil.Runtime()
	if _, err := exec.LookPath(cli); err != nil {
		return runWithoutDatabase(m, fmt.Sprintf(
			"no container CLI found on PATH (looked for docker and podman; set %s to name another)",
			dockerutil.EnvContainerRuntime))
	}

	db, err := start()
	if err != nil {
		// A broken harness must not look like a passing run with skips.
		fmt.Fprintf(os.Stderr, "pgtest: could not start PostgreSQL with %s: %v\n", cli, err)
		fmt.Fprintf(os.Stderr, "pgtest: set %s to use an existing database, or %s=1 to skip database tests\n",
			EnvDatabaseURL, EnvNoContainers)
		return 1
	}
	defer db.stop()

	os.Setenv(EnvDatabaseURL, db.url)
	defer os.Unsetenv(EnvDatabaseURL)

	return m.Run()
}

// runWithoutDatabase runs the tests with no database, so database tests skip
// themselves, unless a database is required, in which case it fails without
// running anything. reason says why no database is available.
func runWithoutDatabase(m runner, reason string) int {
	if os.Getenv(EnvRequireDB) != "" {
		fmt.Fprintf(os.Stderr, "pgtest: %s is set but no database is available: %s\n", EnvRequireDB, reason)
		fmt.Fprintf(os.Stderr, "pgtest: set %s to a PostgreSQL URL, or install docker or podman\n", EnvDatabaseURL)
		return 1
	}
	fmt.Fprintf(os.Stderr, "pgtest: database tests will be skipped: %s\n", reason)
	return m.Run()
}

// database is a running PostgreSQL container.
type database struct {
	containerID string
	url         string
}

// start launches the container and waits until PostgreSQL accepts TCP
// connections. On failure the container is removed.
func start() (*database, error) {
	ctx, cancel := context.WithTimeout(context.Background(), startTimeout)
	defer cancel()

	image := os.Getenv(EnvImage)
	if image == "" {
		image = defaultImage
	}

	// Publish on a random loopback port so that parallel test processes, and
	// any PostgreSQL already on this machine, do not collide.
	out, err := dockerutil.CommandContext(ctx, "run", "--detach", "--rm",
		"--label", "forge.test=pgtest",
		"--env", "POSTGRES_USER="+dbUser,
		"--env", "POSTGRES_PASSWORD="+dbPassword,
		"--env", "POSTGRES_DB="+dbName,
		"--publish", "127.0.0.1::5432",
		image,
	).Output()
	if err != nil {
		return nil, fmt.Errorf("starting %s: %w", image, describe(err))
	}
	db := &database{containerID: strings.TrimSpace(string(out))}

	if err := db.waitUntilReady(ctx); err != nil {
		db.stop()
		return nil, err
	}

	hostPort, err := db.hostPort(ctx)
	if err != nil {
		db.stop()
		return nil, err
	}
	db.url = buildURL(hostPort)
	return db, nil
}

// waitUntilReady polls pg_isready over TCP inside the container. The image's
// entrypoint first runs a temporary server that listens only on a Unix
// socket, so a TCP check passes only once the real server is up.
func (db *database) waitUntilReady(ctx context.Context) error {
	for {
		err := dockerutil.CommandContext(ctx, "exec", db.containerID,
			"pg_isready", "--host", "127.0.0.1", "--username", dbUser, "--dbname", dbName).Run()
		if err == nil {
			return nil
		}
		select {
		case <-ctx.Done():
			return fmt.Errorf("PostgreSQL was not ready after %s", startTimeout)
		case <-time.After(pollInterval):
		}
	}
}

// hostPort asks the container CLI which host address the database port was
// published on.
func (db *database) hostPort(ctx context.Context) (string, error) {
	out, err := dockerutil.CommandContext(ctx, "port", db.containerID, containerPort).Output()
	if err != nil {
		return "", fmt.Errorf("reading the published port: %w", describe(err))
	}
	return parseHostPort(string(out))
}

// stop removes the container. Errors are reported but not returned: by this
// point the tests have their result.
func (db *database) stop() {
	ctx, cancel := context.WithTimeout(context.Background(), stopTimeout)
	defer cancel()
	if err := dockerutil.CommandContext(ctx, "rm", "--force", "--volumes", db.containerID).Run(); err != nil {
		fmt.Fprintf(os.Stderr, "pgtest: could not remove container %s: %v\n", db.containerID, err)
	}
}

// parseHostPort extracts "host:port" from the output of the CLI's port
// command, which prints one mapping per line, for example "127.0.0.1:49153".
// An IPv4 mapping is preferred over an IPv6 one.
func parseHostPort(output string) (string, error) {
	var fallback string
	for _, line := range strings.Split(output, "\n") {
		line = strings.TrimSpace(line)
		// Some versions print "5432/tcp -> 127.0.0.1:49153".
		if _, after, found := strings.Cut(line, "-> "); found {
			line = strings.TrimSpace(after)
		}
		if line == "" {
			continue
		}
		if strings.HasPrefix(line, "[") {
			if fallback == "" {
				fallback = line
			}
			continue
		}
		if strings.HasPrefix(line, "0.0.0.0:") {
			line = "127.0.0.1:" + strings.TrimPrefix(line, "0.0.0.0:")
		}
		return line, nil
	}
	if fallback != "" {
		return fallback, nil
	}
	return "", errors.New("the container CLI reported no published port")
}

// buildURL returns the DSN for the test database at hostPort.
func buildURL(hostPort string) string {
	return fmt.Sprintf("postgres://%s:%s@%s/%s?sslmode=disable", dbUser, dbPassword, hostPort, dbName)
}

// describe adds a failed command's stderr to its error, which otherwise says
// only "exit status 125".
func describe(err error) error {
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) && len(exitErr.Stderr) > 0 {
		return fmt.Errorf("%w: %s", err, strings.TrimSpace(string(exitErr.Stderr)))
	}
	return err
}
