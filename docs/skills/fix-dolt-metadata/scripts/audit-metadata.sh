#!/bin/bash
# Audit all .beads/metadata.json files against the canonical Dolt server.
# Usage: audit-metadata.sh [--canonical-host HOST] [--canonical-port PORT]

set -euo pipefail

# Canonical server from HQ metadata
HQ_META="$HOME/gt/.beads/metadata.json"
CANONICAL_HOST="${1:-$(python3 -c "import json; print(json.load(open('$HQ_META')).get('dolt_server_host','127.0.0.1'))")}"
CANONICAL_PORT="${2:-$(python3 -c "import json; print(json.load(open('$HQ_META')).get('dolt_server_port',3307))")}"

echo "Canonical Dolt server: $CANONICAL_HOST:$CANONICAL_PORT"
echo "Source: $HQ_META"
echo ""

# Check for rogue local servers
echo "=== Local Dolt Processes ==="
DOLT_PROCS=$(ps aux | grep "dolt sql-server" | grep -v grep || true)
if [ -n "$DOLT_PROCS" ]; then
  echo "$DOLT_PROCS"
  PID=$(echo "$DOLT_PROCS" | awk '{print $2}' | head -1)
  CWD=$(lsof -p "$PID" -Fn 2>/dev/null | grep "^n/" | head -1 | cut -c2-)
  echo "  CWD: $CWD"
  if [[ "$CWD" == *"/.beads/dolt"* ]]; then
    echo "  ⚠ ROGUE SERVER — running from rig .beads/dolt/ instead of .dolt-data/"
  elif [[ "$CWD" == *"/.dolt-data"* ]]; then
    echo "  ✓ Running from correct .dolt-data/ directory"
  else
    echo "  ? Unknown data directory — verify manually"
  fi
else
  echo "  No local dolt server running"
fi
echo ""

# Check canonical server reachability
echo "=== Canonical Server Connectivity ==="
if nc -z -w 3 "$CANONICAL_HOST" "$CANONICAL_PORT" 2>/dev/null; then
  echo "  ✓ $CANONICAL_HOST:$CANONICAL_PORT is reachable"
else
  echo "  ✗ $CANONICAL_HOST:$CANONICAL_PORT is UNREACHABLE"
fi
echo ""

# Audit all metadata files
echo "=== Metadata Audit ==="
TOTAL=0
OK=0
WRONG=0
MISSING_PORT=0

find "$HOME/gt" -maxdepth 5 -name "metadata.json" -path "*/.beads/*" 2>/dev/null | sort | while read f; do
  backend=$(python3 -c "import json; d=json.load(open('$f')); print(d.get('backend',''))" 2>/dev/null)
  [ "$backend" != "dolt" ] && continue

  host=$(python3 -c "import json; d=json.load(open('$f')); print(d.get('dolt_server_host','MISSING'))" 2>/dev/null)
  port=$(python3 -c "import json; d=json.load(open('$f')); print(d.get('dolt_server_port','MISSING'))" 2>/dev/null)
  db=$(python3 -c "import json; d=json.load(open('$f')); print(d.get('dolt_database','MISSING'))" 2>/dev/null)
  short="${f#$HOME/gt/}"

  if [ "$port" = "MISSING" ]; then
    printf "  ⚠ %-50s MISSING_PORT (auto-start risk!)  host=%s db=%s\n" "$short" "$host" "$db"
  elif [ "$host" != "$CANONICAL_HOST" ]; then
    printf "  ✗ %-50s WRONG_HOST=%s  port=%s  db=%s\n" "$short" "$host" "$port" "$db"
  else
    printf "  ✓ %-50s OK  db=%s\n" "$short" "$db"
  fi
done

echo ""
echo "Done. Fix issues with: fix-metadata.sh"
