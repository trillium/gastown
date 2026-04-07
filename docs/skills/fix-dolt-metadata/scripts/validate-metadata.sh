#!/bin/bash
# Validate that all rigs can connect to Dolt after metadata fixes.
# Usage: validate-metadata.sh

set -euo pipefail

echo "=== Dolt Connectivity Validation ==="
echo ""

PASS=0
FAIL=0

# Test from key rig directories
DIRS=(
  "$HOME/gt"
  "$HOME/gt/deacon"
  "$HOME/gt/gastown/mayor/rig"
  "$HOME/gt/beads/mayor/rig"
  "$HOME/gt/massage_tracker"
  "$HOME/gt/massage/witness"
  "$HOME/gt/personal"
  "$HOME/gt/life"
  "$HOME/gt/ops"
  "$HOME/gt/openclaw"
)

for dir in "${DIRS[@]}"; do
  [ ! -d "$dir/.beads" ] && continue
  name="${dir#$HOME/gt/}"
  [ "$name" = "$HOME/gt" ] && name="HQ"

  if cd "$dir" && bd stats 2>&1 | head -1 | grep -q "Issue Database Status"; then
    printf "  ✓ %-40s PASS\n" "$name"
    PASS=$((PASS + 1))
  else
    err=$(cd "$dir" && bd stats 2>&1 | head -2)
    printf "  ✗ %-40s FAIL\n" "$name"
    echo "    $err"
    FAIL=$((FAIL + 1))
  fi
done

echo ""
echo "Results: $PASS passed, $FAIL failed"

if [ "$FAIL" -gt 0 ]; then
  echo ""
  echo "Troubleshooting:"
  echo "  1. Check canonical server: nc -z -w 3 <host> 3307"
  echo "  2. Circuit breaker stuck? pkill -9 -f 'bd ' && retry"
  echo "  3. Database missing on server? ssh mini2 'ls /Users/b/gt/.dolt-data/'"
  echo "  4. Schema missing? cd <rig> && bd init --force --prefix <prefix>"
  exit 1
fi
