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
# An empty FORGE_TEST_FILES here is NOT itself a "skip" signal — it's
# ambiguous on its own between "this shard should run everything" (shard
# 1 on a cold start) and "this shard was assigned nothing" (any other
# shard, cold start or not). FORGE_TEST_SHARD_EMPTY, checked above, is
# the one authoritative signal for the latter; if we got this far it
# wasn't set, so treat an empty/unset FORGE_TEST_FILES as "nothing to
# filter to" and run everything.
# The suite's actual top-level test names, straight from the compiled
# package. This is ground truth the planner does not have: it only knows
# names that already appear in duration history.
# (POSIX sh + busybox only here -- no bash, no process substitution.)
TMPD=$(mktemp -d)
trap 'rm -rf "$TMPD"' EXIT
go test -list '.*' ./tests/integration/... 2>/dev/null | grep '^Test' | sort -u > "$TMPD/all"

# Zero tests in the whole suite means the package did not build or the
# listing broke. Every downstream check would then read as "nothing to
# do" and pass, which is how a suite stops running without anyone
# noticing. This is the one condition that is always wrong.
if [ ! -s "$TMPD/all" ]; then
  echo "no tests found in ./tests/integration/... -- suite did not build or list" >&2
  exit 1
fi

if [ -n "${FORGE_TEST_FILES:-}" ]; then
  echo "$FORGE_TEST_FILES" | tr ',' '\n' | grep -v '^$' | sort -u > "$TMPD/assigned"
  echo "${FORGE_TEST_FILES_ALL:-}" | tr ',' '\n' | grep -v '^$' | sort -u > "$TMPD/union"

  # Tests that exist but that no shard was assigned. A newly added test has
  # no history row, so shard planning cannot see it and it would never run
  # anywhere -- green builds that silently skip new tests. Each shard claims
  # a stable slice of the leftovers by name hash, so shards agree on the
  # split without having to know each other's assignments.
  grep -Fxv -f "$TMPD/union" "$TMPD/all" > "$TMPD/leftover" || true
  while IFS= read -r t; do
    [ -n "$t" ] || continue
    h=$(printf '%s' "$t" | cksum | cut -d' ' -f1)
    if [ "$(( h % ${FORGE_SHARD_TOTAL:-1} ))" -eq "${FORGE_SHARD_INDEX:-0}" ]; then
      echo "shard ${FORGE_SHARD_INDEX:-0}: picking up unassigned test $t"
      echo "$t" >> "$TMPD/assigned"
    fi
  done < "$TMPD/leftover"

  # Drop assigned names that no longer exist (renamed or deleted tests still
  # sitting in history) so they cannot drag the regex toward zero matches.
  sort -u "$TMPD/assigned" -o "$TMPD/assigned"
  # Names that were assigned AND still exist -- used only to tell a stale
  # assignment apart from a merely small one when reporting below.
  echo "$FORGE_TEST_FILES" | tr ',' '\n' | grep -v '^$' | sort -u > "$TMPD/orig"
  grep -Fx -f "$TMPD/all" "$TMPD/orig" > "$TMPD/matched" || true

  grep -Fx -f "$TMPD/all" "$TMPD/assigned" > "$TMPD/final" || true

  if [ ! -s "$TMPD/final" ]; then
    # Nothing left for this shard once stale names were dropped. Because
    # leftovers are distributed deterministically across ALL shards, the
    # tests are provably covered elsewhere -- so this is a genuine no-op,
    # not a failure. (The suite-wide "no tests at all" case, which is a
    # failure, was already caught above.)
    echo "shard ${FORGE_SHARD_INDEX:-0}: no tests assigned after reconciling against the suite; nothing to do"
    echo "assigned was: ${FORGE_TEST_FILES}"
    exit 0
  fi

  # Loud, but non-fatal: every assigned name being stale means duration
  # history is keyed differently than this suite reports. The run stays
  # green because leftover pickup already covered the tests, and the
  # scheduler re-keys history within a couple of runs.
  if [ ! -s "$TMPD/matched" ]; then
    echo "WARNING: none of this shard's assigned names matched a real test;" >&2
    echo "         duration history looks stale or differently keyed." >&2
    echo "         assigned: ${FORGE_TEST_FILES}" >&2
  fi

  RUN_REGEX="^($(tr '\n' '|' < "$TMPD/final" | sed 's/|$//'))\$"
else
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