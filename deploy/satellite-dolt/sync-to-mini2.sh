#!/bin/bash
# Sync Dolt databases to mini2 (hub) via remotesapi.
# Run via cron, launchd, or manually on each machine.
#
# Usage: ./sync-to-mini2.sh [database...]
# Default: syncs all databases with 'hub' remotes.
#
# Prerequisites:
#   - Dolt sql-server running with DOLT_REMOTE_PASSWORD in its env
#   - Each database has a 'hub' remote pointing to mini2's remotesapi
#   - Mini2's remotesapi running on port 8000

set -uo pipefail

# Ensure Homebrew and common bin paths are available (needed when run from cron)
export PATH="/opt/homebrew/bin:/usr/local/bin:$HOME/.local/bin:$PATH"
export DOLT_REMOTE_PASSWORD=""

DOLT_DATA="${GT_DOLT_DATA_DIR:-$HOME/gt/.dolt-data}"
LOG="${GT_SYNC_LOG:-/tmp/dolt-sync.log}"

log() { echo "$(date '+%Y-%m-%d %H:%M:%S') $*" | tee -a "$LOG"; }

# Determine which databases to sync
if [ $# -gt 0 ]; then
    DBS="$*"
else
    # Auto-discover databases with 'hub' remotes
    DBS=""
    for dir in "$DOLT_DATA"/*/; do
        db=$(basename "$dir")
        if [ -d "$dir/.dolt" ] && (cd "$dir" && dolt remote -v 2>/dev/null | grep -q "^hub"); then
            DBS="$DBS $db"
        fi
    done
    DBS=$(echo "$DBS" | xargs)  # trim
fi

if [ -z "$DBS" ]; then
    log "SKIP: no databases with 'hub' remotes found"
    exit 0
fi

log "SYNC: starting ($DBS)"

ok=0
fail=0
skip=0

for db in $DBS; do
    cd "$DOLT_DATA/$db" || { log "FAIL $db: directory not found"; fail=$((fail+1)); continue; }

    result=$(DOLT_REMOTE_PASSWORD="" dolt push --user root hub main 2>&1)
    rc=$?

    if [ $rc -eq 0 ]; then
        if echo "$result" | grep -q "up-to-date"; then
            skip=$((skip+1))
        else
            log "OK   $db: pushed"
            ok=$((ok+1))
        fi
    else
        # Try force push for non-fast-forward or uncommitted changes
        if echo "$result" | grep -q "non-fast-forward\|uncommitted changes\|no common ancestor"; then
            result2=$(DOLT_REMOTE_PASSWORD="" dolt push --user root --force hub main 2>&1)
            if [ $? -eq 0 ]; then
                log "OK   $db: force pushed"
                ok=$((ok+1))
            else
                clean=$(echo "$result2" | tr -d '\r' | sed 's/[|/\\-] Uploading\.\.\.//g' | sed '/^[[:space:]]*$/d' | tail -1)
                log "FAIL $db: $clean"
                fail=$((fail+1))
            fi
        else
            clean=$(echo "$result" | tr -d '\r' | sed 's/[|/\\-] Uploading\.\.\.//g' | sed '/^[[:space:]]*$/d' | tail -1)
            log "FAIL $db: $clean"
            fail=$((fail+1))
        fi
    fi
done

log "DONE: pushed=$ok skipped=$skip failed=$fail"
