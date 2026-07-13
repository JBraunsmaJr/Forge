#!/bin/sh
# docker-entrypoint.sh
#
# Waits for dependent services to be TCP-reachable before starting Forge.
# This handles the gap between "container healthy" (application-level check)
# and "network path established" (OS routing to the container is ready).
#
# Usage: set as ENTRYPOINT in the Dockerfile.
# The first argument is the Forge subcommand (scheduler, agent, etc.).

set -e

wait_for() {
    HOST="$1"
    PORT="$2"
    LABEL="${3:-$HOST:$PORT}"
    echo "[entrypoint] waiting for $LABEL..."
    until nc -z "$HOST" "$PORT" 2>/dev/null; do
        sleep 1
    done
    echo "[entrypoint] $LABEL is reachable"
}

case "$1" in
  scheduler)
    # Parse host and port from FORGE_DB_URL
    # Format: postgres://user:pass@host:port/dbname
    if [ -n "$FORGE_DB_URL" ]; then
        DB_HOST=$(echo "$FORGE_DB_URL" | sed -E 's|.*@([^:/?]+).*|\1|')
        DB_PORT=$(echo "$FORGE_DB_URL" | sed -E 's|.*:([0-9]+)/.*|\1|')
        DB_PORT="${DB_PORT:-5432}"
        wait_for "$DB_HOST" "$DB_PORT" "postgres"
    fi
    ;;

  agent)
    # Parse host and port from the scheduler URL arg
    SCHEDULER_URL="${2:-http://scheduler:8080}"
    SCHED_HOST=$(echo "$SCHEDULER_URL" | sed -E 's|https?://([^:/]+).*|\1|')
    SCHED_PORT=$(echo "$SCHEDULER_URL" | sed -E 's|https?://[^:/]+:([0-9]+).*|\1|')

    if [ "$SCHED_PORT" = "$SCHEDULER_URL" ]; then
        if echo "$SCHEDULER_URL" | grep -q "^https://"; then
            SCHED_PORT=443
        else
            SCHED_PORT=80
        fi
    fi
    wait_for "$SCHED_HOST" "$SCHED_PORT" "scheduler"
    ;;
esac

exec /forge "$@"