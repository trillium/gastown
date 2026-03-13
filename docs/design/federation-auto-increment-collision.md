# Federation Auto-Increment Collision

> **Status**: Active bug. Blocks E2E federation testing (me-cgu2, me-82p2).
> **Severity**: P1 — any multi-node write to the `beads` database triggers Error 1062.
> **Filed**: 2026-03-13
> **Related**: beads#2133 (closed, partial fix), dolthub/dolt#3373, dolthub/dolt#7702

## Problem Statement

When two Dolt server instances (macbook→mini2, mini3 satellite) both write to
the same `beads` database and sync via `dolt_pull`/`dolt_push`, INSERT
operations fail with `Error 1062 (HY000): duplicate primary key` on the
`events` table (and potentially 5 other AUTO_INCREMENT tables).

This blocks the Dolt federation E2E tests and makes multi-machine Gas Town
unusable for concurrent writes.

## Reproduction

```
1. macbook creates beads → writes to mini2 Dolt (100.111.197.110:3307)
   events table: AUTO_INCREMENT = 4091, MAX(id) = 4090

2. mini3 satellite creates beads → writes to mini3 Docker Dolt (100.86.9.58:3307)
   events table: AUTO_INCREMENT = 4218, MAX(id) = 4217

3. mini3 cron sync runs (every 5 min):
   - dolt_pull('mini2', 'main')  → merges mini2 rows into mini3
   - dolt_push('--force', 'mini2', 'main') → pushes mini3 rows to mini2
   Result: mini2 now has rows with IDs up to 4217, but its AUTO_INCREMENT
   counter was NOT advanced (stays at ~4091 or wherever it was)

4. macbook runs: bd update me-cgu2 --status=in_progress
   → INSERT INTO events (...) VALUES (...)
   → Dolt assigns AUTO_INCREMENT value 4183 (or similar)
   → Row with id=4183 already exists (pushed from mini3)
   → Error 1062: duplicate primary key given: [4183]
```

## Root Cause

**Dolt's AUTO_INCREMENT counter is per-server-instance with no cross-clone
reconciliation.** This is documented behavior, not a bug in Dolt.

From [DoltHub's blog](https://www.dolthub.com/blog/2023-10-27-uuid-keys/):

> "Dolt branches and clones do not play well with AUTO_INCREMENT primary keys."
> The counter is shared across branches within a single server, but "this
> approach does not work for clones."

The sync script (`satellite-dolt/sync-to-mini2.sh`) uses raw `dolt_pull` and
`dolt_push` SQL procedures inside the Docker container. These do not trigger
`bd`'s post-pull AUTO_INCREMENT fixup (which was added in beads#2133 for `bd`'s
own pull path).

### Observed State (2026-03-13)

| Node | Host | MAX(id) | COUNT(*) | AUTO_INCREMENT |
|------|------|---------|----------|----------------|
| macbook local | 127.0.0.1:3307 | 4090 | 2477 | 4091 |
| mini2 (primary) | 100.111.197.110:3307 | 4198 | 3104 | 4199 |
| mini3 satellite | 100.86.9.58:3307 | 4217 | 3123 | 4218 |

The rows pushed from mini3→mini2 have IDs in ranges that mini2's counter
doesn't know about.

## Affected Tables

Six tables in `beads` use `BIGINT AUTO_INCREMENT PRIMARY KEY`:

| Table | Writes from |
|-------|-------------|
| `events` | Every create, update, close, reopen, label, rename |
| `comments` | `bd comment` |
| `issue_snapshots` | Compaction |
| `compaction_snapshots` | Compaction |
| `wisp_events` | Polecat work lifecycle |
| `wisp_comments` | Polecat annotations |

All six are vulnerable to the same collision if both nodes write to them.

## Prior Art

### beads#2133 — Partial fix (closed)

Added `ALTER TABLE <tbl> AUTO_INCREMENT = MAX(id) + 1` after every
`DOLT_PULL` in `bd`'s pull code path. This works when `bd` is the one doing
the pull. It does NOT help when:

- The satellite cron script does raw `dolt_pull`/`dolt_push` via Docker exec
- `gt` commands write to the database independently of `bd`
- Two nodes write concurrently between syncs

### beads#2466, #2530 — Metadata merge conflicts (closed)

Machine-local values (`dolt_auto_push_commit`, `dolt_auto_push_last`) in the
`metadata` table caused merge conflicts on every pull. Fixed by namespacing
per-clone. Same class of problem — per-clone state in a shared table.

### ops-d2d — Safety-hash ID scheme (open)

Proposes `tag-paddednum-safetyhash` format (e.g., `life-001-a3f`) for
collision-resistant distributed IDs. Addresses the issue ID layer but not the
events table auto_increment.

