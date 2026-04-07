#!/bin/bash
# Fix all .beads/metadata.json files to point to the canonical Dolt server.
# Usage: fix-metadata.sh [--dry-run] [--host HOST] [--port PORT]

set -euo pipefail

DRY_RUN=false
CANONICAL_HOST=""
CANONICAL_PORT=""

while [[ $# -gt 0 ]]; do
  case $1 in
    --dry-run) DRY_RUN=true; shift ;;
    --host) CANONICAL_HOST="$2"; shift 2 ;;
    --port) CANONICAL_PORT="$2"; shift 2 ;;
    *) echo "Unknown option: $1"; exit 1 ;;
  esac
done

# Default from HQ metadata
HQ_META="$HOME/gt/.beads/metadata.json"
if [ -z "$CANONICAL_HOST" ]; then
  CANONICAL_HOST=$(python3 -c "import json; print(json.load(open('$HQ_META')).get('dolt_server_host','127.0.0.1'))")
fi
if [ -z "$CANONICAL_PORT" ]; then
  CANONICAL_PORT=$(python3 -c "import json; print(json.load(open('$HQ_META')).get('dolt_server_port',3307))")
fi

echo "Canonical server: $CANONICAL_HOST:$CANONICAL_PORT"
[ "$DRY_RUN" = true ] && echo "DRY RUN — no files will be modified"
echo ""

FIXED=0
SKIPPED=0

find "$HOME/gt" -maxdepth 5 -name "metadata.json" -path "*/.beads/*" 2>/dev/null | sort | while read f; do
  backend=$(python3 -c "import json; d=json.load(open('$f')); print(d.get('backend',''))" 2>/dev/null)
  [ "$backend" != "dolt" ] && continue

  short="${f#$HOME/gt/}"

  result=$(python3 -c "
import json
f = '$f'
d = json.load(open(f))
changed = False
if d.get('dolt_server_host') != '$CANONICAL_HOST':
    d['dolt_server_host'] = '$CANONICAL_HOST'
    changed = True
if d.get('dolt_server_port') != $CANONICAL_PORT:
    d['dolt_server_port'] = $CANONICAL_PORT
    changed = True
if changed:
    if '$DRY_RUN' != 'true':
        json.dump(d, open(f, 'w'), indent=2)
    print('FIXED')
else:
    print('OK')
")

  if [ "$result" = "FIXED" ]; then
    if [ "$DRY_RUN" = true ]; then
      echo "  WOULD FIX: $short"
    else
      echo "  FIXED: $short"
    fi
  else
    echo "  OK: $short"
  fi
done

echo ""
echo "Done. Run validate-metadata.sh to verify connectivity."
