#!/bin/bash
set -euo pipefail

apk add --no-cache docker-cli docker-cli-compose iproute2

# See integration-tests.sh for why this is derived here rather than
# read from an env: block, and why it's guarded rather than
# unconditional (this script runs under set -u, so referencing
# FORGE_BUILD_NUMBER unguarded would hard-fail if it's ever unset).
if [ -z "${FORGE_IMAGE:-}" ] && [ -n "${FORGE_BUILD_NUMBER:-}" ]; then
  export FORGE_IMAGE="ghcr.io/jbraunsmajr/forge/forge:test-${FORGE_BUILD_NUMBER}"
fi

# Debug: check if artifact was downloaded
ls -la

mkdir -p internal/scheduler/web
tar -xzf ui-dist.tar.gz -C internal/scheduler/web

# Debug: verify extraction
ls -la internal/scheduler/web/dist
INTEGRATION_SKIP=1 go test -v ./...