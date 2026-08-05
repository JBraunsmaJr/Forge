#!/bin/bash
set -euo pipefail

apk add --no-cache docker-cli docker-cli-compose iproute2

# Pull the test image if provided to speed up tests
if [ -n "${FORGE_IMAGE:-}" ]; then
  echo "Pulling test image: $FORGE_IMAGE"
  docker pull "$FORGE_IMAGE" || true
fi

# Debug: check if artifact was downloaded
ls -la

mkdir -p internal/scheduler/web
tar -xzf ui-dist.tar.gz -C internal/scheduler/web

# Debug: verify extraction
ls -la internal/scheduler/web/dist
INTEGRATION_SKIP=1 go test -v ./...