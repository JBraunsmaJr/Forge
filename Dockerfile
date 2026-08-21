# --- Stage 1: Build UI ---
FROM node:22-alpine AS uibuilder
WORKDIR /app/ui
COPY ui/ .
RUN rm -rf /app/internal/scheduler/web/dist && npm install && npm run build

# --- Stage 2: Build Go ---
FROM golang:1.26.7-alpine AS gobuilder

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

COPY . .
# Copy the built UI assets (either from the context or from the build above)
COPY --from=uibuilder /app/internal/scheduler/web/dist ./internal/scheduler/web/dist
RUN CGO_ENABLED=0 GOOS=linux GOARCH=amd64 \
    go build -ldflags="-s -w" -o /forge ./cmd/forge

# --- Stage 3: Runtime ---
FROM alpine:3.20

# docker-cli        - agents call docker run/exec to launch job containers
# ca-certificates   - HTTPS to Vault, GitHub raw CDN, etc.
# curl + jq         - used by the init container's shell script
# bash              - for init script and entrypoint
# git               - git operations in some pipeline steps
# netcat-openbsd    - TCP wait in docker-entrypoint.sh before connecting to DB

RUN apk add --no-cache \
        docker-cli \
        ca-certificates \
        curl \
        jq \
        bash \
        git \
        netcat-openbsd \
        postgresql-client \
        python3

COPY --from=gobuilder /forge /forge
COPY scripts/ /scripts/
COPY scripts/docker-entrypoint.sh /docker-entrypoint.sh
RUN chmod +x /docker-entrypoint.sh /scripts/*.sh

ENTRYPOINT ["/docker-entrypoint.sh"]