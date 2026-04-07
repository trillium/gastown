---
name: fix-dolt-metadata
description: |
  Audit and fix all .beads/metadata.json files across Gas Town rigs to ensure
  they point to the correct Dolt server. Also diagnoses rogue auto-started servers,
  circuit breaker lockouts, and missing database schemas. Use when rigs report
  "database not found", after moving Dolt to a new host, or as part of
  infrastructure health checks.
  Trigger phrases: "fix metadata", "fix dolt metadata", "audit metadata",
  "fix rig databases", "dolt metadata", "fix dolt", "dolt health".
version: 0.2.0
---

# Fix Dolt Metadata

Audit and repair all `.beads/metadata.json` files across Gas Town rigs so every
rig connects to the correct Dolt server.

## Background

Gas Town rigs each have a `.beads/metadata.json` that tells `bd` which Dolt
server to connect to. Common failure modes:

1. **Stale metadata** — Dolt moved to a new host but metadata still points to localhost
2. **Missing port** — metadata lacks `dolt_server_port`, allowing `bd` to auto-start
   a rogue local server from the wrong directory (the root cause of most Dolt issues)
3. **Rogue server** — `bd` auto-starts a dolt server from a rig's `.beads/dolt/`
   directory instead of the canonical `~/gt/.dolt-data/`. This server only serves
   databases in its CWD, causing "database not found" for every other rig.
4. **Circuit breaker lockout** — bd's circuit breaker is keyed on port only (not
   host:port), so a failed connection to localhost:3307 blocks connections to
   mini2:3307. Requires process restart to reset.

## Protocol

Execute these phases IN ORDER.

### Phase 1: Determine the Canonical Dolt Server

The canonical server is defined by HQ's metadata:

```bash
cat ~/gt/.beads/metadata.json
```

Extract `dolt_server_host` and `dolt_server_port`. These are the values ALL rigs
should use.

**Current canonical:** mini2 at `192.168.86.29:3307` (LAN IP).

Verify the canonical server is running:

```bash
ssh mini2 "ps aux | grep 'dolt sql' | grep -v grep"
nc -z -w 3 192.168.86.29 3307 && echo "reachable" || echo "UNREACHABLE"
```

### Phase 2: Check for Rogue Local Servers

A rogue server is a dolt process running locally from the wrong directory:

```bash
# Check if any local dolt is running
ps aux | grep "dolt sql" | grep -v grep

# If running, check its CWD — this reveals the data directory it serves
lsof -p <PID> -Fn 2>/dev/null | head -3
# Expected CWD: ~/gt/.dolt-data/
# ROGUE if CWD is: ~/gt/<rig>/.beads/dolt/ (any rig subdirectory)
```

**Why rogue servers appear:** `bd` has auto-start logic. When it can't connect
to the configured server, it starts `dolt sql-server` from the local `.beads/dolt/`
directory. The auto-start is suppressed ONLY when `dolt_server_port` is explicitly
set in `metadata.json` (see `open.go:resolveAutoStart`). Missing port = auto-start.

**Fix:** Kill the rogue server. If you need local dolt temporarily, start it from
`~/gt/.dolt-data/` with `--config config.yaml`.

### Phase 3: Audit All Metadata Files

```bash
~/.openclaw/skills/fix-dolt-metadata/scripts/audit-metadata.sh
```

Or manually:

```bash
CANONICAL_HOST="192.168.86.29"
CANONICAL_PORT=3307

find ~/gt -maxdepth 4 -name "metadata.json" -path "*/.beads/*" 2>/dev/null | sort | while read f; do
  host=$(python3 -c "import json; d=json.load(open('$f')); print(d.get('dolt_server_host','MISSING'))")
  port=$(python3 -c "import json; d=json.load(open('$f')); print(d.get('dolt_server_port','MISSING'))")
  db=$(python3 -c "import json; d=json.load(open('$f')); print(d.get('dolt_database','MISSING'))")
  short="${f#$HOME/gt/}"

  status="OK"
  [ "$host" != "$CANONICAL_HOST" ] && status="WRONG_HOST"
  [ "$port" = "MISSING" ] && status="MISSING_PORT (auto-start risk!)"

  printf "%-50s %-20s host=%-18s port=%-8s db=%s\n" "$short" "$status" "$host" "$port" "$db"
done
```

**Critical:** Any file with `MISSING_PORT` is an auto-start risk — this is the #1
cause of rogue dolt servers.

### Phase 4: Fix Metadata Files

```bash
~/.openclaw/skills/fix-dolt-metadata/scripts/fix-metadata.sh [--dry-run]
```

Or manually:

```bash
CANONICAL_HOST="192.168.86.29"
CANONICAL_PORT=3307

find ~/gt -maxdepth 4 -name "metadata.json" -path "*/.beads/*" 2>/dev/null | sort | while read f; do
  python3 -c "
import json
d = json.load(open('$f'))
changed = False
if d.get('dolt_server_host') != '$CANONICAL_HOST':
    d['dolt_server_host'] = '$CANONICAL_HOST'
    changed = True
if d.get('dolt_server_port') != $CANONICAL_PORT:
    d['dolt_server_port'] = $CANONICAL_PORT
    changed = True
if changed:
    json.dump(d, open('$f', 'w'), indent=2)
    print('FIXED: $f')
else:
    print('OK: $f')
"
done
```

**Don't forget crew/polecat worktrees** — they may have their own `.beads/`
or use redirect files. Check:

```bash
find ~/gt/*/polecats ~/gt/*/crew -name "metadata.json" -path "*/.beads/*" 2>/dev/null
# If they have a "redirect" file instead of metadata.json, they inherit from parent — OK
```

