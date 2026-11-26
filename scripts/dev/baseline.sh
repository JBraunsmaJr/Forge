#!/usr/bin/env bash
#
# baseline.sh - record the current health of the codebase.
#
# Runs the build, static checks, unit tests, race detector, and Web UI checks,
# and writes a Markdown report with the outcome and duration of each. The
# report is a snapshot to compare against after a change; it does not gate
# anything, so every step runs even when an earlier one fails.
#
# Usage:
#   scripts/dev/baseline.sh [--no-ui] [--no-race] [--out DIR]
#
# Output:
#   DIR/report.md     the summary (also printed to stdout)
#   DIR/logs/*.log    full output of each step
#
# DIR defaults to .baseline in the repository root, which is gitignored.
#
# Compatible with bash 3.2 so it runs on a stock macOS shell.

set -uo pipefail

usage() {
  sed -n '3,18p' "$0" | sed 's/^# \{0,1\}//'
}

RUN_UI=1
RUN_RACE=1
OUT_DIR=""

while [ $# -gt 0 ]; do
  case "$1" in
    --no-ui)   RUN_UI=0 ;;
    --no-race) RUN_RACE=0 ;;
    --out)
      [ $# -ge 2 ] || { echo "error: --out needs a directory" >&2; exit 2; }
      OUT_DIR="$2"
      shift
      ;;
    -h|--help) usage; exit 0 ;;
    *) echo "error: unknown argument: $1" >&2; usage >&2; exit 2 ;;
  esac
  shift
done

REPO_ROOT="$(cd "$(dirname "$0")/../.." && pwd)"
cd "$REPO_ROOT" || exit 1

[ -n "$OUT_DIR" ] || OUT_DIR="$REPO_ROOT/.baseline"
LOG_DIR="$OUT_DIR/logs"
REPORT="$OUT_DIR/report.md"
mkdir -p "$LOG_DIR"
# Resolve to an absolute path: some steps run from a subdirectory.
OUT_DIR="$(cd "$OUT_DIR" && pwd)"
LOG_DIR="$OUT_DIR/logs"
REPORT="$OUT_DIR/report.md"

# Results are kept in parallel arrays; bash 3.2 has no associative arrays.
STEP_NAMES=()
STEP_STATUS=()
STEP_SECONDS=()
STEP_NOTES=()

record() {
  STEP_NAMES+=("$1")
  STEP_STATUS+=("$2")
  STEP_SECONDS+=("$3")
  STEP_NOTES+=("$4")
}

have() {
  command -v "$1" >/dev/null 2>&1
}

# run_step NAME LOG_SLUG DIR COMMAND...
# Runs COMMAND in DIR, sends its output to LOG_DIR/LOG_SLUG.log, and records
# pass or fail with the elapsed time. Returns the command's exit status.
run_step() {
  local name="$1" slug="$2" dir="$3"
  shift 3
  local log="$LOG_DIR/$slug.log"
  local start=$SECONDS status

  echo "==> $name" >&2
  ( cd "$dir" && "$@" ) >"$log" 2>&1
  status=$?

  if [ $status -eq 0 ]; then
    record "$name" "pass" "$((SECONDS - start))" ""
  else
    record "$name" "FAIL" "$((SECONDS - start))" "exit $status, see logs/$slug.log"
  fi
  return $status
}

skip_step() {
  echo "==> $1 (skipped: $2)" >&2
  record "$1" "skipped" "0" "$2"
}

# count_json_tests FILE ACTION
# Counts test-level events with the given action in `go test -json` output.
# Package-level events carry no "Test" field and are not counted.
count_json_tests() {
  local n
  n=$(grep -c "\"Action\":\"$2\".*\"Test\":\"" "$1" 2>/dev/null)
  echo "${n:-0}"
}

