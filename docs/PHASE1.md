# Phase 1 — guided tour of what we built

## The layers in this repo

| Code | Layer | Job |
|------|-------|-----|
| `internal/storage` | Storage | Snapshot + CoW clone of PGDATA |
| `internal/postgres` | Compute | init/start/stop/prepare one Postgres |
| `internal/branch` | Orchestration | Wire storage + compute into `branch create` |
| `internal/meta` | Notebook | Remember ports/paths (Phase 2 → real DB) |
| `cmd/ardent` | CLI | Human interface |

## Why APFS instead of ZFS here?

You are on macOS. ZFS is not installed. APFS can still **share file extents**
via `clonefile` until a write occurs — same *idea* as ZFS CoW, file-by-file.

| | ZFS | APFS (our Phase 1) |
|--|-----|---------------------|
| Snapshot | metadata on dataset | CoW-copy tree → `snapshots/N` |
| Clone | `zfs clone` | CoW-copy snapshot → `branches/N` |
| Extra space after writes | new blocks only | new blocks only (per rewritten file) |

When you move to Linux, `storage.Detect` prefers ZFS automatically.

## Consistency: why we stop main

`CHECKPOINT` flushes dirty pages. Stopping main around the snapshot makes the
frozen directory look like a clean shutdown — easiest mental model for Phase 1.

Later (Phase 3) you snapshot a **replica** so production never stops.

## PrepareClone — the Postgres-specific gotchas

After cloning files you must:

1. Delete `postmaster.pid` (belongs to the parent instance)
2. Change `port`
3. Remove `standby.signal` / `recovery.signal` if present

Otherwise the branch either refuses to start or tries to be a replica of something that is not its primary.

## Timing

Watch the stderr lines:

```text
[storage/apfs] snapshot ...
[storage/apfs] clone ...
[postgres] ready in ...
```

Grow `main` later and re-run `branch create` — create time should stay roughly flat.
That is the whole Phase 1 thesis.