### Phase 5: Ensure Databases Exist on Canonical Server

Compare local rig databases against what the canonical server has:

```bash
# What databases do rigs need?
find ~/gt -maxdepth 4 -name "metadata.json" -path "*/.beads/*" -exec \
  python3 -c "import json,sys; print(json.load(open(sys.argv[1])).get('dolt_database','?'))" {} \; | sort -u

# What does the canonical server have?
ssh mini2 "ls /Users/b/gt/.dolt-data/ | grep -v config | grep -v log | sort"
```

For any missing databases on the server:

```bash
ssh mini2 "cd /Users/b/gt/.dolt-data && mkdir -p <dbname> && cd <dbname> && dolt init --name 'Gas Town' --email 'gastown@local'"
# Then restart the server to pick up new databases:
ssh mini2 "kill <PID> && sleep 2 && cd /Users/b/gt/.dolt-data && nohup dolt sql-server --config config.yaml > dolt-server.log 2>&1 &"
```

**Important:** New databases need `bd init` to create the beads schema:

```bash
cd ~/gt/<rig> && bd init --force --prefix <prefix>
```

### Phase 6: Validate Connectivity

```bash
~/.openclaw/skills/fix-dolt-metadata/scripts/validate-metadata.sh
```

Or manually — test from each rig directory:

```bash
for dir in ~/gt/deacon ~/gt/gastown/mayor/rig ~/gt/beads/mayor/rig; do
  echo "=== $(basename $dir) ==="
  cd "$dir" && bd stats 2>&1 | head -3
done
```

### Phase 7: Handle Circuit Breaker Issues

If `bd` commands fail with "circuit breaker is open" after changing hosts:

**Root cause:** bd's circuit breaker is keyed on port only, not host:port.
A failed probe to the old host (e.g. dead localhost:3307) blocks connections
to the new host (e.g. healthy mini2:3307).

**Fix:** Kill all `bd` and `dolt` processes to reset in-memory circuit breaker state:

```bash
pkill -9 -f "dolt sql-server"
pkill -9 -f "bd "
sleep 2
# Now bd commands will establish fresh connections to the correct host
```

**Note:** This was fixed in bd-ic2 (PR #2604). If running bd >= 0.60.0 with the
fix, circuit breakers are keyed on host:port and this phase can be skipped.
Clear stale state files with: `rm -f /tmp/beads-dolt-circuit-*.json`

### Phase 8: Validate Mail System

`gt mail send` scans ALL rigs' beads databases to resolve recipient addresses.
If ANY rig has a broken schema, mail fails entirely. This is the most fragile
surface — one broken rig blocks all mail.

```bash
# Test mail delivery
gt mail send mayor/ --self -s "Dolt health check" -m "Mail system test" 2>&1
```

If it fails, the error will name the broken rig. Common causes:

1. **"table not found: issues"** — rig has `dolt init` but no `bd init` (no schema)
   ```bash
   cd ~/gt/<broken-rig> && bd init --force --prefix <prefix>
   ```

2. **"database not found"** — database doesn't exist on the Dolt server
   ```bash
   ssh mini2 "cd /Users/b/gt/.dolt-data && mkdir -p <db> && cd <db> && dolt init --name 'Gas Town' --email 'gastown@local'"
   # Restart mini2 dolt to pick up new DB
   ssh mini2 "kill <PID> && sleep 2 && cd /Users/b/gt/.dolt-data && nohup dolt sql-server --config config.yaml > dolt-server.log 2>&1 &"
   # Then init schema locally
   cd ~/gt/<rig> && bd init --force --prefix <prefix>
   ```

3. **`bd init --force` overwrites metadata** — after running `bd init`, re-run
   the fix-metadata script because init resets `dolt_server_host` to `127.0.0.1`:
   ```bash
   ~/gt/gastown/mayor/rig/docs/skills/fix-dolt-metadata/scripts/fix-metadata.sh
   ```

**Always test mail after any schema or metadata changes.**

### Phase 9: Report and Notify

1. File a bead summarizing the audit
2. Nudge any agents that were escalating about Dolt access:

```bash
gt nudge deacon "Dolt metadata fixed. All rigs now point to mini2. Resume patrol."
```

3. Mark stale escalation beads as closed:

```bash
bd close <escalation-id> --reason="Dolt metadata fixed, pointing to canonical server"
```

## Key Files

| File | Purpose |
|------|---------|
| `~/gt/.beads/metadata.json` | HQ metadata — defines canonical server |
| `~/gt/<rig>/.beads/metadata.json` | Per-rig metadata — must match canonical |
| `~/gt/.dolt-data/config.yaml` | Local dolt server config (backup/fallback only) |
| `~/gt/<rig>/.beads/dolt/config.yaml` | Rig-local dolt config (auto-start source — avoid) |

## Known Issues

- **Circuit breaker bug (bd-ic2):** FIXED in PR #2604. Breaker now keyed on
  host:port. Clear stale state: `rm -f /tmp/beads-dolt-circuit-*.json`
- **Dolt auto-discovery:** dolt server only discovers databases present at startup.
  New databases require server restart.
- **No dolt_server_port = auto-start:** This is BY DESIGN in bd (standalone user
  convenience) but catastrophic in Gas Town. Always set the port explicitly.
- **bd init overwrites metadata:** `bd init --force` resets `dolt_server_host` to
  `127.0.0.1`. Always re-run `fix-metadata.sh` after any `bd init`.
- **gt mail scans ALL rigs:** One broken rig schema blocks all mail delivery.
  This is the most fragile integration point. Always validate mail after fixes.
