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

TEST_PKGS=""

for p in $(echo "$FORGE_TEST_FILES" | tr ',' ' '); do TEST_PKGS="$TEST_PKGS ./$p"; done

if [ -z "$TEST_PKGS" ]; then TEST_PKGS="./..."; fi

# We don't want the exit code here to fail the script
set +e

go test -v -json $TEST_PKGS 2>&1 | tee /tmp/go-test-output.json | forge report stream-go-test
test_status=$?

# Now things can start failing again
set -e

# Normalize report for Forge metrics
forge report from-go-test /tmp/go-test-output.json .forge/test-report.json
exit $test_status