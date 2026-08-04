#!/bin/sh
# pipefail: without it, `go test | tee | ...` exits with the LAST
# command's status and test failures would not fail the step.
set -eo pipefail

apk add --no-cache docker-cli docker-cli-compose iproute2

# Debug: check if artifact was downloaded
ls -la

mkdir -p internal/scheduler/web
tar -xzf ui-dist.tar.gz -C internal/scheduler/web

# Debug: verify extraction
ls -la internal/scheduler/web/dist

if [ "$FORGE_TEST_SHARD_EMPTY" = "1" ]; then
  echo "no timing history yet; shard $FORGE_SHARD_INDEX deferring to shard 0 (fallback: single)"
  exit 0
fi

go build -o /usr/local/bin/forge ./cmd/forge

SUITE_FIXTURE="tests/integration/suite_test.go"
TEST_PKGS=""

if [ -n "${FORGE_TEST_FILES:-}" ]; then
  # We have sharded files/packages.
  # Check if we have any directories. If we do, we can't mix with suite_test.go as a file.
  has_dir=0
  for p in $(echo "$FORGE_TEST_FILES" | tr ',' ' '); do
    if [ -d "$p" ]; then
      has_dir=1
      break
    fi
  done

  if [ "$has_dir" -eq 1 ]; then
    # Use package mode.
    for p in $(echo "$FORGE_TEST_FILES" | tr ',' ' '); do
      # Only include integration tests to avoid running unit tests in this expensive step
      # if the history was poisoned by a previous run.
      case "$p" in
        tests/integration*)
          TEST_PKGS="$TEST_PKGS ./$p"
          ;;
      esac
    done
  else
    # All are files. We can mix them with suite_test.go.
    # Note: suite_test.go is always needed as it contains TestMain (stack setup).
    TEST_PKGS="./$SUITE_FIXTURE"
    for p in $(echo "$FORGE_TEST_FILES" | tr ',' ' '); do
      case "$p" in
        tests/integration*)
          if [ "$p" != "$SUITE_FIXTURE" ]; then
            TEST_PKGS="$TEST_PKGS ./$p"
          fi
          ;;
      esac
    done
  fi
else
  # Cold start or no history for this shard.
  TEST_PKGS="./tests/integration/..."
fi

if [ -z "$TEST_PKGS" ] || [ "$TEST_PKGS" = "./$SUITE_FIXTURE" ]; then
  # If we are in a shard that only got poisoned history (unit tests),
  # just exit successfully. Shard 0 or other shards will handle the actual
  # integration tests if they were sharded correctly.
  if [ -n "${FORGE_TEST_FILES:-}" ]; then
     echo "Shard $FORGE_SHARD_INDEX assigned no integration tests. Skipping."
     exit 0
  fi
fi

# We don't want the exit code here to fail the script
set +e

go test -v -json $TEST_PKGS 2>&1 | tee /tmp/go-test-output.json | forge report stream-go-test
test_status=$?

# Now things can start failing again
set -e

# Normalize report for Forge metrics
forge report from-go-test /tmp/go-test-output.json .forge/test-report.json
exit $test_status