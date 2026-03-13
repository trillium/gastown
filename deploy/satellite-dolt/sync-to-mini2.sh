#!/bin/bash
# Sync satellite Dolt databases to mini2 (primary)
# Run via cron or launchd on each satellite machine.
#
# Usage: ./sync-to-mini2.sh [database...]
# Default: syncs 'beads' database only.

set -euo pipefail

# Ensure Homebrew and common bin paths are available (needed when run from cron)
export PATH="/opt/homebrew/bin:/usr/local/bin:$PATH"

MINI2_HOST="100.111.197.110"
MINI2_PORT="8000"
CONTAINER="dolt-satellite"
DBS="${@:-beads}"
LOG="/tmp/dolt-sync.log"

log() { echo "$(date '+%Y-%m-%d %H:%M:%S') $*" >> "$LOG"; }

# Check if mini2 is reachable
if ! curl -s --connect-timeout 3 -o /dev/null "http://${MINI2_HOST}:${MINI2_PORT}/"; then
    log "SKIP: mini2 unreachable"
    exit 0
fi

# Check if container is running
if ! docker ps --format '{{.Names}}' | grep -q "^${CONTAINER}$"; then
    log "SKIP: container ${CONTAINER} not running"
    exit 0
fi

for db in $DBS; do
    log "SYNC: ${db} -> mini2"

    # Pull from mini2 first (merge any new changes)
    result=$(docker exec "$CONTAINER" dolt --host 127.0.0.1 --port 3307 \
        --user root --password '' --no-tls --use-db "$db" \
        sql -q "CALL dolt_pull('mini2', 'main');" 2>&1) || true
    log "PULL ${db}: ${result}"

    # Push back to mini2
    result=$(docker exec "$CONTAINER" dolt --host 127.0.0.1 --port 3307 \
        --user root --password '' --no-tls --use-db "$db" \
        sql -q "CALL dolt_push('--force', 'mini2', 'main');" 2>&1) || true
    log "PUSH ${db}: ${result}"
done

log "DONE"
