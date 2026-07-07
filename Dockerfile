# syntax=docker/dockerfile:1
# ── Stage 1: Build ────────────────────────────────────────────────────────────
FROM golang:1.26-alpine AS builder

WORKDIR /app

# git is required by GOPROXY=direct to fetch modules from GitHub.
# ca-certificates is needed for HTTPS connections during the fetch.
RUN apk add --no-cache git ca-certificates

# Download dependencies first (cached unless go.mod/go.sum change).
COPY go.mod go.sum ./
ENV GOPROXY=direct
ENV GONOSUMDB=*
ENV GOINSECURE=*
RUN go mod download

# Build the binary.
COPY . .
RUN CGO_ENABLED=0 GOOS=linux GOARCH=amd64 \
    go build -ldflags="-s -w" -o /forge ./cmd/forge

# ── Stage 2: Runtime ──────────────────────────────────────────────────────────
FROM alpine:3.20

# docker-cli        — agents call docker run/exec to launch job containers
# ca-certificates   — HTTPS to Vault, GitHub raw CDN, etc.
# curl + jq         — used by the init container's shell script
# bash              — for init script and entrypoint
# git               — git operations in some pipeline steps
# netcat-openbsd    — TCP wait in docker-entrypoint.sh before connecting to DB
RUN apk add --no-cache \
        docker-cli \
        ca-certificates \
        curl \
        jq \
        bash \
        git \
        netcat-openbsd \
        postgresql-client

COPY --from=builder /forge /forge
COPY scripts/docker-entrypoint.sh /docker-entrypoint.sh

# Healthchecks are defined per-service in compose.yml, not here.
# Defining HEALTHCHECK in the Dockerfile applies it to every container using
# this image (scheduler, agent, init), but only the scheduler serves HTTP.

ENTRYPOINT ["/docker-entrypoint.sh"]