### dolthub/dolt#7702 — AUTO_INCREMENT race condition (open)

Even within a single server, concurrent transactions can get the same
auto_increment ID. Separate from the federation problem but compounds it.

## Solution Options

### Option A: UUID Primary Keys (DoltHub recommended)

Change all six tables from `BIGINT AUTO_INCREMENT` to `VARCHAR(36) DEFAULT(uuid())`.

**Pros**: Eliminates the problem permanently. Collision-free across any number
of clones. DoltHub's official recommendation. `last_insert_uuid()` function
available since Dolt 2024.

**Cons**: Schema migration across all nodes. Every query that references
`events.id` (ORDER BY, joins, etc.) needs review — UUID ordering is random, not
temporal. Larger storage footprint. Requires coordinated bd version upgrade
across all machines.

**Migration path**:
1. Add `uuid VARCHAR(36) DEFAULT(uuid())` column alongside `id`
2. Backfill UUIDs for existing rows
3. Swap primary key from `id` to `uuid`
4. Drop `id` column
5. Rename `uuid` → `id` (or keep both during transition)

### Option B: Post-Sync AUTO_INCREMENT Reset (Band-aid)

Add `ALTER TABLE <tbl> AUTO_INCREMENT = MAX(id) + 1` to the satellite sync
script for all six tables, on both sides (after pull AND after push).

**Pros**: No schema change. Quick fix. Matches what beads#2133 already does
inside `bd`.

**Cons**: Fragile — any sync path that doesn't call the fixup will break.
Doesn't handle concurrent writes between syncs. A band-aid on a fundamental
architectural mismatch.

**Implementation**:
```bash
# Add to sync-to-mini2.sh after each pull/push:
for tbl in events comments issue_snapshots compaction_snapshots wisp_events wisp_comments; do
  docker exec "$CONTAINER" dolt --host 127.0.0.1 --port 3307 \
    --user root --password '' --no-tls --use-db "$db" \
    sql -q "ALTER TABLE $tbl AUTO_INCREMENT = (SELECT COALESCE(MAX(id),0)+1 FROM $tbl);"
done
```

Also need to reset on mini2 after receiving the push. This requires a
post-push hook or a separate script on mini2.

### Option C: Node-Offset Auto-Increment

Configure each Dolt server with different `auto_increment_increment` and
`auto_increment_offset` values (standard MySQL multi-master pattern).

**Pros**: No schema change. Well-understood MySQL pattern.

**Cons**: Dolt may not support `auto_increment_increment` / `auto_increment_offset`
system variables (needs verification). Fragile if nodes are added. Wastes ID
space. Doesn't survive clone recreation.

### Option D: Application-Level ID Generation

Have `bd` generate IDs explicitly (e.g., snowflake, ULID, or node-prefixed
sequences) instead of relying on database auto_increment.

**Pros**: Full control. Can embed node identity and timestamp. ULIDs are
sortable (unlike UUIDv4). No Dolt-specific behavior dependency.

**Cons**: Requires changes to all INSERT paths in `bd`. More complex than
letting the DB handle it.

## Recommendation

**Short term (unblock E2E tests): Option B.** Add AUTO_INCREMENT reset to the
sync script. This matches the existing beads#2133 pattern and can be deployed
in minutes.

**Long term (federation at scale): Option A (UUIDs) or Option D (ULIDs).**
DoltHub explicitly recommends UUIDs for multi-clone scenarios. ULIDs are
preferable if temporal ordering matters (which it does for events). File this
as a beads feature request with a migration plan.

## Immediate Action Items

1. **Patch `sync-to-mini2.sh`** to reset AUTO_INCREMENT on all six tables after
   pull and push (Option B)
2. **Patch mini2** to reset AUTO_INCREMENT after receiving pushes (either via a
   cron job or a Dolt stored procedure trigger)
3. **File beads issue** for UUID/ULID migration (Option A/D) as a P2 feature
4. **Re-run E2E test** (me-cgu2) after applying the patch
5. **Verify** macbook can write to mini2 without Error 1062

## Appendix: DoltHub References

- [AUTO_INCREMENT vs UUID Primary Keys](https://www.dolthub.com/blog/2023-10-27-uuid-keys/) — Official recommendation against AUTO_INCREMENT for clones
- [last_insert_uuid()](https://www.dolthub.com/blog/2024-04-10-last_insert_uuid/) — UUID convenience function
- [dolthub/dolt#3373](https://github.com/dolthub/dolt/issues/3373) — Per-branch counter fix (single server only)
- [dolthub/dolt#7702](https://github.com/dolthub/dolt/issues/7702) — Concurrent transaction race (open)
- [beads#2133](https://github.com/steveyegge/beads/issues/2133) — Post-pull AUTO_INCREMENT reset fix
