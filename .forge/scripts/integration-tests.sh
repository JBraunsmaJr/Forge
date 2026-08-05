#!/bin/sh
# pipefail: without it, `go test | tee | ...` exits with the LAST
# command's status and test failures would not fail the step.
set -eo pipefail

apk add --no-cache docker-cli docker-cli-compose iproute2

# The test image was already built and pushed once by build-image;
# derive its tag here from $FORGE_BUILD_NUMBER (a real env var in this
# container) and export it so the integration suite's buildImage() can
# pull and reuse it instead of every shard rebuilding it from scratch —
# see tests/integration/suite_test.go's pullImage(). Only derive it when
# nothing was already supplied (an externally-set FORGE_IMAGE always
# wins) and a real build number is actually available (a plain local
# run of this script with FORGE_BUILD_NUMBER unset would otherwise
# produce a bogus "test-" tag with nothing after it).
if [ -z "${FORGE_IMAGE:-}" ] && [ -n "${FORGE_BUILD_NUMBER:-}" ]; then
  export FORGE_IMAGE="ghcr.io/jbraunsmajr/forge/forge:test-${FORGE_BUILD_NUMBER}"
fi

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

# FORGE_TEST_FILES holds this shard's assigned top-level test function
# names (e.g. "TestAuth_Login"), not file paths — go test -json has no
# way to report which .go file a test is defined in, so Forge's
# per-test-duration history (and therefore shard assignment) for Go is
# keyed by test name instead. See cmd/forge/cmd_report.go's fromGoTest
# for the full explanation.
#
# ${VAR+set} (not ${VAR:-}) deliberately distinguishes "not running as
# a shard at all" (FORGE_TEST_FILES unset entirely — run everything)
# from "running as a shard that was assigned zero tests" (set to an
# empty string — skip; this can legitimately happen with more shards
# configured than there are test functions).
if [ -n "${FORGE_TEST_FILES+set}" ]; then
  if [ -z "$FORGE_TEST_FILES" ]; then
    echo "Shard ${FORGE_SHARD_INDEX:-?} assigned no tests. Skipping."
    exit 0
  fi
  RUN_REGEX="^($(echo "$FORGE_TEST_FILES" | tr ',' '|'))\$"
else
  # Not running under Forge's split expansion (e.g. a plain local
  # `go test` via this script) — run everything.
  RUN_REGEX=".*"
fi

# We don't want the exit code here to fail the script
set +e

go test -v -json -run "$RUN_REGEX" ./tests/integration/... 2>&1 | tee /tmp/go-test-output.json | forge report stream-go-test
test_status=$?

# Now things can start failing again
set -e

# Normalize report for Forge metrics
forge report from-go-test /tmp/go-test-output.json .forge/test-report.json
exit $test_status