# go_test_step NAME LOG_SLUG [EXTRA_GO_TEST_FLAGS...]
# Runs the unit tests with JSON output and appends pass/fail/skip counts, and
# the number of data race reports, to the recorded note.
go_test_step() {
  local name="$1" slug="$2"
  shift 2
  local log="$LOG_DIR/$slug.log"

  INTEGRATION_SKIP=1 run_step "$name" "$slug" "$REPO_ROOT" \
    go test -count=1 -json "$@" ./...

  local last=$(( ${#STEP_NOTES[@]} - 1 ))
  local passed failed skipped races summary
  passed=$(count_json_tests "$log" pass)
  failed=$(count_json_tests "$log" fail)
  skipped=$(count_json_tests "$log" skip)
  races=$(grep -c 'WARNING: DATA RACE' "$log" 2>/dev/null)
  summary="$passed passed, $failed failed, $skipped skipped"
  if [ "${races:-0}" -gt 0 ]; then
    summary="$summary, $races data race reports"
  fi
  if [ -n "${STEP_NOTES[$last]}" ]; then
    summary="$summary; ${STEP_NOTES[$last]}"
  fi
  STEP_NOTES[$last]="$summary"
}

version_of() {
  if have "$1"; then
    "$@" 2>/dev/null | head -n 1
  else
    echo "not installed"
  fi
}

# --- Go ----------------------------------------------------------------------

if have go; then
  run_step "go build" "go-build" "$REPO_ROOT" go build ./...
  run_step "go vet" "go-vet" "$REPO_ROOT" go vet ./...
  go_test_step "unit tests" "go-test"
  if [ "$RUN_RACE" -eq 1 ]; then
    go_test_step "unit tests (race detector)" "go-test-race" -race
  else
    skip_step "unit tests (race detector)" "--no-race"
  fi
else
  for name in "go build" "go vet" "unit tests" "unit tests (race detector)"; do
    skip_step "$name" "go is not installed"
  done
fi

# --- Web UI ------------------------------------------------------------------

UI_DIR="$REPO_ROOT/ui"
if [ "$RUN_UI" -eq 0 ]; then
  for name in "ui install" "ui build" "ui type check"; do
    skip_step "$name" "--no-ui"
  done
elif ! have npm; then
  for name in "ui install" "ui build" "ui type check"; do
    skip_step "$name" "npm is not installed"
  done
elif run_step "ui install" "ui-install" "$UI_DIR" npm ci; then
  run_step "ui build" "ui-build" "$UI_DIR" npm run build
  run_step "ui type check" "ui-check" "$UI_DIR" npm run check
else
  skip_step "ui build" "ui install failed"
  skip_step "ui type check" "ui install failed"
fi

# --- Report ------------------------------------------------------------------

commit="$(git rev-parse --short HEAD 2>/dev/null || echo unknown)"
if [ -n "$(git status --porcelain 2>/dev/null)" ]; then
  commit="$commit (with uncommitted changes)"
fi

{
  echo "# Forge baseline"
  echo
  echo "| | |"
  echo "| --- | --- |"
  echo "| Recorded | $(date -u +%Y-%m-%dT%H:%M:%SZ) |"
  echo "| Commit | $commit |"
  echo "| Platform | $(uname -sm) |"
  echo "| Go | $(version_of go version) |"
  echo "| Node | $(version_of node --version) |"
  echo "| npm | $(version_of npm --version) |"
  echo "| Docker | $(version_of docker --version) |"
  echo
  echo "| Step | Result | Seconds | Notes |"
  echo "| --- | --- | ---: | --- |"
  i=0
  while [ $i -lt ${#STEP_NAMES[@]} ]; do
    echo "| ${STEP_NAMES[$i]} | ${STEP_STATUS[$i]} | ${STEP_SECONDS[$i]} | ${STEP_NOTES[$i]} |"
    i=$((i + 1))
  done
  echo
  echo "The integration suite is not part of this baseline; it needs Docker and"
  echo "a built image. See .forge/scripts/integration-tests.sh."
} >"$REPORT"

cat "$REPORT"
echo >&2
echo "Report written to $REPORT" >&2
