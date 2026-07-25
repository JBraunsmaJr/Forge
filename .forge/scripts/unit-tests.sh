#!/bin/bash

apk add --no-cache docker-cli docker-cli-compose iproute2

# Debug: check if artifact was downloaded
ls -la

mkdir -p internal/scheduler/web
tar -xzf ui-dist.tar.gz -C internal/scheduler/web

# Debug: verify extraction
ls -la internal/scheduler/web/dist
INTEGRATION_SKIP=1 go test -v ./...